package catalog_test

import (
	"context"
	"database/sql"
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
