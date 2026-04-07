package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RunWorker processes jobs from the queue sequentially.
// It runs forever and should be called in a goroutine.
func RunWorker(q *Queue, configDir, peliculaAPI string) {
	slog.Info("worker started", "component", "pipeline")
	for id := range q.pending {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic in job — worker continuing", "component", "pipeline", "job_id", id, "panic", r)
				}
			}()
			processJob(q, id, configDir, peliculaAPI)
		}()
	}
}

func processJob(q *Queue, id, configDir, peliculaAPI string) {
	job, ok := q.Get(id)
	if !ok {
		slog.Warn("job not found, skipping", "component", "pipeline", "job_id", id)
		return
	}

	// Skip cancelled jobs that were queued before cancellation
	if job.State == StateCancelled {
		return
	}

	// Create a cancellable context so Cancel() can kill a running FFmpeg
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.registerCancel(id, cancel)
	defer q.unregisterCancel(id)

	slog.Info("starting job", "component", "pipeline", "job_id", id, "title", job.Source.Title, "type", job.Source.Type)

	// Mark as processing
	_ = q.Update(id, func(j *Job) {
		j.State = StateProcessing
		j.Stage = StageValidate
	})

	settings := GetSettings()

	// ── Stage 1: Validate ─────────────────────────────────────────────────
	job, _ = q.Get(id)
	if settings.ValidationEnabled {
		result, failReason := Validate(job)

		_ = q.Update(id, func(j *Job) {
			j.Validation = &result
			j.Progress = 0.33
		})

		if !result.Passed {
			slog.Warn("validation failed", "component", "pipeline", "job_id", id, "reason", failReason)
			_ = q.Update(id, func(j *Job) {
				j.State = StateFailed
				j.Stage = StageValidate
				j.Error = failReason
			})

			// Remove the bad file from the library only when explicitly enabled in settings
			if settings.DeleteOnFailure && job.Source.Path != "" {
				if !isAllowedPath(job.Source.Path) {
					slog.Warn("refusing to remove file outside allowed directories", "component", "pipeline", "path", job.Source.Path)
				} else if err := os.Remove(job.Source.Path); err == nil {
					slog.Info("removed bad file", "component", "pipeline", "path", job.Source.Path)
				} else if !os.IsNotExist(err) {
					slog.Error("could not remove bad file", "component", "pipeline", "error", err)
				}
			} else if job.Source.Path != "" {
				slog.Info("validation failed — file left in place (delete_on_failure=false)", "component", "pipeline", "path", job.Source.Path)
			}

			// Ask pelicula-api to blocklist in *arr so the watcher re-searches
			if job.Source.DownloadHash != "" {
				blocklist(job, peliculaAPI, failReason)
			}

			// Notify the dashboard
			WriteValidationFailedNotification(job, configDir, failReason)
			return
		}

		slog.Info("validation passed", "component", "pipeline", "job_id", id, "video", result.Checks.Codecs.Video, "audio", result.Checks.Codecs.Audio)

		// Check for missing subtitle languages (informational — does not fail the job)
		if result.Checks.Codecs != nil {
			if missing := checkMissingSubtitles(result.Checks.Codecs.Subtitles); len(missing) > 0 {
				slog.Info("missing subtitle languages", "component", "pipeline", "job_id", id, "langs", missing)
				_ = q.Update(id, func(j *Job) { j.MissingSubs = missing })
			}
		}
	} else {
		slog.Info("validation skipped (disabled in settings)", "component", "pipeline", "job_id", id)
		_ = q.Update(id, func(j *Job) { j.Progress = 0.33 })
	}

	// ── Stage 2: Process ─────────────────────────────────────────────────
	if job, _ = q.Get(id); job.State == StateCancelled {
		return
	}
	_ = q.Update(id, func(j *Job) {
		j.Stage = StageProcess
		j.Progress = 0.5
	})
	job, _ = q.Get(id)
	if outputPath, err := maybeTranscode(ctx, q, job, configDir); err != nil {
		slog.Warn("transcoding failed, proceeding with original", "component", "pipeline", "job_id", id, "error", err)
	} else if outputPath != "" {
		_ = q.Update(id, func(j *Job) { j.Source.Path = outputPath })
		job, _ = q.Get(id)
	}

	// ── Stage 3: Catalog ─────────────────────────────────────────────────
	if job, _ = q.Get(id); job.State == StateCancelled {
		return
	}
	_ = q.Update(id, func(j *Job) {
		j.Stage = StageCatalog
		j.Progress = 0.9
	})
	job, _ = q.Get(id)
	if settings.CatalogEnabled {
		Catalog(job, configDir, peliculaAPI)
	} else {
		slog.Info("catalog skipped (disabled in settings)", "component", "pipeline", "job_id", id)
	}

	// ── Done ──────────────────────────────────────────────────────────────
	if job, _ = q.Get(id); job.State == StateCancelled {
		return
	}
	_ = q.Update(id, func(j *Job) {
		j.State = StateCompleted
		j.Stage = StageDone
		j.Progress = 1.0
	})

	slog.Info("job completed", "component", "pipeline", "job_id", id, "title", job.Source.Title)
}

