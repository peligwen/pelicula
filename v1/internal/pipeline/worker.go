// Package pipeline validates imported media files and reacts to the outcome.
//
// The *arr webhook enqueues a job per imported file. The worker claims jobs
// one at a time, runs ffprobe checks, and then either marks the matching
// requests available and refreshes Jellyfin (pass) or marks the release failed
// in *arr, deletes the bad file through the *arr API and searches again (fail).
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"pelicula/internal/store"
)

// ArrClient is the slice of the Sonarr/Radarr client the pipeline uses.
// *arr.Client satisfies it.
type ArrClient interface {
	TriggerCommand(ctx context.Context, payload map[string]any) error
	GetMovie(ctx context.Context, id int) (map[string]any, error)
	GetEpisodeFiles(ctx context.Context, seriesID int) ([]map[string]any, error)
	GetHistory(ctx context.Context, query string) ([]map[string]any, error)
	MarkHistoryFailed(ctx context.Context, historyID int) error
	DeleteMovieFile(ctx context.Context, id int) error
	DeleteEpisodeFile(ctx context.Context, id int) error
}

// LibraryRefresher asks Jellyfin to rescan its libraries.
type LibraryRefresher interface {
	RefreshLibrary(ctx context.Context, token string) error
}

// TokenSource yields a Jellyfin admin token. *jellyfin.Admin satisfies it.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Deps are the worker's collaborators. Any nil client is skipped with a log
// line; Store is required.
type Deps struct {
	Store    *store.Store
	Sonarr   ArrClient
	Radarr   ArrClient
	Jellyfin LibraryRefresher
	JFAdmin  TokenSource
	FFprobe  string // binary, default "ffprobe"
	Log      *slog.Logger
}

const (
	defaultIdleWait        = 2 * time.Second
	defaultRefreshDebounce = 15 * time.Second
	refreshTimeout         = 30 * time.Second
)

// Worker processes the job queue, one job at a time.
type Worker struct {
	// IdleWait is how long Run sleeps when the queue is empty before polling
	// again; Kick cuts it short. Default 2s.
	IdleWait time.Duration
	// RefreshDebounce is the quiet period after a passing job before the
	// Jellyfin library refresh fires; every pass inside it resets the timer,
	// so a burst of imports causes one refresh. Default 15s.
	RefreshDebounce time.Duration

	d    Deps
	log  *slog.Logger
	kick chan struct{}

	// validate is a seam for tests; it is Validate in production.
	validate func(ctx context.Context, ffprobe, path string, size int64, runtimeMin int) Result

	mu         sync.Mutex
	refresh    *time.Timer
	refreshCtx context.Context
}

// New builds a worker. It does not start it; call Run.
func New(d Deps) *Worker {
	if d.FFprobe == "" {
		d.FFprobe = "ffprobe"
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Worker{
		IdleWait:        defaultIdleWait,
		RefreshDebounce: defaultRefreshDebounce,
		d:               d,
		log:             d.Log.With("component", "pipeline"),
		kick:            make(chan struct{}, 1),
		validate:        Validate,
	}
}

// Kick wakes an idle worker; call it after enqueueing a job. It never blocks.
func (w *Worker) Kick() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// Run processes jobs until ctx is done. Callers should run
// Store.ResetRunningJobs first so jobs interrupted by a restart are retried.
func (w *Worker) Run(ctx context.Context) {
	if w.d.Store == nil {
		w.log.Error("pipeline worker has no store, not running")
		return
	}
	defer w.stopRefresh()
	w.log.Info("pipeline worker started")
	for ctx.Err() == nil {
		job, err := w.d.Store.ClaimNextJob(ctx)
		if err != nil {
			if ctx.Err() == nil {
				w.log.Error("claim job failed", "error", err)
			}
		} else if job != nil {
			w.process(ctx, job)
			continue
		}
		w.wait(ctx)
	}
	w.log.Info("pipeline worker stopped")
}

// wait sleeps until the idle interval passes, a kick arrives or ctx is done.
func (w *Worker) wait(ctx context.Context) {
	t := time.NewTimer(w.IdleWait)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-w.kick:
	case <-t.C:
	}
}

