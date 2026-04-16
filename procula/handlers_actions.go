package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// handleManualTranscode creates a transcoding-only job for an existing library file.
// The file must already be under /media/ (not /downloads).
func (s *Server) handleManualTranscode(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Path    string `json:"path"`
		Profile string `json:"profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Path == "" || req.Profile == "" {
		writeError(w, "path and profile are required", http.StatusBadRequest)
		return
	}

	// Manual transcode is only valid for files already in the library.
	clean := filepath.Clean(req.Path)
	if !isLibraryPath(clean) {
		writeError(w, "path must be under /media/", http.StatusBadRequest)
		return
	}

	// Stat the file to confirm it exists and get its size.
	fi, err := os.Stat(clean)
	if err != nil {
		writeError(w, "file not found or not accessible", http.StatusBadRequest)
		return
	}
	if fi.IsDir() {
		writeError(w, "path must be a file, not a directory", http.StatusBadRequest)
		return
	}

	// Derive a human-readable title from the parent directory (Plex-style naming).
	// Skip the parent if it is a known library root slug (e.g. "movies", "tv", "anime") —
	// in that case the filename itself is the best available title.
	title := strings.TrimSuffix(fi.Name(), filepath.Ext(fi.Name()))
	if parent := filepath.Base(filepath.Dir(clean)); parent != "" {
		isLibraryRoot := false
		for _, lib := range getProculaLibraries() {
			if parent == lib.Slug {
				isLibraryRoot = true
				break
			}
		}
		if !isLibraryRoot {
			title = parent
		}
	}

	source := JobSource{
		Path:    clean,
		Size:    fi.Size(),
		Title:   title,
		ArrType: "radarr", // placeholder; manual jobs aren't tied to an arr instance
		Type:    "movie",
	}

	job, err := s.queue.Create(source)
	if err != nil {
		writeError(w, "failed to create job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := s.queue.Update(job.ID, func(j *Job) {
		j.ManualProfile = req.Profile
	}); err != nil {
		writeError(w, "failed to set profile: "+err.Error(), http.StatusInternalServerError)
		return
	}

	job, _ = s.queue.Get(job.ID)
	slog.Info("manual transcode job created", "component", "api", "job_id", job.ID, "profile", req.Profile, "title", title)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, job)
}

// handleCreateAction creates an action-bus job from an ActionRequest.
// When ?wait=N is set (max 10 seconds) the handler blocks until the job
// reaches a terminal state and returns the result inline.
func (s *Server) handleCreateAction(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req ActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	def := Lookup(req.Action)
	if def == nil {
		writeError(w, "unknown action: "+req.Action, http.StatusBadRequest)
		return
	}

	params := map[string]any{}
	for k, v := range req.Params {
		params[k] = v
	}
	if req.Target.Path != "" {
		params["path"] = req.Target.Path
	}
	if req.Target.ArrType != "" {
		params["arr_type"] = req.Target.ArrType
	}
	if req.Target.ArrID != 0 {
		params["arr_id"] = float64(req.Target.ArrID)
	}
	if req.Target.EpisodeID != 0 {
		params["episode_id"] = float64(req.Target.EpisodeID)
	}

	title := ""
	if req.Target.Path != "" {
		title = filepath.Base(req.Target.Path)
	}
	source := JobSource{
		Path:    req.Target.Path,
		ArrType: req.Target.ArrType,
		ArrID:   req.Target.ArrID,
		Type:    mediaTypeFromPath(req.Target.Path),
		Title:   title,
	}

	job, err := s.queue.createActionJob(source, req.Action, params)
	if err != nil {
		writeError(w, "create job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	waitSecs := 0
	if v := r.URL.Query().Get("wait"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			waitSecs = n
		}
	}
	if waitSecs > 10 {
		waitSecs = 10
	}
	if waitSecs > 0 {
		final, err := s.queue.Wait(job.ID, time.Duration(waitSecs)*time.Second)
		if err != nil && final == nil {
			writeError(w, err.Error(), http.StatusGatewayTimeout)
			return
		}
		res := ActionResult{JobID: final.ID, State: string(final.State), Error: final.Error, Result: final.Result}
		writeJSON(w, res)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, ActionResult{JobID: job.ID, State: string(StateQueued)})
}

// handleListActionRegistry returns all registered actions as JSON.
func (s *Server) handleListActionRegistry(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, List())
}

// handleCatalogFlags returns every row in the catalog_flags table,
// sorted error > warn > info, newest first within each bucket.
func (s *Server) handleCatalogFlags(w http.ResponseWriter, r *http.Request) {
	rows, err := AllFlagged(s.db)
	if err != nil {
		writeError(w, "flags query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []CatalogFlagRow{}
	}
	writeJSON(w, map[string]any{"rows": rows})
}

// handleListBlockedReleases returns all rows in blocked_releases, newest first.
func (s *Server) handleListBlockedReleases(w http.ResponseWriter, r *http.Request) {
	rows, err := ListBlockedReleases(s.db)
	if err != nil {
		writeError(w, "query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []BlockedRelease{}
	}
	writeJSON(w, rows)
}

// handleDeleteBlockedRelease removes a blocked release by id and calls
// middleware to delete the entry from *arr's blocklist.
func (s *Server) handleDeleteBlockedRelease(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id == 0 {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	blocklistID, err := DeleteBlockedRelease(s.db, id)
	if err != nil {
		writeError(w, err.Error(), http.StatusNotFound)
		return
	}

	if blocklistID > 0 {
		// Best-effort: remove from *arr blocklist via middleware.
		req, _ := http.NewRequest(http.MethodDelete,
			fmt.Sprintf("%s/api/pelicula/catalog/blocklist/%d", s.peliculaAPI, blocklistID),
			nil)
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 400 {
				slog.Warn("failed to remove *arr blocklist entry", "component", "replace",
					"blocklist_id", blocklistID)
			}
		} else {
			slog.Warn("failed to remove *arr blocklist entry", "component", "replace",
				"blocklist_id", blocklistID)
		}
	}

	writeJSON(w, map[string]any{"deleted": id})
}
