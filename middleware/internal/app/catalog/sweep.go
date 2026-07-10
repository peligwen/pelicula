package catalog

// sweep.go — stale-row sweep for catalog.db (audit finding MWD-7).
//
// catalog_items is a derived cache of *arr (identity, tier) and Jellyfin
// (artwork, synopsis) plus the reconcile ledger — never an independent source
// of truth. Rows are written by download hooks, backfill and the reconciler,
// but before this sweep nothing ever removed one, so media deleted from
// Radarr/Sonarr/Jellyfin accumulated as permanent orphans.
//
// Liveness rules, by source:
//   - source != 'reconcile' (hook/backfill rows): alive while the owning *arr
//     app still lists the item, matched on the same natural keys Upsert uses
//     (movie: tmdb_id/arr_id; series: tvdb_id/tmdb_id/arr_id).
//   - source == 'reconcile' (movies adopted from Jellyfin): alive while the
//     file path is still present in a complete Jellyfin library scan.
//
// Safety valves — a sub-sweep is skipped, keeping its rows, when:
//   - its source-of-truth service is unconfigured or the fetch fails;
//   - the fetch returns zero items while catalog rows exist (a glitchy empty
//     response is far more likely than a deliberately emptied library; the
//     cost of guessing wrong here is only stale rows, which the next sweep
//     after a manual backfill can still clear);
//   - the Jellyfin scan hit its cap (absence proves nothing);
//   - a row carries none of the natural keys its liveness test needs.
//
// Rows touched within sweepGraceWindow are never deleted, and the updated_at
// re-check runs inside the DELETE statement itself (Store.DeleteStale), so a
// concurrent hook upsert always wins over the sweep.
//
// Seasons and episodes are never tested directly: they live and die with
// their parent chain (Store.DeleteOrphanedChildren). A deleted-then-readded
// item simply gets a fresh row from the next hook/backfill/reconcile pass,
// and Jellyfin metadata re-syncs on first view.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// sweepGraceWindow spares recently written rows from the current sweep pass.
// Anything this young is either already covered by the just-fetched live sets
// or will be re-checked next pass; sparing it costs one 15-minute cycle.
const sweepGraceWindow = time.Minute

// SweepResult summarises one sweep pass. Counters are rows actually deleted
// (the updated_at guard may spare rows the liveness test doomed).
type SweepResult struct {
	MoviesDeleted    int64 `json:"movies_deleted"`
	SeriesDeleted    int64 `json:"series_deleted"`
	ReconcileDeleted int64 `json:"reconcile_deleted"`
	ChildrenDeleted  int64 `json:"children_deleted"`
}

func (r SweepResult) total() int64 {
	return r.MoviesDeleted + r.SeriesDeleted + r.ReconcileDeleted + r.ChildrenDeleted
}

// SweepStale removes catalog rows whose backing media no longer exists,
// per the liveness rules in the file header. Fetch failures degrade to
// skipped sub-sweeps (logged, not fatal); only catalog DB errors are
// returned. Idempotent — a second pass right after a first deletes nothing.
func SweepStale(ctx context.Context, db *sql.DB, jf JellyfinMetaClient, svc ArrClient) (SweepResult, error) {
	var result SweepResult
	cutoff := time.Now().UTC().Add(-sweepGraceWindow).Format(time.RFC3339)

	sonarrKey, radarrKey, _ := svc.Keys()

	// Fetch every live set first, then load roots, so rows written during
	// the fetches are at worst spared by the grace window, never doomed by
	// a stale snapshot.
	var (
		radarrTmdb, radarrArrIDs map[int]bool
		radarrOK                 bool

		sonarrTvdb, sonarrTmdb, sonarrArrIDs map[int]bool
		sonarrOK                             bool

		jfPaths map[string]bool
		jfOK    bool
	)

	if radarrKey != "" {
		radarrTmdb, radarrArrIDs, radarrOK = radarrLiveSets(ctx, svc)
	}
	if sonarrKey != "" {
		sonarrTvdb, sonarrTmdb, sonarrArrIDs, sonarrOK = sonarrLiveSets(ctx, svc)
	}
	if jf == nil {
		slog.Warn("sweep: no jellyfin client, skipping reconcile-row sweep",
			"component", "catalog_sweep")
	} else if jfItems, complete, err := scanJellyfinMovies(ctx, jf); err != nil {
		slog.Warn("sweep: jellyfin scan failed, skipping reconcile-row sweep",
			"component", "catalog_sweep", "error", err)
	} else if !complete {
		slog.Warn("sweep: jellyfin scan hit cap, skipping reconcile-row sweep",
			"component", "catalog_sweep", "scanned", len(jfItems),
			"hint", "raise RECONCILE_LIBRARY_LIMIT")
	} else {
		jfPaths = make(map[string]bool, len(jfItems))
		for _, it := range jfItems {
			if it.Path != "" {
				jfPaths[it.Path] = true
			}
		}
		jfOK = true
	}

	roots, err := storeFor(db).ListRoots(ctx)
	if err != nil {
		return result, fmt.Errorf("sweep: list roots: %w", err)
	}

	// Empty-set guards: refuse to treat "the source listed nothing" as "the
	// user deleted everything" while rows exist to delete.
	if radarrOK && len(radarrTmdb) == 0 && len(radarrArrIDs) == 0 && hasRoot(roots, "movie", false) {
		slog.Warn("sweep: radarr returned no movies while catalog has movie rows — skipping movie sweep",
			"component", "catalog_sweep")
		radarrOK = false
	}
	if sonarrOK && len(sonarrTvdb) == 0 && len(sonarrTmdb) == 0 && len(sonarrArrIDs) == 0 && hasRoot(roots, "series", false) {
		slog.Warn("sweep: sonarr returned no series while catalog has series rows — skipping series sweep",
			"component", "catalog_sweep")
		sonarrOK = false
	}
	if jfOK && len(jfPaths) == 0 && hasRoot(roots, "movie", true) {
		slog.Warn("sweep: jellyfin returned no movies while catalog has reconcile rows — skipping reconcile-row sweep",
			"component", "catalog_sweep")
		jfOK = false
	}

	var doomedMovies, doomedSeries, doomedReconcile []string
	for _, it := range roots {
		switch {
		case it.Source == "reconcile":
			// Only movie reconcile rows exist today (TV reconcile is
			// deferred); anything else stays untouched.
			if jfOK && it.Type == "movie" && it.FilePath != "" && !jfPaths[it.FilePath] {
				doomedReconcile = append(doomedReconcile, it.ID)
			}
		case it.Type == "movie":
			if !radarrOK || (it.TmdbID == 0 && it.ArrID == 0) {
				continue // untestable — keep
			}
			if !radarrTmdb[it.TmdbID] && !radarrArrIDs[it.ArrID] {
				doomedMovies = append(doomedMovies, it.ID)
			}
		case it.Type == "series":
			if !sonarrOK || (it.TvdbID == 0 && it.TmdbID == 0 && it.ArrID == 0) {
				continue // untestable — keep
			}
			if !sonarrTvdb[it.TvdbID] && !sonarrTmdb[it.TmdbID] && !sonarrArrIDs[it.ArrID] {
				doomedSeries = append(doomedSeries, it.ID)
			}
		}
	}

	if result.MoviesDeleted, err = storeFor(db).DeleteStale(ctx, doomedMovies, cutoff); err != nil {
		return result, fmt.Errorf("sweep movies: %w", err)
	}
	if result.SeriesDeleted, err = storeFor(db).DeleteStale(ctx, doomedSeries, cutoff); err != nil {
		return result, fmt.Errorf("sweep series: %w", err)
	}
	if result.ReconcileDeleted, err = storeFor(db).DeleteStale(ctx, doomedReconcile, cutoff); err != nil {
		return result, fmt.Errorf("sweep reconcile rows: %w", err)
	}
	if result.ChildrenDeleted, err = storeFor(db).DeleteOrphanedChildren(ctx); err != nil {
		return result, fmt.Errorf("sweep orphaned children: %w", err)
	}

	if result.total() > 0 {
		slog.Info("catalog sweep removed stale rows",
			"component", "catalog_sweep",
			"movies", result.MoviesDeleted,
			"series", result.SeriesDeleted,
			"reconcile", result.ReconcileDeleted,
			"children", result.ChildrenDeleted,
		)
	}
	return result, nil
}

