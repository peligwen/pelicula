package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"pelicula/internal/store"
)

const runtimeLookupTimeout = 5 * time.Second

// handleImportHook receives Radarr/Sonarr "Download" webhooks and queues the
// imported file for validation. It is not behind a session; the *arr apps
// authenticate with the shared X-Webhook-Secret header instead.
func (s *Server) handleImportHook(w http.ResponseWriter, r *http.Request) {
	// An unset secret must never mean "open": refuse until one is configured.
	if s.Cfg.WebhookSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "webhook secret is not configured")
		return
	}
	if !secretMatches(r.Header.Get("X-Webhook-Secret"), s.Cfg.WebhookSecret) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeError(w, http.StatusBadRequest, "could not read body")
		}
		return
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	event, _ := raw["eventType"].(string)
	switch {
	case strings.EqualFold(event, "test"):
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	case !strings.EqualFold(event, "download"):
		s.log().Debug("ignoring webhook event", "event", event)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}

	job, err := jobFromPayload(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid webhook payload: "+err.Error())
		return
	}
	job.RuntimeMin = s.lookupRuntime(r.Context(), job.ArrType, job.ArrID)

	// A retried delivery for a file that is already queued gets the existing
	// job back (the store dedupes on path).
	if err := s.Store.EnqueueJob(r.Context(), &job); err != nil {
		s.log().Error("enqueue job", "path", job.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "could not queue job")
		return
	}
	s.log().Info("import queued", "job", job.ID, "arr", job.ArrType, "title", job.Title, "path", job.Path)
	s.kick()
	writeJSON(w, http.StatusOK, map[string]any{"status": "queued", "job_id": job.ID})
}

// secretMatches compares in constant time. Hashing first makes the compare
// length-independent.
func secretMatches(got, want string) bool {
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// jobFromPayload maps a Radarr ("movie") or Sonarr ("series") Download
// payload to a job. RuntimeMin is left for the caller to fill in.
func jobFromPayload(raw map[string]any) (store.Job, error) {
	job := store.Job{DownloadID: str(raw, "downloadId")}
	switch {
	case isObject(raw["movie"]):
		movie, _ := raw["movie"].(map[string]any)
		file, _ := raw["movieFile"].(map[string]any)
		job.ArrType = "radarr"
		job.ArrID = num(movie, "id")
		job.Title = str(movie, "title")
		job.Path = str(file, "path")
		job.Size = num64(file, "size")
	case isObject(raw["series"]):
		series, _ := raw["series"].(map[string]any)
		file, _ := raw["episodeFile"].(map[string]any)
		job.ArrType = "sonarr"
		job.ArrID = num(series, "id")
		job.Title = str(series, "title")
		job.Path = str(file, "path")
		job.Size = num64(file, "size")
		if eps, _ := raw["episodes"].([]any); len(eps) > 0 {
			if ep, ok := eps[0].(map[string]any); ok {
				job.EpisodeID = num(ep, "id")
			}
		}
	default:
		return job, errors.New("no 'movie' or 'series' in payload")
	}
	if job.ArrID == 0 {
		return job, errors.New("missing " + map[string]string{"radarr": "movie", "sonarr": "series"}[job.ArrType] + " id")
	}
	if job.Path == "" {
		return job, errors.New("no file path in payload")
	}
	return job, nil
}

func isObject(v any) bool {
	_, ok := v.(map[string]any)
	return ok
}

// lookupRuntime asks *arr for the title's runtime in minutes (the pipeline's
// duration check needs it). Best effort: 0 on any failure.
func (s *Server) lookupRuntime(ctx context.Context, arrType string, id int) int {
	ctx, cancel := context.WithTimeout(ctx, runtimeLookupTimeout)
	defer cancel()
	var (
		m   map[string]any
		err error
	)
	switch {
	case arrType == "radarr" && s.Radarr != nil:
		m, err = s.Radarr.GetMovie(ctx, id)
	case arrType == "sonarr" && s.Sonarr != nil:
		m, err = s.Sonarr.GetSeriesByID(ctx, id)
	default:
		return 0
	}
	if err != nil {
		s.log().Debug("runtime lookup failed", "arr", arrType, "id", id, "err", err)
		return 0
	}
	return num(m, "runtime")
}
