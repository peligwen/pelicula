package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RunWorker processes jobs from the queue sequentially.
// It runs forever and should be called in a goroutine.
func RunWorker(q *Queue, configDir, peliculaAPI string) {
	log.Println("[pipeline] worker started")
	for id := range q.pending {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[pipeline] panic in job %s: %v — worker continuing", id, r)
				}
			}()
			processJob(q, id, configDir, peliculaAPI)
		}()
	}
}

func processJob(q *Queue, id, configDir, peliculaAPI string) {
	job, ok := q.Get(id)
	if !ok {
		log.Printf("[pipeline] job %s not found, skipping", id)
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

	log.Printf("[pipeline] starting job %s: %s (%s)", id, job.Source.Title, job.Source.Type)

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
			log.Printf("[pipeline] validation FAILED for job %s: %s", id, failReason)
			_ = q.Update(id, func(j *Job) {
				j.State = StateFailed
				j.Stage = StageValidate
				j.Error = failReason
			})

			// Remove the bad file from the library
			if job.Source.Path != "" {
				if !isAllowedPath(job.Source.Path) {
					log.Printf("[pipeline] refusing to remove file outside allowed directories: %s", job.Source.Path)
				} else if err := os.Remove(job.Source.Path); err == nil {
					log.Printf("[pipeline] removed bad file: %s", job.Source.Path)
				} else if !os.IsNotExist(err) {
					log.Printf("[pipeline] could not remove bad file: %v", err)
				}
			}

			// Ask pelicula-api to blocklist in *arr so the watcher re-searches
			if job.Source.DownloadHash != "" {
				blocklist(job, peliculaAPI, failReason)
			}

			// Notify the dashboard
			WriteValidationFailedNotification(job, configDir, failReason)
			return
		}

		log.Printf("[pipeline] validation passed for job %s (video=%s audio=%s)",
			id, result.Checks.Codecs.Video, result.Checks.Codecs.Audio)
	} else {
		log.Printf("[pipeline] validation skipped for job %s (disabled in settings)", id)
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
		log.Printf("[pipeline] transcoding failed for job %s: %v (proceeding with original)", id, err)
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
		log.Printf("[pipeline] catalog skipped for job %s (disabled in settings)", id)
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

	log.Printf("[pipeline] completed job %s: %s", id, job.Source.Title)
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

	log.Printf("[pipeline] transcoding job %s with profile %q", job.ID, profile.Name)

	outputPath, err := Process(ctx, job, profile, func(pct float64) {
		// Map pct (0–1) into the 50–90% progress window
		progress := 0.5 + pct*0.4
		q.Update(job.ID, func(j *Job) { j.Progress = progress }) //nolint:errcheck
	})
	return outputPath, err
}

// isAllowedPath checks that path is under one of the expected media directories.
// This prevents accidental deletion of files outside the media library.
func isAllowedPath(path string) bool {
	clean := filepath.Clean(path)
	for _, prefix := range []string{"/downloads", "/movies", "/tv", "/processing"} {
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
		log.Printf("[pipeline] blocklist marshal error: %v", err)
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
		log.Printf("[pipeline] blocklisted %s/%s (reason: %s)", category, hash, reason)
		return
	}
	log.Printf("[pipeline] failed to blocklist after 3 attempts: %v", lastErr)
}
