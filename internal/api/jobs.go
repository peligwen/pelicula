package api

import (
	"errors"
	"net/http"
	"strconv"

	"pelicula/internal/store"
)

const defaultJobLimit = 50

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	limit := defaultJobLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = n // the store caps it
	}
	jobs, err := s.Store.ListJobs(r.Context(), limit)
	if err != nil {
		s.log().Error("list jobs", "err", err)
		writeError(w, http.StatusInternalServerError, "could not list jobs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid job id")
		return
	}
	job, err := s.Store.GetJob(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		s.log().Error("get job", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "could not load job")
		return
	}
	if job.Status == store.JobQueued || job.Status == store.JobRunning {
		writeError(w, http.StatusConflict, "job is already "+string(job.Status))
		return
	}
	if err := s.Store.RequeueJob(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) { // raced with the worker
			writeError(w, http.StatusConflict, "job is no longer finished")
			return
		}
		s.log().Error("requeue job", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "could not requeue job")
		return
	}
	s.kick()
	noContent(w)
}
