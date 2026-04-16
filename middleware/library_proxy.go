package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"pelicula-api/httputil"
	"strings"
)

// ── Filesystem helpers ────────────────────────────────────────────────────────

// copyFile copies src to dst using a buffered io.Copy, removing dst on error.
// The destination file is closed explicitly (not via defer) so that a flush
// error at close time is detected and the partial file is cleaned up.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		in.Close()
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		in.Close()
		os.Remove(dst)
		return err
	}
	// Close dst first: this is where write-back errors (fsync) surface.
	if err := out.Close(); err != nil {
		in.Close()
		os.Remove(dst)
		return err
	}
	in.Close()
	return nil
}

// moveFile moves src to dst, falling back to copy+remove when os.Rename fails
// across different filesystems (common on Synology bind mounts).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Cross-device: copy then remove.
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// fsOpResult records the outcome of a single filesystem operation.
type fsOpResult struct {
	op  string // "moved", "hardlinked", "symlinked", "kept", "skipped", "failed"
	err string // non-empty only when op == "failed"
}

// applyFSOps iterates items and performs the filesystem operation dictated by
// strategy for each item that has a SourcePath. Accepted strategy values:
//   - "import" (alias: "migrate")  — move the file into the library
//   - "link"   (alias: "symlink")  — create a symlink in the library
//   - "hardlink"                   — create a hard link in the library
//   - "register" (alias: "keep")   — no-op; files are already in place
//
// Items are modified in place: SourcePath and DestPath are updated on success.
// allowedSrcRoots and allowedDstRoots default to the production values when nil.
// Returns a slice of per-item results parallel to items. Items that are skipped
// because their path is not under an allowed prefix are included with op="skipped"
// and a non-empty err reason.
func applyFSOps(items []ApplyItem, strategy string, allowedSrcRoots, allowedDstRoots []string) []fsOpResult {
	results := make([]fsOpResult, len(items))
	for i := range results {
		results[i] = fsOpResult{op: "kept"}
	}

	// Normalise legacy strategy names to canonical ones.
	switch strategy {
	case "migrate":
		strategy = "import"
	case "symlink":
		strategy = "link"
	case "keep":
		strategy = "register"
	}
	if strategy == "register" {
		return results
	}
	if allowedSrcRoots == nil {
		allowedSrcRoots = browseRoots()
	}
	if allowedDstRoots == nil {
		libs := GetLibraries()
		allowedDstRoots = make([]string, 0, len(libs))
		for _, lib := range libs {
			allowedDstRoots = append(allowedDstRoots, lib.ContainerPath())
		}
	}

	for i := range items {
		item := &items[i]
		if item.SourcePath == "" {
			continue
		}
		src := filepath.Clean(item.SourcePath)
		if !isUnderPrefixes(src, allowedSrcRoots) {
			results[i] = fsOpResult{op: "skipped", err: "path not allowed"}
			continue
		}

		dst := item.DestPath
		if dst == "" {
			if item.Type == "movie" {
				dst = suggestedMoviePath(item.Title, item.Year, filepath.Base(src))
			} else {
				dst = suggestedTVPath(item.Title, 0, filepath.Base(src))
			}
		}
		dst = filepath.Clean(dst)
		if !isUnderPrefixes(dst, allowedDstRoots) {
			results[i] = fsOpResult{op: "skipped", err: "destination path not allowed"}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			slog.Warn("import: mkdir failed", "component", "library", "dst", dst, "error", err)
			results[i] = fsOpResult{op: "failed", err: err.Error()}
			continue
		}

		switch strategy {
		case "import":
			if err := moveFile(src, dst); err != nil {
				slog.Warn("import: move failed", "component", "library",
					"src", src, "dst", dst, "error", err)
				results[i] = fsOpResult{op: "failed", err: err.Error()}
			} else {
				item.SourcePath = dst
				item.DestPath = dst
				results[i] = fsOpResult{op: "moved"}
			}
		case "hardlink":
			if _, err := os.Lstat(dst); os.IsNotExist(err) {
				if err := os.Link(src, dst); err != nil {
					slog.Warn("import: hardlink failed", "component", "library",
						"src", src, "dst", dst, "error", err)
					results[i] = fsOpResult{op: "failed", err: err.Error()}
				} else {
					item.DestPath = dst
					results[i] = fsOpResult{op: "hardlinked"}
				}
			} else {
				// dst already exists — treat as success (idempotent)
				item.DestPath = dst
				results[i] = fsOpResult{op: "hardlinked"}
			}
		case "link":
			if _, err := os.Lstat(dst); os.IsNotExist(err) {
				if err := os.Symlink(src, dst); err != nil {
					slog.Warn("import: symlink failed", "component", "library",
						"src", src, "dst", dst, "error", err)
					results[i] = fsOpResult{op: "failed", err: err.Error()}
				} else {
					item.DestPath = dst
					results[i] = fsOpResult{op: "symlinked"}
				}
			} else {
				// dst already exists — treat as success (idempotent)
				item.DestPath = dst
				results[i] = fsOpResult{op: "symlinked"}
			}
		}
	}
	return results
}

