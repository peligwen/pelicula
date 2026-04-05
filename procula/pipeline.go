package main

import (
	"bytes"
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

	log.Printf("[pipeline] starting job %s: %s (%s)", id, job.Source.Title, job.Source.Type)

	// Mark as processing
	_ = q.Update(id, func(j *Job) {
		j.State = StateProcessing
		j.Stage = StageValidate
	})

	// ── Stage 1: Validate ─────────────────────────────────────────────────
	job, _ = q.Get(id)
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

	// ── Stage 2: Process (Phase 5 — stub) ────────────────────────────────
	if job, _ = q.Get(id); job.State == StateCancelled {
		return
	}
	_ = q.Update(id, func(j *Job) {
		j.Stage = StageProcess
		j.Progress = 0.5
	})
	// TODO: transcoding, extraction, audio normalization

	// ── Stage 3: Catalog ─────────────────────────────────────────────────
	if job, _ = q.Get(id); job.State == StateCancelled {
		return
	}
	_ = q.Update(id, func(j *Job) {
		j.Stage = StageCatalog
		j.Progress = 0.9
	})
	job, _ = q.Get(id)
	Catalog(job, configDir, peliculaAPI)

	// ── Done ──────────────────────────────────────────────────────────────
	_ = q.Update(id, func(j *Job) {
		j.State = StateCompleted
		j.Stage = StageDone
		j.Progress = 1.0
	})

	log.Printf("[pipeline] completed job %s: %s", id, job.Source.Title)
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
