package catalog

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"pelicula-api/internal/app/util"
)

const reconcileInterval = 15 * time.Minute

// RunReconcileLoop runs ReconcileOrphans, then SweepStale, on a periodic
// ticker (every 15 minutes, ±10% jitter). It mirrors the shape of
// RunQueuePoller. The first run fires immediately at startup after the
// initial delay to spread startup load. Adoption before sweep: rows the
// reconciler just inserted are alive in Jellyfin by construction, so the
// sweep never undoes fresh adoptions. Goroutine exits when ctx is cancelled.
func RunReconcileLoop(ctx context.Context, db *sql.DB, jf JellyfinMetaClient, arr ArrClient) {
	tick := util.JitteredTicker(ctx, reconcileInterval, 0.1)

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			result, err := ReconcileOrphans(ctx, db, jf, arr)
			if err != nil {
				slog.Error("reconcile loop: run failed",
					"component", "catalog_reconcile", "error", err)
			} else if result.Added > 0 {
				slog.Info("reconcile loop: orphans recovered",
					"component", "catalog_reconcile",
					"added", result.Added,
					"scanned", result.Scanned,
				)
			}
			// Sweep even when reconcile failed — its sub-sweeps are
			// independently guarded and skip whatever they can't verify.
			if _, err := SweepStale(ctx, db, jf, arr); err != nil {
				slog.Error("reconcile loop: sweep failed",
					"component", "catalog_sweep", "error", err)
			}
		}
	}
}