// ── Transcode proxy handlers ──────────────────────────────────────────────────

// RetranscodeRequest is the body accepted by handleLibraryRetranscode.
type RetranscodeRequest struct {
	Files   []string `json:"files"`
	Profile string   `json:"profile"`
}

// RetranscodeResult summarises the outcome of a batch retranscode request.
type RetranscodeResult struct {
	Queued int      `json:"queued"`
	Failed int      `json:"failed"`
	Errors []string `json:"errors,omitempty"`
}

// handleTranscodeProfiles proxies GET and POST /api/procula/profiles.
// GET lists all profiles; POST creates or updates a profile.
func handleTranscodeProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var upstream *http.Request
	var err error
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		upstream, err = http.NewRequest(http.MethodPost, proculaURL+"/api/procula/profiles", r.Body)
		if err == nil {
			upstream.Header.Set("Content-Type", "application/json")
		}
	} else {
		upstream, err = http.NewRequest(http.MethodGet, proculaURL+"/api/procula/profiles", nil)
	}
	if err != nil {
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
		upstream.Header.Set("X-API-Key", key)
	}
	resp, err := services.client.Do(upstream)
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		slog.Warn("failed to stream profiles response", "component", "library", "error", err)
	}
}

// handleDeleteTranscodeProfile proxies DELETE /api/procula/profiles/{name}.
func handleDeleteTranscodeProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := r.PathValue("name")
	if name == "" {
		httputil.WriteError(w, "profile name required", http.StatusBadRequest)
		return
	}
	upstream, err := http.NewRequest(http.MethodDelete, proculaURL+"/api/procula/profiles/"+url.PathEscape(name), nil)
	if err != nil {
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
		upstream.Header.Set("X-API-Key", key)
	}
	resp, err := services.client.Do(upstream)
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.WriteHeader(resp.StatusCode)
}

// handleLibraryRetranscode accepts a list of library file paths and a profile
// name, then enqueues a manual transcode job in Procula for each file.
// Only paths under a registered library container path are accepted.
func handleLibraryRetranscode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req RetranscodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Files) == 0 || req.Profile == "" {
		httputil.WriteError(w, "files and profile are required", http.StatusBadRequest)
		return
	}

	libs := GetLibraries()
	libRoots := make([]string, 0, len(libs))
	for _, lib := range libs {
		libRoots = append(libRoots, lib.ContainerPath())
	}
	result := RetranscodeResult{}
	for _, path := range req.Files {
		clean := filepath.Clean(path)
		if !isUnderPrefixes(clean, libRoots) {
			result.Failed++
			result.Errors = append(result.Errors, path+": not under a library path")
			continue
		}
		body, _ := json.Marshal(map[string]string{"path": clean, "profile": req.Profile})
		proculaReq, err := http.NewRequest(http.MethodPost, proculaURL+"/api/procula/transcode", bytes.NewReader(body))
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, path+": "+err.Error())
			continue
		}
		proculaReq.Header.Set("Content-Type", "application/json")
		if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
			proculaReq.Header.Set("X-API-Key", key)
		}
		resp, err := services.client.Do(proculaReq)
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, path+": "+err.Error())
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Sprintf("%s: procula HTTP %d", path, resp.StatusCode))
			continue
		}
		result.Queued++
	}

	httputil.WriteJSON(w, result)
}

