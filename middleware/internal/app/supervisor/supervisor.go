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
		if err := a.Autowirer.Run(ctx); err != nil {
			slog.Error("autowire failed", "component", "supervisor", "error", err)
		}
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