// hasRoot reports whether roots contains a row of the given type, filtered to
// reconcile-sourced rows when reconcileOnly is set (and non-reconcile rows
// otherwise) — mirroring how the sweep partitions liveness ownership.
func hasRoot(roots []CatalogItem, typ string, reconcileOnly bool) bool {
	for _, it := range roots {
		if it.Type != typ {
			continue
		}
		if reconcileOnly == (it.Source == "reconcile") {
			return true
		}
	}
	return false
}

// radarrLiveSets fetches every movie Radarr currently tracks (regardless of
// hasFile — queued items legitimately hold catalog rows) and returns its
// identity sets. ok=false means the fetch failed and absence proves nothing.
func radarrLiveSets(ctx context.Context, svc ArrClient) (tmdb, arrIDs map[int]bool, ok bool) {
	movies, err := svc.RadarrClient().GetMovies(ctx, "/api/v3")
	if err != nil {
		slog.Warn("sweep: radarr fetch failed, skipping movie sweep",
			"component", "catalog_sweep", "error", err)
		return nil, nil, false
	}
	tmdb = make(map[int]bool, len(movies))
	arrIDs = make(map[int]bool, len(movies))
	for _, m := range movies {
		if id := int(floatVal(m, "tmdbId")); id != 0 {
			tmdb[id] = true
		}
		if id := int(floatVal(m, "id")); id != 0 {
			arrIDs[id] = true
		}
	}
	return tmdb, arrIDs, true
}

// sonarrLiveSets fetches every series Sonarr currently tracks and returns its
// identity sets. ok=false means the fetch failed and absence proves nothing.
func sonarrLiveSets(ctx context.Context, svc ArrClient) (tvdb, tmdb, arrIDs map[int]bool, ok bool) {
	data, err := svc.SonarrClient().Get(ctx, "/api/v3/series")
	if err != nil {
		slog.Warn("sweep: sonarr fetch failed, skipping series sweep",
			"component", "catalog_sweep", "error", err)
		return nil, nil, nil, false
	}
	var seriesList []map[string]any
	if err := json.Unmarshal(data, &seriesList); err != nil {
		slog.Warn("sweep: sonarr parse failed, skipping series sweep",
			"component", "catalog_sweep", "error", err)
		return nil, nil, nil, false
	}
	tvdb = make(map[int]bool, len(seriesList))
	tmdb = make(map[int]bool, len(seriesList))
	arrIDs = make(map[int]bool, len(seriesList))
	for _, s := range seriesList {
		if id := int(floatVal(s, "tvdbId")); id != 0 {
			tvdb[id] = true
		}
		if id := int(floatVal(s, "tmdbId")); id != 0 {
			tmdb[id] = true
		}
		if id := int(floatVal(s, "id")); id != 0 {
			arrIDs[id] = true
		}
	}
	return tvdb, tmdb, arrIDs, true
}
