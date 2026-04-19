// Package supervisor launches the background goroutines that keep the
// pelicula-api service running: SSE poller, catalog queue poller,
// autowirer, missing-content watcher, and VPN watchdog.
package supervisor

import (
	"context"
	"log/slog"
	"time"

	pelapp "pelicula-api/internal/app/app"
	"pelicula-api/internal/app/catalog"
	"pelicula-api/internal/app/missingwatcher"
)

// Run launches all background goroutines. It returns immediately; goroutines
// are tied to ctx and exit when it is cancelled.
func Run(ctx context.Context, a *pelapp.App) {
	go a.SSEPoller.Run(ctx)
	go catalog.RunQueuePoller(ctx, a.CatalogDB, a.Svc, a.URLs.Radarr, a.URLs.Sonarr)

	go func() {
		if err := a.Autowirer.Run(ctx); err != nil {
			slog.Error("autowire failed", "component", "supervisor", "error", err)
		}
	}()

	go missingwatcher.New(a.Svc, a.URLs.Sonarr, a.URLs.Radarr).Run(2 * time.Minute)

	if a.Watchdog != nil {
		go a.Watchdog.Run()
	}
}