// handleJobResub proxies POST /api/procula/jobs/{id}/resub — re-triggers
// Bazarr subtitle search for an existing pipeline job.
func handleJobResub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteError(w, "job id required", http.StatusBadRequest)
		return
	}
	upstream, err := http.NewRequest(http.MethodPost, proculaURL+"/api/procula/jobs/"+url.PathEscape(id)+"/resub", nil)
	if err != nil {
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
		upstream.Header.Set("X-API-Key", key)
	}
	resp, err := services.client.Do(upstream)
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}

// handleJobRetry proxies POST /api/procula/jobs/{id}/retry — re-queues
// a failed procula job for processing.
func handleJobRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteError(w, "job id required", http.StatusBadRequest)
		return
	}
	upstream, err := http.NewRequest(http.MethodPost, proculaURL+"/api/procula/jobs/"+url.PathEscape(id)+"/retry", nil)
	if err != nil {
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
		upstream.Header.Set("X-API-Key", key)
	}
	resp, err := services.client.Do(upstream)
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}

// handleLibraryResub looks up a media file in Radarr/Sonarr by path and
// triggers Bazarr subtitle search via Procula.
// Accepts POST with body {"path": "/media/..."}.
func handleLibraryResub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	clean := filepath.Clean(req.Path)
	resubLibs := GetLibraries()
	resubRoots := make([]string, 0, len(resubLibs))
	for _, lib := range resubLibs {
		resubRoots = append(resubRoots, lib.ContainerPath())
	}
	if !isUnderPrefixes(clean, resubRoots) {
		httputil.WriteError(w, "path not under a library path", http.StatusBadRequest)
		return
	}

	sonarrKey, radarrKey, _ := services.Keys()

	// Try Radarr first.
	if radarrKey != "" {
		data, err := services.ArrGet(radarrURL, radarrKey, "/api/v3/movie?path="+url.QueryEscape(clean))
		if err == nil {
			var movies []map[string]any
			if json.Unmarshal(data, &movies) == nil {
				for _, m := range movies {
					if id, ok := m["id"].(float64); ok && id > 0 {
						sendSubSearch(w, r, "radarr", int(id), 0)
						return
					}
				}
			}
		}
	}

	// Fall back to Sonarr: look up by episode file path.
	if sonarrKey != "" {
		data, err := services.ArrGet(sonarrURL, sonarrKey, "/api/v3/episodefile?path="+url.QueryEscape(clean))
		if err == nil {
			var epFiles []map[string]any
			if json.Unmarshal(data, &epFiles) == nil {
				for _, ef := range epFiles {
					seriesID, _ := ef["seriesId"].(float64)
					epIDs, _ := ef["episodeIds"].([]any)
					if seriesID > 0 && len(epIDs) > 0 {
						epID, _ := epIDs[0].(float64)
						sendSubSearch(w, r, "sonarr", int(seriesID), int(epID))
						return
					}
				}
			}
		}
	}

	httputil.WriteError(w, "file not found in Radarr or Sonarr", http.StatusNotFound)
}

// sendSubSearch calls Procula's subtitle search endpoint for the given arr item.
func sendSubSearch(w http.ResponseWriter, r *http.Request, arrType string, arrID, episodeID int) {
	payload, _ := json.Marshal(map[string]any{
		"arr_type":   arrType,
		"arr_id":     arrID,
		"episode_id": episodeID,
	})
	upstream, err := http.NewRequest(http.MethodPost, proculaURL+"/api/procula/subtitles/search", bytes.NewReader(payload))
	if err != nil {
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
		upstream.Header.Set("X-API-Key", key)
	}
	resp, err := services.client.Do(upstream)
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}
