package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"pelicula/internal/store"
)

// handleFailure reacts to a job that failed validation: mark the grabbed
// release failed in *arr (which blocklists it), delete the bad file through
// the *arr API, and ask *arr to search again. Every step logs and continues
// on error. Only the file whose path equals job.Path is ever deleted.
func (w *Worker) handleFailure(ctx context.Context, job *store.Job) {
	switch job.ArrType {
	case "radarr":
		w.failMovie(ctx, job)
	case "sonarr":
		w.failEpisode(ctx, job)
	default:
		w.log.Warn("blocklist skipped: unknown arr type", "job", job.ID, "arr_type", job.ArrType)
	}
}

func (w *Worker) failMovie(ctx context.Context, job *store.Job) {
	c := w.d.Radarr
	if c == nil {
		w.log.Warn("blocklist skipped: radarr client not configured", "job", job.ID)
		return
	}
	id := job.ArrID

	w.step("radarr mark history failed", job, func() error {
		return markFailed(ctx, c, fmt.Sprintf("movieId=%d&eventType=1", id), job.DownloadID)
	})
	w.step("radarr delete movie file", job, func() error {
		movie, err := c.GetMovie(ctx, id)
		if err != nil {
			return err
		}
		file, _ := movie["movieFile"].(map[string]any)
		if file == nil {
			w.log.Info("no movie file to delete", "job", job.ID, "movie", id)
			return nil
		}
		if p, _ := file["path"].(string); p != job.Path {
			w.log.Info("movie file path differs from job path, not deleting", "job", job.ID, "movie", id)
			return nil
		}
		fileID, ok := toInt(file["id"])
		if !ok || fileID <= 0 {
			return fmt.Errorf("movie file has no id")
		}
		return c.DeleteMovieFile(ctx, fileID)
	})
	w.step("radarr search", job, func() error {
		return c.TriggerCommand(ctx, map[string]any{"name": "MoviesSearch", "movieIds": []int{id}})
	})
}

func (w *Worker) failEpisode(ctx context.Context, job *store.Job) {
	c := w.d.Sonarr
	if c == nil {
		w.log.Warn("blocklist skipped: sonarr client not configured", "job", job.ID)
		return
	}
	id, ep := job.ArrID, job.EpisodeID

	query := fmt.Sprintf("seriesId=%d&episodeId=%d&eventType=1", id, ep)
	if ep == 0 {
		query = fmt.Sprintf("seriesId=%d&eventType=1", id)
	}
	w.step("sonarr mark history failed", job, func() error {
		return markFailed(ctx, c, query, job.DownloadID)
	})
	w.step("sonarr delete episode file", job, func() error {
		files, err := c.GetEpisodeFiles(ctx, id)
		if err != nil {
			return err
		}
		for _, f := range files {
			if p, _ := f["path"].(string); p != job.Path {
				continue
			}
			fileID, ok := toInt(f["id"])
			if !ok || fileID <= 0 {
				return fmt.Errorf("episode file has no id")
			}
			return c.DeleteEpisodeFile(ctx, fileID)
		}
		w.log.Info("no episode file matches job path, not deleting", "job", job.ID, "series", id)
		return nil
	})
	w.step("sonarr search", job, func() error {
		if ep == 0 {
			w.log.Info("no episode id, skipping search", "job", job.ID, "series", id)
			return nil
		}
		return c.TriggerCommand(ctx, map[string]any{"name": "EpisodeSearch", "episodeIds": []int{ep}})
	})
}

// markFailed looks the grab up in history and marks it failed.
func markFailed(ctx context.Context, c ArrClient, query, downloadID string) error {
	records, err := c.GetHistory(ctx, query)
	if err != nil {
		return err
	}
	rec := pickGrab(records, downloadID)
	if rec == nil {
		return fmt.Errorf("no grabbed history record for %q", query)
	}
	histID, ok := toInt(rec["id"])
	if !ok || histID <= 0 {
		return fmt.Errorf("history record has no id")
	}
	return c.MarkHistoryFailed(ctx, histID)
}

// pickGrab returns the history record whose downloadId equals downloadID
// (case-insensitive), else the most recently grabbed record, else nil.
func pickGrab(records []map[string]any, downloadID string) map[string]any {
	if downloadID != "" {
		for _, r := range records {
			if s, _ := r["downloadId"].(string); strings.EqualFold(s, downloadID) {
				return r
			}
		}
	}
	var best map[string]any
	var bestAt time.Time
	var bestID int
	for _, r := range records {
		s, _ := r["date"].(string)
		at, _ := time.Parse(time.RFC3339Nano, s)
		id, _ := toInt(r["id"])
		if best == nil || at.After(bestAt) || (at.Equal(bestAt) && id > bestID) {
			best, bestAt, bestID = r, at, id
		}
	}
	return best
}

// toInt reads a JSON number that may have been decoded as float64, json.Number
// or a Go integer.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case string:
		i, err := strconv.Atoi(n)
		return i, err == nil
	}
	return 0, false
}
