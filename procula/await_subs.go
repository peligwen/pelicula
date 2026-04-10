package main

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// awaitPollInterval controls how often disk is polled for sidecar files.
// Overridden in tests to avoid 30-second waits.
var awaitPollInterval = 30 * time.Second

// nowFn is the clock function used for deadline checks. Overridable in tests.
var nowFn = time.Now

// awaitSubtitles is the StageAwaitSubs pipeline stage.
// It asks Bazarr to search for missing subtitles, then polls disk every 30s
// until all sidecar files appear (or timeout/cancellation).
// Always returns — never blocks the pipeline indefinitely.
func awaitSubtitles(ctx context.Context, q *Queue, job *Job, settings PipelineSettings, configDir string) {
	if len(job.MissingSubs) == 0 {
		return
	}

	// Kick Bazarr to search immediately (best-effort — errors logged internally)
	bazarrSearchSubtitles(ctx, configDir, job)

	timeout := time.Duration(settings.SubAcquireTimeout) * time.Minute
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	// deadline uses real time.Now() so it is a fixed wall-clock point.
	// The check (nowFn().After(deadline)) uses the overridable clock so tests
	// can advance it without waiting real time.
	deadline := time.Now().Add(timeout)

	// remaining tracks langs not yet acquired
	remaining := make([]string, len(job.MissingSubs))
	copy(remaining, job.MissingSubs)

	ticker := time.NewTicker(awaitPollInterval)
	defer ticker.Stop()

	slog.Info("awaiting subtitles", "component", "await_subs", "job_id", job.ID, "langs", remaining, "timeout_min", settings.SubAcquireTimeout)

	for {
		// Check for newly arrived sidecars
		var stillMissing []string
		for _, lang := range remaining {
			if path := findSubSidecar(job.Source.Path, lang); path != "" {
				slog.Info("subtitle sidecar acquired", "component", "await_subs", "job_id", job.ID, "lang", lang, "path", path)
				_ = q.Update(job.ID, func(j *Job) {
					// Remove from MissingSubs, add to SubsAcquired
					j.MissingSubs = removeString(j.MissingSubs, lang)
					j.SubsAcquired = append(j.SubsAcquired, lang)
				})
				emitEvent(PipelineEvent{
					Type:      EventSubAcquired,
					JobID:     job.ID,
					Title:     job.Source.Title,
					Year:      job.Source.Year,
					MediaType: job.Source.Type,
					Stage:     string(StageAwaitSubs),
					Details:   map[string]any{"lang": lang, "path": path},
					Message:   "Subtitle acquired: " + lang,
				})
			} else {
				stillMissing = append(stillMissing, lang)
			}
		}
		remaining = stillMissing

		if len(remaining) == 0 {
			slog.Info("all subtitles acquired", "component", "await_subs", "job_id", job.ID)
			return
		}

		// Check exit conditions before sleeping
		if nowFn().After(deadline) {
			slog.Info("subtitle acquire timeout", "component", "await_subs", "job_id", job.ID, "still_missing", remaining)
			emitEvent(PipelineEvent{
				Type:      EventSubTimeout,
				JobID:     job.ID,
				Title:     job.Source.Title,
				Year:      job.Source.Year,
				MediaType: job.Source.Type,
				Stage:     string(StageAwaitSubs),
				Details:   map[string]any{"still_missing": remaining},
				Message:   "Subtitle acquire timeout — proceeding without: " + strings.Join(remaining, ", "),
			})
			return
		}

		select {
		case <-ticker.C:
			// poll again
		case <-ctx.Done():
			slog.Info("await_subs cancelled", "component", "await_subs", "job_id", job.ID)
			return
		}
	}
}

// removeString returns a new slice with the first occurrence of s removed.
func removeString(slice []string, s string) []string {
	out := make([]string, 0, len(slice))
	removed := false
	for _, v := range slice {
		if v == s && !removed {
			removed = true
			continue
		}
		out = append(out, v)
	}
	return out
}
