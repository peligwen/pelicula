// Package supervisor launches the background goroutines that keep the
// pelicula-api service running: SSE poller, catalog queue poller,
// autowirer, missing-content watcher, and VPN watchdog.
package supervisor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	pelapp "pelicula-api/internal/app/app"
	"pelicula-api/internal/app/catalog"
	"pelicula-api/internal/app/missingwatcher"
)

// autowireInitialBackoff and autowireMaxBackoff configure the capped
// exponential backoff between failed/incomplete Autowirer.Run attempts (MWA-2).
// Exposed as vars (not consts) so tests can shrink them instead of waiting
// out real timers.
var (
	autowireInitialBackoff = 30 * time.Second
	autowireMaxBackoff     = 5 * time.Minute
)

// Run launches all background goroutines. It returns immediately; goroutines
// are tied to ctx and exit when it is cancelled. The returned WaitGroup drains
// to zero once every goroutine has exited — callers should Wait after ctx
// cancel to avoid racing ongoing writes at shutdown.
func Run(ctx context.Context, a *pelapp.App) *sync.WaitGroup {
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		a.SSEPoller.Run(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		catalog.RunQueuePoller(ctx, a.CatalogDB, a.Svc)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		catalog.RunReconcileLoop(ctx, a.CatalogDB, a.Svc, a.Svc)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		runAutowireWithRetry(ctx, a)
	}()

	mw := missingwatcher.New(a.Svc, a.URLs.Sonarr, a.URLs.Radarr)
	mw.CatalogCache = a.ArrCatalogCache
	wg.Add(1)
	go func() {
		defer wg.Done()
		mw.Run(ctx, 10*time.Minute)
	}()

	if a.Watchdog != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.Watchdog.Run(ctx)
		}()
	}

	return &wg
}

// runAutowireWithRetry runs a.Autowirer.Run repeatedly with capped exponential
// backoff (autowireInitialBackoff → autowireMaxBackoff) until it either fully
// succeeds (a.Svc.IsWired() reports true) or ctx is cancelled (MWA-2).
//
// Without this loop, any single transient failure among the wiring steps
// gated behind VPN configuration (download clients, Prowlarr↔*arr apps)
// permanently left a.Svc.IsWired() false for the life of the process, which
// in turn blocks missingwatcher.Watcher.Run forever (it waits on IsWired()
// before it starts scanning). Every wiring step in autowire.Run has its own
// existence check before creating anything (see autowire.go: wireDownloadClient,
// wireRootFolder, wireReleaseProfile, wireImportWebhook, wireProwlarrApp all
// list-then-create-or-update; wireBazarr's SaveSettings replaces settings
// wholesale rather than appending, and is additionally gated by
// bazarrAlreadyWired), so re-running the whole sequence here is safe — a
// retry after partial success just re-verifies the already-wired steps and
// completes whatever failed the first time.
func runAutowireWithRetry(ctx context.Context, a *pelapp.App) {
	backoff := autowireInitialBackoff
	for attempt := 1; ; attempt++ {
		slog.Debug("autowire attempt starting", "component", "supervisor", "attempt", attempt)
		err := a.Autowirer.Run(ctx)
		if ctx.Err() != nil {
			// Shutting down — stop retrying regardless of the outcome above.
			return
		}
		if err == nil && a.Svc.IsWired() {
			if attempt > 1 {
				slog.Info("autowire succeeded after retry", "component", "supervisor", "attempt", attempt)
			}
			return
		}

		if err != nil {
			slog.Error("autowire attempt failed, will retry", "component", "supervisor",
				"attempt", attempt, "backoff", backoff, "error", err)
		} else {
			slog.Warn("autowire completed with partial failures, will retry", "component", "supervisor",
				"attempt", attempt, "backoff", backoff)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > autowireMaxBackoff {
			backoff = autowireMaxBackoff
		}
	}
}
