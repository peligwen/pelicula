// Package pipeline provides the job worker loop for the procula processing pipeline.
// The Worker reads jobs from the Queue, dispatches pipeline jobs through the stage
// machine (validate → catalog → await_subs → dualsub → process → done), and routes
// action-bus jobs through the actions.Registry.
package pipeline

import (
	"context"
	"log/slog"
	"time"

	"procula/internal/actions"
	"procula/internal/queue"
)

// Worker processes jobs from the Queue sequentially.
type Worker struct {
	q   *queue.Queue
	reg *actions.Registry
}

// New creates a Worker with the given queue and action registry.
func New(q *queue.Queue, reg *actions.Registry) *Worker {
	return &Worker{q: q, reg: reg}
}

// Run processes jobs until ctx is cancelled. It should be called in a goroutine.
// A background ticker re-checks deferred (backoff) jobs once per minute.
func (w *Worker) Run(ctx context.Context) {
	slog.Info("worker started", "component", "pipeline")

	// Periodic wake: re-check for deferred jobs once per minute.
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.q.SignalPending()
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			slog.Info("worker stopping", "component", "pipeline")
			return
		case <-w.q.Pending():
			w.drainQueued(ctx)
		}
	}
}

// drainQueued processes all immediately-eligible queued jobs.
func (w *Worker) drainQueued(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		ids := w.q.NextQueued()
		if len(ids) == 0 {
			return
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			func(id string) {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("panic in job — worker continuing",
							"component", "pipeline", "job_id", id, "panic", r)
					}
				}()
				w.dispatchJob(ctx, id)
			}(id)
		}
	}
}

// dispatchJob routes a single job: action-bus jobs go to the registry;
// pipeline jobs go to the stage machine (not yet implemented in this package —
// the root procula package owns the stage machine until it is migrated here).
func (w *Worker) dispatchJob(ctx context.Context, id string) {
	job, ok := w.q.Get(id)
	if !ok {
		slog.Warn("job not found, skipping", "component", "pipeline", "job_id", id)
		return
	}

	if job.State == queue.StateCancelled {
		return
	}

	if job.ActionType != "" && job.ActionType != "pipeline" {
		jobCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		w.q.RegisterCancel(id, cancel)
		defer w.q.UnregisterCancel(id)
		w.runActionJob(jobCtx, job)
	}
	// Pipeline-stage jobs are dispatched by the root procula package's RunWorker
	// until the stage machine is migrated into this package.
}

// runActionJob dispatches a non-pipeline job to a registered action handler,
// writes the result back to the job row, and marks the job completed/failed.
// Transient failures are re-queued with exponential backoff.
func (w *Worker) runActionJob(ctx context.Context, job *queue.Job) {
	const maxTransientRetries = 5

	def := w.reg.Lookup(job.ActionType)
	if def == nil {
		w.q.Update(job.ID, func(j *queue.Job) { //nolint:errcheck
			j.State = queue.StateFailed
			j.Error = "unknown action: " + j.ActionType
		})
		return
	}

	w.q.Update(job.ID, func(j *queue.Job) { //nolint:errcheck
		j.State = queue.StateProcessing
	})

	result, err := def.Handler(ctx, w.q, job)

	w.q.Update(job.ID, func(j *queue.Job) { //nolint:errcheck
		if j.State == queue.StateCancelled || ctx.Err() != nil {
			j.State = queue.StateCancelled
			j.NextAttemptAt = nil
			return
		}
		if err != nil {
			// Treat all errors as transient up to maxTransientRetries.
			j.RetryCount++
			if j.RetryCount > maxTransientRetries {
				j.State = queue.StateFailed
				j.Error = "max retries exceeded: " + err.Error()
				j.NextAttemptAt = nil
				return
			}
			next := time.Now().UTC().Add(queue.BackoffDuration(j.RetryCount))
			j.State = queue.StateQueued
			j.NextAttemptAt = &next
			slog.Info("transient failure, re-queuing with backoff",
				"component", "pipeline", "job_id", j.ID,
				"action", j.ActionType, "retry_count", j.RetryCount,
				"next_attempt_at", next.Format(time.RFC3339),
				"error", err.Error())
			return
		}
		j.State = queue.StateCompleted
		j.Progress = 1.0
		j.Result = result
		j.NextAttemptAt = nil
	})

	// Signal if re-queued (backoff case).
	if fresh, ok := w.q.Get(job.ID); ok && fresh.State == queue.StateQueued {
		w.q.SignalPending()
	}
}
