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

	// Manual transcode jobs skip Validate and CatalogEarly — the file is
	// already in the library and the user explicitly chose a profile.
	if job.ManualProfile != "" {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		q.registerCancel(id, cancel)
		defer q.unregisterCancel(id)
		runManualTranscode(ctx, q, id, configDir, peliculaAPI)
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

	settings := GetSettings(appDB)

	// ── Stage 1: Validate ─────────────────────────────────────────────────
	job, _ = q.Get(id)
	if settings.ValidationEnabled {
		result, failReason := Validate(job)

		_ = q.Update(id, func(j *Job) {
			j.Validation = &result
			j.Progress = 0.33
		})

		checksDetail := map[string]any{
			"integrity": result.Checks.Integrity,
			"duration":  result.Checks.Duration,
			"sample":    result.Checks.Sample,
		}
		if result.Checks.Codecs != nil {
			checksDetail["video"] = result.Checks.Codecs.Video
			checksDetail["audio"] = result.Checks.Codecs.Audio
			checksDetail["width"] = result.Checks.Codecs.Width
			checksDetail["height"] = result.Checks.Codecs.Height
		}

		if !result.Passed {
			slog.Warn("validation failed", "component", "pipeline", "job_id", id, "reason", failReason)
			_ = q.Update(id, func(j *Job) {
				j.State = StateFailed
				j.Stage = StageValidate
				j.Error = failReason
			})

			emitEvent(PipelineEvent{
				Type:      EventValidationFailed,
				JobID:     id,
				Title:     job.Source.Title,
				Year:      job.Source.Year,
				MediaType: job.Source.Type,
				Stage:     string(StageValidate),
				Details:   checksDetail,
				Message:   failReason,
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

		emitEvent(PipelineEvent{
			Type:      EventValidationPassed,
			JobID:     id,
			Title:     job.Source.Title,
			Year:      job.Source.Year,
			MediaType: job.Source.Type,
			Stage:     string(StageValidate),
			Details:   checksDetail,
			Message:   "Validation passed",
		})

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

	// ── Stage 2: Catalog (early) ──────────────────────────────────────────
	// Run before transcoding so Jellyfin sees the original immediately —
	// the file is already on disk (hardlinked by *arr). The user can start
	// watching while the sidecar is being generated.
	if job, _ = q.Get(id); job.State == StateCancelled {
		return
	}
	_ = q.Update(id, func(j *Job) {
		j.Stage = StageCatalog
		j.Progress = 0.4
	})
	job, _ = q.Get(id)
	if settings.CatalogEnabled {
		CatalogEarly(job, configDir, peliculaAPI)
	} else {
		slog.Info("catalog skipped (disabled in settings)", "component", "pipeline", "job_id", id)
	}

	// ── Stage 3: Dual Subtitles ──────────────────────────────────────────
	// Generate stacked ASS sidecar files (e.g. Movie.en-es.ass) before
	// transcoding so Jellyfin picks them up in the late catalog refresh.
	if job, _ = q.Get(id); job.State == StateCancelled {
		return
	}
	_ = q.Update(id, func(j *Job) {
		j.Stage = StageDualSub
		j.Progress = 0.42
	})
	job, _ = q.Get(id)
	if settings.DualSubEnabled {
		dualSubStart := time.Now()
		outputs, firstErr := GenerateDualSubs(ctx, job, settings, configDir)
		if len(outputs) > 0 {
			_ = q.Update(id, func(j *Job) {
				j.DualSubOutputs = outputs
			})
			emitEvent(PipelineEvent{
				Type:      EventDualSubDone,
				JobID:     id,
				Title:     job.Source.Title,
				Year:      job.Source.Year,
				MediaType: job.Source.Type,
				Stage:     string(StageDualSub),
				Duration:  time.Since(dualSubStart).Seconds(),
				Details:   map[string]any{"count": len(outputs), "outputs": outputs},
				Message:   fmt.Sprintf("Dual subtitles generated: %d pair(s)", len(outputs)),
			})
		} else {
			errMsg := "no subtitle source available for configured pairs"
			if firstErr != nil {
				errMsg = firstErr.Error()
			}
			_ = q.Update(id, func(j *Job) { j.DualSubError = errMsg })
			emitEvent(PipelineEvent{
				Type:      EventDualSubFailed,
				JobID:     id,
				Title:     job.Source.Title,
				Year:      job.Source.Year,
				MediaType: job.Source.Type,
				Stage:     string(StageDualSub),
				Duration:  time.Since(dualSubStart).Seconds(),
				Message:   "Dual subtitles skipped: " + errMsg,
			})
			slog.Info("dual sub produced no outputs", "component", "pipeline", "job_id", id, "reason", errMsg)
		}
	} else {
		slog.Info("dual sub skipped (disabled in settings)", "component", "pipeline", "job_id", id)
	}

	// ── Stage 4: Process ─────────────────────────────────────────────────
	if job, _ = q.Get(id); job.State == StateCancelled {
		return
	}
	_ = q.Update(id, func(j *Job) {
		j.Stage = StageProcess
		j.Progress = 0.5
	})
	job, _ = q.Get(id)
	if err := maybeTranscode(ctx, q, job, configDir); err != nil {
		slog.Warn("transcoding failed, proceeding with original", "component", "pipeline", "job_id", id, "error", err)
		job, _ = q.Get(id)
		WriteTranscodeFailedNotification(job, configDir, err.Error())
	}
	job, _ = q.Get(id)

	// ── Stage 5: Catalog (late) ───────────────────────────────────────────
	// Trigger a second refresh if any sidecar was written (dual-sub ASS or
	// transcoded alternate) so the new file appears in Jellyfin's pickers.
	if job, _ = q.Get(id); job.State == StateCancelled {
		return
	}
	if settings.CatalogEnabled && (len(job.TranscodeOutputs) > 0 || len(job.DualSubOutputs) > 0) {
		_ = q.Update(id, func(j *Job) { j.Progress = 0.95 })
		job, _ = q.Get(id)
		CatalogLate(job, peliculaAPI)
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
// exists for the job's video codec/resolution. On success the sidecar path is
// appended to job.TranscodeOutputs; the original Source.Path is never modified.
// Returns a non-nil error only when transcoding was attempted and failed; the
// caller continues with the original file either way.
func maybeTranscode(ctx context.Context, q *Queue, job *Job, configDir string) error {
	if !GetSettings(appDB).TranscodingEnabled {
		return nil
	}

	if job.Validation == nil || job.Validation.Checks.Codecs == nil {
		return nil
	}
	codecs := job.Validation.Checks.Codecs

	profiles, err := LoadProfiles(configDir)
	if err != nil || len(profiles) == 0 {
		return nil
	}

	profile := FindMatchingProfile(profiles, codecs.Video, codecs.Height)
	if profile == nil {
		return nil // no matching profile → skip transcoding
	}

	// Passthrough: source already satisfies the profile's output requirements
	if ShouldPassthrough(codecs, profile) {
		slog.Info("transcode passthrough: source already matches profile target",
			"component", "pipeline", "job_id", job.ID,
			"codec", codecs.Video, "height", codecs.Height, "profile", profile.Name)
		_ = q.Update(job.ID, func(j *Job) {
			j.TranscodeProfile = profile.Name
			j.TranscodeDecision = "passthrough"
		})
		return nil
	}

	slog.Info("transcoding job", "component", "pipeline", "job_id", job.ID, "profile", profile.Name)
	_ = q.Update(job.ID, func(j *Job) {
		j.TranscodeProfile = profile.Name
	})

	emitEvent(PipelineEvent{
		Type:      EventTranscodeStarted,
		JobID:     job.ID,
		Title:     job.Source.Title,
		Year:      job.Source.Year,
		MediaType: job.Source.Type,
		Stage:     string(StageProcess),
		Details:   map[string]any{"profile": profile.Name},
		Message:   "Transcode started with profile " + profile.Name,
	})
	transcodeStart := time.Now()

	outputPath, err := Process(ctx, job, profile, func(pct, etaSecs float64) {
		// Map pct (0–1) into the 50–90% progress window
		progress := 0.5 + pct*0.4
		q.Update(job.ID, func(j *Job) { //nolint:errcheck
			j.Progress = progress
			j.TranscodeETA = etaSecs
		})
	})

	if err != nil {
		_ = q.Update(job.ID, func(j *Job) {
			j.TranscodeDecision = "failed"
			j.TranscodeError = err.Error()
		})
		emitEvent(PipelineEvent{
			Type:      EventTranscodeFailed,
			JobID:     job.ID,
			Title:     job.Source.Title,
			Year:      job.Source.Year,
			MediaType: job.Source.Type,
			Stage:     string(StageProcess),
			Duration:  time.Since(transcodeStart).Seconds(),
			Details:   map[string]any{"profile": profile.Name, "error": err.Error()},
			Message:   "Transcode failed: " + err.Error(),
		})
		return err
	}

	_ = q.Update(job.ID, func(j *Job) {
		j.TranscodeDecision = "transcoded"
		j.TranscodeOutputs = append(j.TranscodeOutputs, outputPath)
	})
	emitEvent(PipelineEvent{
		Type:      EventTranscodeDone,
		JobID:     job.ID,
		Title:     job.Source.Title,
		Year:      job.Source.Year,
		MediaType: job.Source.Type,
		Stage:     string(StageProcess),
		Duration:  time.Since(transcodeStart).Seconds(),
		Details:   map[string]any{"profile": profile.Name, "output": outputPath},
		Message:   "Transcode completed with profile " + profile.Name,
	})
	return nil
}

// runManualTranscode handles jobs with ManualProfile set. It skips validation
// and the early catalog (the file is already in the library), transcodes using
// the named profile, then triggers a late Jellyfin refresh so the sidecar
// appears as an alternate version. On failure the job is marked StateFailed.
func runManualTranscode(ctx context.Context, q *Queue, id, configDir, peliculaAPI string) {
	_ = q.Update(id, func(j *Job) {
		j.State = StateProcessing
		j.Stage = StageProcess
		j.Progress = 0.1
	})
	job, _ := q.Get(id)

	slog.Info("manual transcode job", "component", "pipeline", "job_id", id, "profile", job.ManualProfile, "title", job.Source.Title)

	profiles, err := LoadProfiles(configDir)
	if err != nil {
		slog.Error("failed to load profiles", "component", "pipeline", "job_id", id, "error", err)
		_ = q.Update(id, func(j *Job) {
			j.State = StateFailed
			j.Error = "could not load profiles: " + err.Error()
		})
		return
	}

	profile := FindProfileByName(profiles, job.ManualProfile)
	if profile == nil {
		slog.Warn("profile not found for manual transcode", "component", "pipeline", "job_id", id, "profile", job.ManualProfile)
		_ = q.Update(id, func(j *Job) {
			j.State = StateFailed
			j.Error = "profile not found: " + job.ManualProfile
		})
		return
	}

	_ = q.Update(id, func(j *Job) {
		j.TranscodeProfile = profile.Name
		j.Progress = 0.2
	})

	emitEvent(PipelineEvent{
		Type:      EventTranscodeStarted,
		JobID:     id,
		Title:     job.Source.Title,
		Year:      job.Source.Year,
		MediaType: job.Source.Type,
		Stage:     string(StageProcess),
		Details:   map[string]any{"profile": profile.Name, "manual": true},
		Message:   "Manual transcode started with profile " + profile.Name,
	})
	transcodeStart := time.Now()

	outputPath, err := Process(ctx, job, profile, func(pct, etaSecs float64) {
		progress := 0.2 + pct*0.7
		q.Update(id, func(j *Job) { //nolint:errcheck
			j.Progress = progress
			j.TranscodeETA = etaSecs
		})
	})

	if err != nil {
		slog.Warn("manual transcode failed", "component", "pipeline", "job_id", id, "error", err)
		_ = q.Update(id, func(j *Job) {
			j.State = StateFailed
			j.Stage = StageProcess
			j.TranscodeDecision = "failed"
			j.TranscodeError = err.Error()
		})
		emitEvent(PipelineEvent{
			Type:      EventTranscodeFailed,
			JobID:     id,
			Title:     job.Source.Title,
			Year:      job.Source.Year,
			MediaType: job.Source.Type,
			Stage:     string(StageProcess),
			Duration:  time.Since(transcodeStart).Seconds(),
			Details:   map[string]any{"profile": profile.Name, "manual": true, "error": err.Error()},
			Message:   "Manual transcode failed: " + err.Error(),
		})
		job, _ = q.Get(id)
		WriteTranscodeFailedNotification(job, configDir, err.Error())
		return
	}

	_ = q.Update(id, func(j *Job) {
		j.TranscodeDecision = "transcoded"
		j.TranscodeOutputs = append(j.TranscodeOutputs, outputPath)
		j.Progress = 0.95
	})
	emitEvent(PipelineEvent{
		Type:      EventTranscodeDone,
		JobID:     id,
		Title:     job.Source.Title,
		Year:      job.Source.Year,
		MediaType: job.Source.Type,
		Stage:     string(StageProcess),
		Duration:  time.Since(transcodeStart).Seconds(),
		Details:   map[string]any{"profile": profile.Name, "manual": true, "output": outputPath},
		Message:   "Manual transcode completed with profile " + profile.Name,
	})

	// Refresh Jellyfin so the sidecar appears as an alternate version
	settings := GetSettings(appDB)
	if settings.CatalogEnabled {
		job, _ = q.Get(id)
		CatalogLate(job, peliculaAPI)
	}

	_ = q.Update(id, func(j *Job) {
		j.State = StateCompleted
		j.Stage = StageDone
		j.Progress = 1.0
	})
	slog.Info("manual transcode completed", "component", "pipeline", "job_id", id, "title", job.Source.Title, "output", outputPath)
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
		emitEvent(PipelineEvent{
			Type:      EventReleaseBlocklisted,
			JobID:     job.ID,
			Title:     job.Source.Title,
			Year:      job.Source.Year,
			MediaType: job.Source.Type,
			Details:   map[string]any{"category": category, "hash": hash},
			Message:   "Release blocklisted: " + reason,
		})
		return
	}
	slog.Error("failed to blocklist after 3 attempts", "component", "pipeline", "error", lastErr)
}