// process runs one claimed job to completion. A panic anywhere in it marks
// the job failed (unless it had already finished) and is not propagated.
func (w *Worker) process(ctx context.Context, job *store.Job) {
	finished := false
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		w.log.Error("job panicked", "job", job.ID, "panic", r, "stack", string(debug.Stack()))
		if finished {
			return
		}
		msg := fmt.Sprintf("internal error: %v", r)
		out, _ := json.Marshal(Result{Integrity: "skip", Sample: "skip", Duration: "skip", Reason: msg})
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := w.d.Store.FinishJob(fctx, job.ID, store.JobFailed, string(out), msg); err != nil {
			w.log.Error("mark panicked job failed", "job", job.ID, "error", err)
		}
	}()

	log := w.log.With("job", job.ID, "arr", job.ArrType, "title", job.Title)
	log.Info("job started", "path", job.Path, "attempt", job.Attempts)

	var res Result
	if !w.d.Store.BoolSetting(ctx, store.SettingValidationEnabled) {
		res = Result{Passed: true, Skipped: true, Integrity: "skip", Sample: "skip", Duration: "skip"}
	} else {
		res = w.validate(ctx, w.d.FFprobe, job.Path, job.Size, job.RuntimeMin)
	}
	if ctx.Err() != nil {
		// Shutting down mid-probe: leave the job running so the next start
		// requeues it. Never blocklist a release because we were interrupted.
		log.Info("job interrupted by shutdown")
		return
	}

	out, err := json.Marshal(res)
	if err != nil {
		out = []byte("{}")
	}

	if res.Passed {
		if err := w.d.Store.FinishJob(ctx, job.ID, store.JobPassed, string(out), ""); err != nil {
			log.Error("finish job failed", "error", err)
		}
		finished = true
		w.step("mark requests available", job, func() error {
			n, err := w.d.Store.MarkRequestsAvailable(ctx, job.ArrType, job.ArrID)
			if n > 0 {
				log.Info("requests marked available", "count", n)
			}
			return err
		})
		w.scheduleRefresh(ctx)
		log.Info("job passed", "skipped", res.Skipped, "duration", res.Duration, "sample", res.Sample)
		return
	}

	if err := w.d.Store.FinishJob(ctx, job.ID, store.JobFailed, string(out), res.Reason); err != nil {
		log.Error("finish job failed", "error", err)
	}
	finished = true
	log.Warn("job failed validation", "reason", res.Reason)
	if w.d.Store.BoolSetting(ctx, store.SettingAutoBlocklist) {
		w.handleFailure(ctx, job)
	} else {
		log.Info("auto blocklist disabled, leaving release and file alone")
	}
}

// step runs one best-effort sub-step: an error or a panic is logged and the
// caller carries on with the next step.
func (w *Worker) step(name string, job *store.Job, fn func() error) {
	defer func() {
		if r := recover(); r != nil {
			w.log.Error("step panicked", "step", name, "job", job.ID, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if err := fn(); err != nil {
		w.log.Warn("step failed", "step", name, "job", job.ID, "error", err)
	}
}

// scheduleRefresh arms the single debounce timer: the first pass starts it,
// each later pass inside the window pushes it back.
func (w *Worker) scheduleRefresh(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refreshCtx = ctx
	if w.refresh == nil {
		w.refresh = time.AfterFunc(w.RefreshDebounce, w.doRefresh)
		return
	}
	w.refresh.Reset(w.RefreshDebounce)
}

func (w *Worker) stopRefresh() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.refresh != nil {
		w.refresh.Stop()
	}
}

// doRefresh runs on the timer goroutine, so it must not panic.
func (w *Worker) doRefresh() {
	defer func() {
		if r := recover(); r != nil {
			w.log.Error("jellyfin refresh panicked", "panic", r)
		}
	}()
	w.mu.Lock()
	parent := w.refreshCtx
	w.mu.Unlock()
	if parent == nil || parent.Err() != nil {
		return
	}
	if w.d.Jellyfin == nil || w.d.JFAdmin == nil {
		w.log.Info("jellyfin not configured, skipping library refresh")
		return
	}
	ctx, cancel := context.WithTimeout(parent, refreshTimeout)
	defer cancel()
	token, err := w.d.JFAdmin.Token(ctx)
	if err != nil {
		w.log.Warn("jellyfin refresh: admin token failed", "error", err)
		return
	}
	if err := w.d.Jellyfin.RefreshLibrary(ctx, token); err != nil {
		w.log.Warn("jellyfin refresh failed", "error", err)
		return
	}
	w.log.Info("jellyfin library refresh requested")
}
