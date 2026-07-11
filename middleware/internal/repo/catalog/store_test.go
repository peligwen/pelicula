package catalog_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
	repocat "pelicula-api/internal/repo/catalog"
)

// newTestDB opens an in-memory SQLite database with the catalog_items schema.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		t.Fatalf("WAL mode: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE catalog_items (
			id                 TEXT PRIMARY KEY,
			type               TEXT NOT NULL,
			parent_id          TEXT NOT NULL DEFAULT '',
			tmdb_id            INTEGER NOT NULL DEFAULT 0,
			tvdb_id            INTEGER NOT NULL DEFAULT 0,
			arr_id             INTEGER NOT NULL DEFAULT 0,
			arr_type           TEXT NOT NULL DEFAULT '',
			jellyfin_id        TEXT NOT NULL DEFAULT '',
			episode_id         INTEGER NOT NULL DEFAULT 0,
			season_number      INTEGER NOT NULL DEFAULT 0,
			episode_number     INTEGER NOT NULL DEFAULT 0,
			title              TEXT NOT NULL,
			year               INTEGER NOT NULL DEFAULT 0,
			tier               TEXT NOT NULL,
			artwork_url        TEXT NOT NULL DEFAULT '',
			synopsis           TEXT NOT NULL DEFAULT '',
			metadata_synced_at TEXT NOT NULL DEFAULT '',
			procula_job_id     TEXT NOT NULL DEFAULT '',
			file_path          TEXT NOT NULL DEFAULT '',
			source             TEXT NOT NULL DEFAULT 'arr',
			created_at         TEXT NOT NULL,
			updated_at         TEXT NOT NULL
		)
	`); err != nil {
		db.Close()
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// ── Upsert → Get round-trip ───────────────────────────────────────────────────

func TestUpsert_Get_Movie(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	item := repocat.Item{
		Type:    "movie",
		TmdbID:  123,
		ArrID:   42,
		ArrType: "radarr",
		Title:   "The Matrix",
		Year:    1999,
		Tier:    "pipeline",
	}
	id, err := s.Upsert(ctx, item)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected item, got nil")
	}
	if got.TmdbID != 123 || got.Title != "The Matrix" || got.Tier != "pipeline" {
		t.Errorf("unexpected item: %+v", got)
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Error("expected timestamps to be set")
	}
}

func TestUpsert_Get_NotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	got, err := s.Get(ctx, "cat_nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

// ── Tier is never downgraded ──────────────────────────────────────────────────

func TestUpsert_TierNotDowngraded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	id, err := s.Upsert(ctx, repocat.Item{
		Type: "movie", TmdbID: 999, ArrType: "radarr",
		Title: "Dune", Year: 2021, Tier: "pipeline",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	if _, err := s.Upsert(ctx, repocat.Item{
		Type: "movie", TmdbID: 999, ArrType: "radarr",
		Title: "Dune", Year: 2021, Tier: "queue",
	}); err != nil {
		t.Fatalf("upsert downgrade: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Tier != "pipeline" {
		t.Errorf("tier was downgraded: got %q, want %q", got.Tier, "pipeline")
	}
}

// ── Episode hierarchy ─────────────────────────────────────────────────────────

func TestUpsert_EpisodeHierarchy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	seriesID, err := s.Upsert(ctx, repocat.Item{
		Type: "series", TvdbID: 81189, ArrType: "sonarr",
		Title: "Breaking Bad", Year: 2008, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert series: %v", err)
	}

	seasonID, err := s.Upsert(ctx, repocat.Item{
		Type: "season", ParentID: seriesID,
		SeasonNumber: 1, Title: "Breaking Bad Season 1",
		Year: 2008, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert season: %v", err)
	}

	epID, err := s.Upsert(ctx, repocat.Item{
		Type: "episode", ParentID: seasonID,
		EpisodeID: 55, SeasonNumber: 1, EpisodeNumber: 1,
		ArrType: "sonarr", FilePath: "/media/bb/s01e01.mkv",
		Title: "Pilot", Year: 2008, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert episode: %v", err)
	}

	// Re-upsert same episode by EpisodeID — should update, not duplicate.
	if _, err := s.Upsert(ctx, repocat.Item{
		Type: "episode", ParentID: seasonID,
		EpisodeID: 55, SeasonNumber: 1, EpisodeNumber: 1,
		ArrType: "sonarr", FilePath: "/media/bb/s01e01.mkv",
		Title: "Pilot", Year: 2008, Tier: "library",
		ProculaJobID: "job_123",
	}); err != nil {
		t.Fatalf("re-upsert episode: %v", err)
	}

	got, err := s.Get(ctx, epID)
	if err != nil {
		t.Fatalf("get episode: %v", err)
	}
	if got.ProculaJobID != "job_123" {
		t.Errorf("expected ProculaJobID updated, got %q", got.ProculaJobID)
	}

	// Exactly one episode in the list.
	items, err := s.List(ctx, repocat.Filter{Type: "episode"})
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("expected 1 episode, got %d", len(items))
	}
}

// ── List with filters ─────────────────────────────────────────────────────────

func TestList_FilterByTier(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	for _, tc := range []struct {
		tmdb int
		tier string
	}{
		{1, "queue"},
		{2, "pipeline"},
		{3, "library"},
	} {
		if _, err := s.Upsert(ctx, repocat.Item{
			Type: "movie", TmdbID: tc.tmdb, ArrType: "radarr",
			Title: "Movie", Year: 2020, Tier: tc.tier,
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	libs, err := s.List(ctx, repocat.Filter{Tier: "library"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(libs) != 1 || libs[0].TmdbID != 3 {
		t.Errorf("expected 1 library item with tmdb_id=3, got %d items", len(libs))
	}
}

func TestList_FilterByType(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	for _, tc := range []struct {
		tmdb int
		typ  string
	}{
		{10, "movie"},
		{20, "movie"},
		{30, "series"},
	} {
		if _, err := s.Upsert(ctx, repocat.Item{
			Type: tc.typ, TmdbID: tc.tmdb, ArrType: "radarr",
			Title: "Item", Year: 2020, Tier: "library",
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	movies, err := s.List(ctx, repocat.Filter{Type: "movie"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(movies) != 2 {
		t.Errorf("expected 2 movies, got %d", len(movies))
	}
}

func TestList_FilterByQuery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	for _, title := range []string{"The Matrix", "Matrix Reloaded", "Inception"} {
		if _, err := s.Upsert(ctx, repocat.Item{
			Type: "movie", TmdbID: len(title), ArrType: "radarr",
			Title: title, Year: 2000, Tier: "library",
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	results, err := s.List(ctx, repocat.Filter{Query: "matrix"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 matrix items, got %d", len(results))
	}
}

func TestList_DefaultLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	// Insert 3 items; default limit is 100, so all should be returned.
	for i := 1; i <= 3; i++ {
		if _, err := s.Upsert(ctx, repocat.Item{
			Type: "movie", TmdbID: i, ArrType: "radarr",
			Title: "Movie", Year: 2020, Tier: "queue",
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	items, err := s.List(ctx, repocat.Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("expected 3 items, got %d", len(items))
	}
}

// ── GetByFilePath ─────────────────────────────────────────────────────────────

func TestGetByFilePath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	if _, err := s.Upsert(ctx, repocat.Item{
		Type: "movie", TmdbID: 101, ArrID: 1, ArrType: "radarr",
		Title: "Test Movie", Year: 2020, Tier: "library",
		FilePath:   "/media/movies/test.mkv",
		Synopsis:   "A test film.",
		ArtworkURL: "http://jellyfin/Items/abc/Images/Primary",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	item, err := s.GetByFilePath(ctx, "/media/movies/test.mkv")
	if err != nil {
		t.Fatalf("GetByFilePath: %v", err)
	}
	if item == nil {
		t.Fatal("expected item, got nil")
	}
	if item.Title != "Test Movie" {
		t.Errorf("title: got %q, want %q", item.Title, "Test Movie")
	}
	if item.Synopsis != "A test film." {
		t.Errorf("synopsis: got %q, want %q", item.Synopsis, "A test film.")
	}
}

func TestGetByFilePath_NotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	item, err := s.GetByFilePath(ctx, "/media/movies/missing.mkv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item != nil {
		t.Errorf("expected nil, got item with title %q", item.Title)
	}
}

// ── UpdateMetadata ────────────────────────────────────────────────────────────

func TestUpdateMetadata(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	id, err := s.Upsert(ctx, repocat.Item{
		Type: "movie", TmdbID: 200, ArrType: "radarr",
		Title: "Arrival", Year: 2016, Tier: "library",
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := s.UpdateMetadata(ctx, id, "jf-abc", "http://art/url", "A sci-fi film.", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.JellyfinID != "jf-abc" {
		t.Errorf("JellyfinID: got %q, want %q", got.JellyfinID, "jf-abc")
	}
	if got.ArtworkURL != "http://art/url" {
		t.Errorf("ArtworkURL: got %q", got.ArtworkURL)
	}
	if got.Synopsis != "A sci-fi film." {
		t.Errorf("Synopsis: got %q", got.Synopsis)
	}
	if got.MetadataSyncedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("MetadataSyncedAt: got %q", got.MetadataSyncedAt)
	}
}

func TestUpdateMetadata_NotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	err := s.UpdateMetadata(ctx, "cat_nonexistent", "jf-x", "", "", "")
	if err == nil {
		t.Fatal("expected error for missing item, got nil")
	}
}

// ── Backfill fields on re-upsert ──────────────────────────────────────────────

func TestUpsert_BackfillsTmdbID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	id, err := s.Upsert(ctx, repocat.Item{
		Type: "movie", ArrID: 77, ArrType: "radarr",
		Title: "Arrival", Year: 2016, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	if _, err := s.Upsert(ctx, repocat.Item{
		Type: "movie", ArrID: 77, ArrType: "radarr",
		TmdbID: 329865,
		Title:  "Arrival", Year: 2016, Tier: "library",
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TmdbID != 329865 {
		t.Errorf("expected TmdbID=329865, got %d", got.TmdbID)
	}
}

// ── Concurrent upsert regression ─────────────────────────────────────────────

// TestUpsert_ConcurrentSameKey verifies that N concurrent callers with the
// same natural key produce exactly one row. This is the regression test for
// the find-then-insert race that existed before Upsert was wrapped in a
// transaction.
func TestUpsert_ConcurrentSameKey(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	s := repocat.New(db)

	item := repocat.Item{
		Type:    "movie",
		TmdbID:  999001,
		ArrID:   501,
		ArrType: "radarr",
		Title:   "Concurrent Movie",
		Year:    2024,
		Tier:    "queue",
	}

	const N = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			<-start
			s.Upsert(ctx, item) //nolint:errcheck — only row count matters here
		}()
	}
	close(start)
	wg.Wait()

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_items WHERE tmdb_id=?`, item.TmdbID).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 row, got %d — duplicate insert race still present", count)
	}
}

