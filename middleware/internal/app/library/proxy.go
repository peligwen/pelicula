package library

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"

	"pelicula-api/httputil"
)

// ── Transcode proxy handlers ──────────────────────────────────────────────────

// RetranscodeRequest is the body accepted by HandleLibraryRetranscode.
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

// HandleTranscodeProfiles handles GET and POST /api/procula/profiles.
// GET lists all profiles; POST creates or updates a profile.
func (h *Handler) HandleTranscodeProfiles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var raw []byte
	var err error
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			httputil.WriteError(w, "read body: "+readErr.Error(), http.StatusBadRequest)
			return
		}
		raw, err = h.Procula.CreateProfile(ctx, body)
	} else if r.Method == http.MethodGet {
		raw, err = h.Procula.ListProfiles(ctx)
	} else {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err != nil {
		httputil.WriteError(w, "procula unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw) //nolint:errcheck
}

// HandleDeleteTranscodeProfile deletes a transcode profile by name.
func (h *Handler) HandleDeleteTranscodeProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := r.PathValue("name")
	if name == "" {
		httputil.WriteError(w, "profile name required", http.StatusBadRequest)
		return
	}
	if err := h.Procula.DeleteProfile(r.Context(), name); err != nil {
		httputil.WriteError(w, "procula unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleLibraryRetranscode accepts a list of library file paths and a profile
// name, then enqueues a manual transcode job in Procula for each file.
// Only paths under a registered library container path are accepted.
func (h *Handler) HandleLibraryRetranscode(w http.ResponseWriter, r *http.Request) {
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

	libs := h.GetLibraries()
	libRoots := make([]string, 0, len(libs))
	for _, lib := range libs {
		libRoots = append(libRoots, lib.ContainerPath())
	}
	result := RetranscodeResult{}
	for _, path := range req.Files {
		clean := filepath.Clean(path)
		if !IsUnderPrefixes(clean, libRoots) {
			result.Failed++
			result.Errors = append(result.Errors, path+": not under a library path")
			continue
		}
		_, err := h.Procula.EnqueueAction(r.Context(), map[string]any{
			"action": "transcode",
			"target": map[string]string{"path": clean},
			"params": map[string]string{"profile": req.Profile},
		}, "")
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, path+": "+err.Error())
			continue
		}
		result.Queued++
	}

	httputil.WriteJSON(w, result)
}

// HandleJobResub re-triggers Bazarr subtitle search for an existing pipeline
// job by looking up the job's source arr_type/arr_id/episode_id from procula
// and dispatching a subtitle_search action.
func (h *Handler) HandleJobResub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteError(w, "job id required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	jobRaw, err := h.Procula.GetJob(ctx, id)
	if err != nil {
		httputil.WriteError(w, "procula unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}

	var job struct {
		Source struct {
			Path    string `json:"path"`
			ArrType string `json:"arr_type"`
			ArrID   int    `json:"arr_id"`
			EpID    int    `json:"episode_id"`
		} `json:"source"`
	}
	if err := json.Unmarshal(jobRaw, &job); err != nil {
		httputil.WriteError(w, "parse job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	raw, err := h.Procula.EnqueueAction(ctx, map[string]any{
		"action": "subtitle_search",
		"target": map[string]any{
			"path":       job.Source.Path,
			"arr_type":   job.Source.ArrType,
			"arr_id":     job.Source.ArrID,
			"episode_id": job.Source.EpID,
		},
	}, "")
	if err != nil {
		httputil.WriteError(w, "procula unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw) //nolint:errcheck
}

// HandleJobRetry re-queues a failed procula job for processing.
func (h *Handler) HandleJobRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteError(w, "job id required", http.StatusBadRequest)
		return
	}
	raw, err := h.Procula.RetryJob(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, "procula unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw) //nolint:errcheck
}

// HandleLibraryResub looks up a media file in Radarr/Sonarr by path and
// triggers Bazarr subtitle search via Procula.
// Accepts POST with body {"path": "/media/..."}.
func (h *Handler) HandleLibraryResub(w http.ResponseWriter, r *http.Request) {
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
	resubLibs := h.GetLibraries()
	resubRoots := make([]string, 0, len(resubLibs))
	for _, lib := range resubLibs {
		resubRoots = append(resubRoots, lib.ContainerPath())
	}
	if !IsUnderPrefixes(clean, resubRoots) {
		httputil.WriteError(w, "path not under a library path", http.StatusBadRequest)
		return
	}

	sonarrKey, radarrKey, _ := h.Svc.Keys()

	// Try Radarr first.
	if radarrKey != "" {
		movies, err := h.Svc.RadarrClient().GetMoviesByPath(r.Context(), "/api/v3", clean)
		if err == nil {
			for _, m := range movies {
				if id, ok := m["id"].(float64); ok && id > 0 {
					h.sendSubSearch(w, r, "radarr", int(id), 0)
					return
				}
			}
		}
	}

	// Fall back to Sonarr.
	if sonarrKey != "" {
		epFiles, err := h.Svc.SonarrClient().GetEpisodeFilesByPath(r.Context(), "/api/v3", clean)
		if err == nil {
			for _, ef := range epFiles {
				seriesID, _ := ef["seriesId"].(float64)
				epIDs, _ := ef["episodeIds"].([]any)
				if seriesID > 0 && len(epIDs) > 0 {
					epID, _ := epIDs[0].(float64)
					h.sendSubSearch(w, r, "sonarr", int(seriesID), int(epID))
					return
				}
			}
		}
	}

	httputil.WriteError(w, "file not found in Radarr or Sonarr", http.StatusNotFound)
}

// sendSubSearch dispatches a subtitle_search action via the procula action bus.
func (h *Handler) sendSubSearch(w http.ResponseWriter, r *http.Request, arrType string, arrID, episodeID int) {
	raw, err := h.Procula.EnqueueAction(r.Context(), map[string]any{
		"action": "subtitle_search",
		"target": map[string]any{
			"arr_type":   arrType,
			"arr_id":     arrID,
			"episode_id": episodeID,
		},
	}, "")
	if err != nil {
		httputil.WriteError(w, "procula unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw) //nolint:errcheck
}
