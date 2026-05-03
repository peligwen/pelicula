package catalog

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"pelicula-api/internal/app/util"
)

const reconcileInterval = 15 * time.Minute

// RunReconcileLoop runs ReconcileOrphans on a periodic ticker (every 15 minutes,
// ±10% jitter). It mirrors the shape of RunQueuePoller. The first run fires
// immediately at startup after the initial delay to spread startup load.
// Goroutine exits when ctx is cancelled.
func RunReconcileLoop(ctx context.Context, db *sql.DB, jf JellyfinMetaClient, radarr ArrClient) {
	tick := util.JitteredTicker(ctx, reconcileInterval, 0.1)

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			result, err := ReconcileOrphans(ctx, db, jf, radarr)
			if err != nil {
				slog.Error("reconcile loop: run failed",
					"component", "catalog_reconcile", "error", err)
				continue
			}
			if result.Added > 0 {
				slog.Info("reconcile loop: orphans recovered",
					"component", "catalog_reconcile",
					"added", result.Added,
					"scanned", result.Scanned,
				)
			}
		}
	}
}