// maybeTranscode runs FFmpeg if TRANSCODING_ENABLED=true and a matching profile
// exists for the job's video codec/resolution. Returns the output path (or "" to
// skip transcoding) and any non-fatal error.
func maybeTranscode(ctx context.Context, q *Queue, job *Job, configDir string) (string, error) {
	if !GetSettings().TranscodingEnabled {
		return "", nil
	}

	if job.Validation == nil || job.Validation.Checks.Codecs == nil {
		return "", nil
	}
	codecs := job.Validation.Checks.Codecs

	profiles, err := LoadProfiles(configDir)
	if err != nil || len(profiles) == 0 {
		return "", nil
	}

	profile := FindMatchingProfile(profiles, codecs.Video, codecs.Height)
	if profile == nil {
		return "", nil // no matching profile → skip transcoding
	}

	slog.Info("transcoding job", "component", "pipeline", "job_id", job.ID, "profile", profile.Name)

	outputPath, err := Process(ctx, job, profile, func(pct float64) {
		// Map pct (0–1) into the 50–90% progress window
		progress := 0.5 + pct*0.4
		q.Update(job.ID, func(j *Job) { j.Progress = progress }) //nolint:errcheck
	})
	return outputPath, err
}

// isAllowedJobPath checks that path is under a known media directory.
// Used at job-creation time to prevent arbitrary paths from being submitted.
func isAllowedJobPath(path string) bool {
	clean := filepath.Clean(path)
	for _, prefix := range []string{"/downloads", "/movies", "/tv", "/processing"} {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

// isAllowedPath checks that path is under a directory where deletion on
// validation failure is safe. /movies and /tv are intentionally excluded —
// files there are already imported; deleting them in response to a failed
// validation job would be destructive, and an attacker with control over the
// webhook path could trigger it.
func isAllowedPath(path string) bool {
	clean := filepath.Clean(path)
	for _, prefix := range []string{"/downloads", "/processing"} {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

// blocklist asks pelicula-api to remove the download from the *arr queue
// and blocklist the release so the watcher triggers a new search.
func blocklist(job *Job, peliculaAPI, reason string) {
	category := job.Source.ArrType // "radarr" or "sonarr"
	payload := map[string]any{
		"hash":      job.Source.DownloadHash,
		"category":  category,
		"blocklist": true,
		"reason":    reason,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		slog.Error("blocklist marshal error", "component", "pipeline", "error", err)
		return
	}

	url := fmt.Sprintf("%s/api/pelicula/downloads/cancel", peliculaAPI)

	httpClient := &http.Client{Timeout: 10 * time.Second}
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err := httpClient.Post(url, "application/json", bytes.NewReader(data))
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
			continue
		}
		hash := job.Source.DownloadHash
		if len(hash) > 8 {
			hash = hash[:8]
		}
		slog.Info("blocklisted release", "component", "pipeline", "category", category, "hash", hash, "reason", reason)
		return
	}
	slog.Error("failed to blocklist after 3 attempts", "component", "pipeline", "error", lastErr)
}