// ── scanItem error contract ───────────────────────────────────────────────────

// TestScanItem_ErrorReturnsNilItem verifies that scanItem (exercised via Get)
// returns (nil, non-nil-err) when the row cannot be scanned, rather than a
// half-populated Item alongside an error.
func TestScanItem_ErrorReturnsNilItem(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	s := repocat.New(db)

	// Insert a row with 'year' set to a non-numeric string via raw SQL,
	// bypassing Go-level type checking. modernc/sqlite stores it as TEXT;
	// scanning into *int will fail.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO catalog_items
			(id, type, parent_id, tmdb_id, tvdb_id, arr_id, arr_type,
			 jellyfin_id, episode_id, season_number, episode_number,
			 title, year, tier, artwork_url, synopsis,
			 metadata_synced_at, procula_job_id, file_path, created_at, updated_at)
		VALUES ('cat_scantest','movie','',0,0,0,'','',0,0,0,'Bad Year','not-a-number',
		        'queue','','','','','','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')
	`); err != nil {
		t.Fatalf("seed bad row: %v", err)
	}

	item, err := s.Get(ctx, "cat_scantest")
	if err == nil {
		t.Fatal("expected scan error, got nil")
	}
	if item != nil {
		t.Errorf("expected nil Item on scan error, got %+v", item)
	}
}

// ── Sweep primitives ──────────────────────────────────────────────────────────

// seedRow inserts a minimal catalog row with explicit id, type, parent_id and
// updated_at via raw SQL, bypassing Upsert so tests control timestamps.
func seedRow(t *testing.T, db *sql.DB, id, typ, parentID, updatedAt string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO catalog_items
			(id, type, parent_id, title, tier, created_at, updated_at)
		VALUES (?,?,?,?,'library',?,?)
	`, id, typ, parentID, "row "+id, updatedAt, updatedAt); err != nil {
		t.Fatalf("seed row %s: %v", id, err)
	}
}

func countRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM catalog_items`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

func TestListRoots_ReturnsOnlyMoviesAndSeries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	s := repocat.New(db)

	seedRow(t, db, "m1", "movie", "", "2026-01-01T00:00:00Z")
	seedRow(t, db, "s1", "series", "", "2026-01-01T00:00:00Z")
	seedRow(t, db, "se1", "season", "s1", "2026-01-01T00:00:00Z")
	seedRow(t, db, "ep1", "episode", "se1", "2026-01-01T00:00:00Z")

	roots, err := s.ListRoots(ctx)
	if err != nil {
		t.Fatalf("ListRoots: %v", err)
	}
	if len(roots) != 2 {
		t.Fatalf("expected 2 roots, got %d: %+v", len(roots), roots)
	}
	for _, it := range roots {
		if it.Type != "movie" && it.Type != "series" {
			t.Errorf("unexpected type in roots: %q", it.Type)
		}
	}
}

func TestDeleteStale_RespectsUpdatedBeforeGuard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	s := repocat.New(db)

	seedRow(t, db, "old", "movie", "", "2026-01-01T00:00:00Z")
	seedRow(t, db, "fresh", "movie", "", "2026-06-01T00:00:00Z")

	// Both IDs are doomed by the caller, but only the row older than the
	// cutoff may actually be deleted.
	n, err := s.DeleteStale(ctx, []string{"old", "fresh"}, "2026-03-01T00:00:00Z")
	if err != nil {
		t.Fatalf("DeleteStale: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 deletion, got %d", n)
	}

	if got, _ := s.Get(ctx, "old"); got != nil {
		t.Error("stale row survived DeleteStale")
	}
	if got, _ := s.Get(ctx, "fresh"); got == nil {
		t.Error("fresh row was deleted despite updated_at >= cutoff")
	}
}

func TestDeleteStale_EmptyIDsIsNoop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	n, err := s.DeleteStale(ctx, nil, "2026-03-01T00:00:00Z")
	if err != nil {
		t.Fatalf("DeleteStale: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 deletions, got %d", n)
	}
}

// TestDeleteStale_ChunksLargeIDLists crosses the internal chunk boundary
// (500) to prove multi-statement batching deletes every doomed row.
func TestDeleteStale_ChunksLargeIDLists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	s := repocat.New(db)

	const total = 750
	ids := make([]string, 0, total)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("bulk_%04d", i)
		if _, err := tx.Exec(`
			INSERT INTO catalog_items (id, type, parent_id, title, tier, created_at, updated_at)
			VALUES (?,?,?,?,'library','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')
		`, id, "movie", "", "bulk "+id); err != nil {
			t.Fatalf("seed bulk row: %v", err)
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed tx: %v", err)
	}

	n, err := s.DeleteStale(ctx, ids, "2026-03-01T00:00:00Z")
	if err != nil {
		t.Fatalf("DeleteStale: %v", err)
	}
	if n != total {
		t.Errorf("expected %d deletions, got %d", total, n)
	}
	if left := countRows(t, db); left != 0 {
		t.Errorf("expected empty table, %d rows remain", left)
	}
}

// ── DeleteByArr ───────────────────────────────────────────────────────────────

func TestDeleteByArr_DeletesMatchingRoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	id, err := s.Upsert(ctx, repocat.Item{
		Type: "movie", ArrID: 42, ArrType: "radarr",
		Title: "The Matrix", Year: 1999, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	n, err := s.DeleteByArr(ctx, "radarr", 42)
	if err != nil {
		t.Fatalf("DeleteByArr: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 deletion, got %d", n)
	}
	if got, _ := s.Get(ctx, id); got != nil {
		t.Error("row survived DeleteByArr")
	}
}

func TestDeleteByArr_NoMatchIsNoop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	n, err := s.DeleteByArr(ctx, "radarr", 9999)
	if err != nil {
		t.Fatalf("DeleteByArr: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 deletions, got %d", n)
	}
}

// TestDeleteByArr_DoesNotTouchOtherArrType verifies (arr_id, arr_type) is a
// compound match — a sonarr series with the same numeric ID as a radarr
// movie must survive a radarr-targeted delete.
func TestDeleteByArr_DoesNotTouchOtherArrType(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := repocat.New(newTestDB(t))

	movieID, err := s.Upsert(ctx, repocat.Item{
		Type: "movie", ArrID: 7, ArrType: "radarr",
		Title: "Movie Seven", Year: 2020, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert movie: %v", err)
	}
	seriesID, err := s.Upsert(ctx, repocat.Item{
		Type: "series", ArrID: 7, ArrType: "sonarr",
		Title: "Series Seven", Year: 2020, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert series: %v", err)
	}

	if _, err := s.DeleteByArr(ctx, "radarr", 7); err != nil {
		t.Fatalf("DeleteByArr: %v", err)
	}

	if got, _ := s.Get(ctx, movieID); got != nil {
		t.Error("radarr movie survived its own DeleteByArr call")
	}
	if got, _ := s.Get(ctx, seriesID); got == nil {
		t.Error("sonarr series with same arr_id was deleted by a radarr-scoped call")
	}
}

// TestDeleteByArr_ThenDeleteOrphanedChildren_CascadesSeriesRemoval verifies the
// documented two-step removal flow: DeleteByArr drops the series root, then
// DeleteOrphanedChildren sweeps the now-parentless season/episode rows.
func TestDeleteByArr_ThenDeleteOrphanedChildren_CascadesSeriesRemoval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	s := repocat.New(db)

	seriesID, err := s.Upsert(ctx, repocat.Item{
		Type: "series", ArrID: 10, ArrType: "sonarr",
		Title: "Breaking Bad", Year: 2008, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert series: %v", err)
	}
	seasonID, err := s.Upsert(ctx, repocat.Item{
		Type: "season", ParentID: seriesID, SeasonNumber: 1,
		Title: "Breaking Bad Season 1", Year: 2008, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert season: %v", err)
	}
	epID, err := s.Upsert(ctx, repocat.Item{
		Type: "episode", ParentID: seasonID, EpisodeID: 55,
		SeasonNumber: 1, EpisodeNumber: 1, ArrType: "sonarr",
		FilePath: "/media/bb/s01e01.mkv", Title: "Pilot", Year: 2008, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert episode: %v", err)
	}

	n, err := s.DeleteByArr(ctx, "sonarr", 10)
	if err != nil {
		t.Fatalf("DeleteByArr: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 root deletion, got %d", n)
	}

	// Children still present until the cascade sweep runs.
	if got, _ := s.Get(ctx, seasonID); got == nil {
		t.Fatal("season deleted before DeleteOrphanedChildren ran")
	}

	cascaded, err := s.DeleteOrphanedChildren(ctx)
	if err != nil {
		t.Fatalf("DeleteOrphanedChildren: %v", err)
	}
	if cascaded != 2 {
		t.Errorf("expected 2 cascaded deletions (season+episode), got %d", cascaded)
	}
	if got, _ := s.Get(ctx, seasonID); got != nil {
		t.Error("season survived cascade")
	}
	if got, _ := s.Get(ctx, epID); got != nil {
		t.Error("episode survived cascade")
	}
}

func TestDeleteOrphanedChildren_CascadesSeasonsThenEpisodes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	s := repocat.New(db)

	// Live chain: series → season → episode. Must survive untouched.
	seedRow(t, db, "live_series", "series", "", "2026-01-01T00:00:00Z")
	seedRow(t, db, "live_season", "season", "live_series", "2026-01-01T00:00:00Z")
	seedRow(t, db, "live_ep", "episode", "live_season", "2026-01-01T00:00:00Z")

	// Dead chain: parent series already swept — season and episode must
	// cascade in one pass.
	seedRow(t, db, "dead_season", "season", "gone_series", "2026-01-01T00:00:00Z")
	seedRow(t, db, "dead_ep", "episode", "dead_season", "2026-01-01T00:00:00Z")

	n, err := s.DeleteOrphanedChildren(ctx)
	if err != nil {
		t.Fatalf("DeleteOrphanedChildren: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 deletions, got %d", n)
	}

	for _, id := range []string{"live_series", "live_season", "live_ep"} {
		if got, _ := s.Get(ctx, id); got == nil {
			t.Errorf("live row %s was deleted", id)
		}
	}
	for _, id := range []string{"dead_season", "dead_ep"} {
		if got, _ := s.Get(ctx, id); got != nil {
			t.Errorf("orphaned row %s survived", id)
		}
	}

	// Idempotent: second pass deletes nothing.
	n, err = s.DeleteOrphanedChildren(ctx)
	if err != nil {
		t.Fatalf("DeleteOrphanedChildren rerun: %v", err)
	}
	if n != 0 {
		t.Errorf("expected idempotent rerun, got %d deletions", n)
	}
}
