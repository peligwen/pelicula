package procula

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"status": "ok",
		"queue":  s.queue.Status(),
	})
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f ListFilter

	if at := q.Get("action_type"); at != "" {
		f.ActionType = at
	}
	if stateStr := q.Get("state"); stateStr != "" {
		f.State = JobState(stateStr)
	}
	if limitStr := q.Get("limit"); limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			f.Limit = n // List() enforces the 1000 cap
		}
	}
	if sinceStr := q.Get("since"); sinceStr != "" {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			f.Since = t
		}
	}

	writeJSON(w, s.queue.List(f))
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	if storageStateFn() == StorageStateCritical {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "300")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
			"error":   "storage_critical",
			"message": "Procula has paused new job admission until storage usage drops below the warning threshold.",
		})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
	var source JobSource
	if err := json.NewDecoder(r.Body).Decode(&source); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	if source.Path == "" || source.ArrType == "" {
		writeError(w, "path and arr_type are required", http.StatusBadRequest)
		return
	}
	if !isAllowedJobPath(source.Path) {
		writeError(w, "path not under an allowed media directory", http.StatusBadRequest)
		return
	}

	job, err := s.queue.Create(source)
	if err != nil {
		writeError(w, "failed to create job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("job created", "component", "api", "job_id", job.ID, "arr_type", job.Source.ArrType, "title", job.Source.Title)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, job)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.queue.Get(id)
	if !ok {
		writeError(w, "job not found", http.StatusNotFound)
		return
	}
	writeJSON(w, job)
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.queue.Retry(id); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	job, _ := s.queue.Get(id)
	slog.Info("job retry", "component", "api", "job_id", id, "attempt", job.RetryCount)
	emitEvent(PipelineEvent{
		Type:      EventJobRetried,
		JobID:     id,
		Title:     job.Source.Title,
		Year:      job.Source.Year,
		MediaType: job.Source.Type,
		Details:   map[string]any{"retry_count": job.RetryCount},
		Message:   "Job queued for retry",
	})
	writeJSON(w, job)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.queue.Cancel(id); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	job, _ := s.queue.Get(id)
	slog.Info("job cancelled", "component", "api", "job_id", id)
	emitEvent(PipelineEvent{
		Type:      EventJobCancelled,
		JobID:     id,
		Title:     job.Source.Title,
		Year:      job.Source.Year,
		MediaType: job.Source.Type,
		Message:   "Job cancelled",
	})
	writeJSON(w, job)
}

// handleResubJob is retained for route compatibility but is no longer active.
// Use POST /api/procula/actions with action "subtitle_search" instead.
func (s *Server) handleResubJob(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusGone)
	json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
		"error":  "endpoint removed, use POST /api/procula/actions",
		"action": "subtitle_search",
	})
}

// handleSubSearch is retained for route compatibility but is no longer active.
// Use POST /api/procula/actions with action "subtitle_search" instead.
func (s *Server) handleSubSearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusGone)
	json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
		"error":  "endpoint removed, use POST /api/procula/actions",
		"action": "subtitle_search",
	})
}

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	events := ReadFeed(s.configDir)
	if events == nil {
		events = []NotificationEvent{}
	}
	writeJSON(w, events)
}

func handleStorage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, buildStorageReport())
}

func handleStorageScan(w http.ResponseWriter, r *http.Request) {
	computeFolderSizes()
	writeJSON(w, buildStorageReport())
}

func handleUpdates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, getCachedUpdate())
}
