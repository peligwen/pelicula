package catalog

import (
	"database/sql"
	"testing"
)

func testCatalogDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenCatalogDB(":memory:")
	if err != nil {
		t.Fatalf("OpenCatalogDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenCatalogDB_CreatesSchema(t *testing.T) {
	db := testCatalogDB(t)

	rows, err := db.Query(`PRAGMA table_info(catalog_items)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}

	required := []string{
		"id", "type", "parent_id", "tmdb_id", "tvdb_id",
		"arr_id", "arr_type", "jellyfin_id", "episode_id",
		"season_number", "episode_number", "title", "year",
		"tier", "artwork_url", "synopsis", "metadata_synced_at",
		"procula_job_id", "file_path", "created_at", "updated_at",
	}
	for _, col := range required {
		if !cols[col] {
			t.Errorf("missing column: %s", col)
		}
	}

	var ver int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		t.Fatalf("PRAGMA user_version: %v", err)
	}
	if ver != 1 {
		t.Errorf("expected user_version=1, got %d", ver)
	}
}

func TestOpenCatalogDB_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/catalog_test.db"

	db1, err := OpenCatalogDB(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	db1.Close()

	db2, err := OpenCatalogDB(path)
	if err != nil {
		t.Fatalf("second open (should skip already-applied migrations): %v", err)
	}
	db2.Close()
}

func TestUpsertCatalogItem_Movie_InsertAndFind(t *testing.T) {
	db := testCatalogDB(t)

	item := CatalogItem{
		Type:    "movie",
		TmdbID:  123,
		ArrID:   42,
		ArrType: "radarr",
		Title:   "The Matrix",
		Year:    1999,
		Tier:    "pipeline",
	}
	id, err := UpsertCatalogItem(db, item)
	if err != nil {
		t.Fatalf("UpsertCatalogItem: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}

	got, err := GetCatalogItemByID(db, id)
	if err != nil {
		t.Fatalf("GetCatalogItemByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected item, got nil")
	}
	if got.TmdbID != 123 || got.Title != "The Matrix" || got.Tier != "pipeline" {
		t.Errorf("unexpected item: %+v", got)
	}
}

func TestUpsertCatalogItem_TierNotDowngraded(t *testing.T) {
	db := testCatalogDB(t)

	id, err := UpsertCatalogItem(db, CatalogItem{
		Type: "movie", TmdbID: 999, ArrType: "radarr",
		Title: "Dune", Year: 2021, Tier: "pipeline",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	_, err = UpsertCatalogItem(db, CatalogItem{
		Type: "movie", TmdbID: 999, ArrType: "radarr",
		Title: "Dune", Year: 2021, Tier: "queue",
	})
	if err != nil {
		t.Fatalf("upsert downgrade: %v", err)
	}

	got, err := GetCatalogItemByID(db, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Tier != "pipeline" {
		t.Errorf("tier was downgraded: got %q, want %q", got.Tier, "pipeline")
	}
}

func TestUpsertCatalogItem_EpisodeHierarchy(t *testing.T) {
	db := testCatalogDB(t)

	seriesID, err := UpsertCatalogItem(db, CatalogItem{
		Type: "series", TvdbID: 81189, ArrType: "sonarr",
		Title: "Breaking Bad", Year: 2008, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert series: %v", err)
	}

	seasonID, err := UpsertCatalogItem(db, CatalogItem{
		Type: "season", ParentID: seriesID,
		SeasonNumber: 1, Title: "Breaking Bad Season 1",
		Year: 2008, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert season: %v", err)
	}

	epID, err := UpsertCatalogItem(db, CatalogItem{
		Type: "episode", ParentID: seasonID,
		EpisodeID: 55, SeasonNumber: 1, EpisodeNumber: 1,
		ArrType: "sonarr", FilePath: "/media/bb/s01e01.mkv",
		Title: "Pilot", Year: 2008, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert episode: %v", err)
	}

	// Re-upsert same episode by EpisodeID — should update, not duplicate
	_, err = UpsertCatalogItem(db, CatalogItem{
		Type: "episode", ParentID: seasonID,
		EpisodeID: 55, SeasonNumber: 1, EpisodeNumber: 1,
		ArrType: "sonarr", FilePath: "/media/bb/s01e01.mkv",
		Title: "Pilot", Year: 2008, Tier: "library",
		ProculaJobID: "job_123",
	})
	if err != nil {
		t.Fatalf("re-upsert episode: %v", err)
	}

	got, err := GetCatalogItemByID(db, epID)
	if err != nil {
		t.Fatalf("get episode: %v", err)
	}
	if got.ProculaJobID != "job_123" {
		t.Errorf("expected ProculaJobID updated, got %q", got.ProculaJobID)
	}

	items, err := ListCatalogItems(db, CatalogFilter{Type: "episode"})
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("expected 1 episode, got %d", len(items))
	}
}

func TestUpsertCatalogItem_BackfillsTmdbID(t *testing.T) {
	db := testCatalogDB(t)

	// First upsert by arr_id only (no tmdb_id yet)
	id, err := UpsertCatalogItem(db, CatalogItem{
		Type: "movie", ArrID: 77, ArrType: "radarr",
		Title: "Arrival", Year: 2016, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Re-upsert with tmdb_id now known
	_, err = UpsertCatalogItem(db, CatalogItem{
		Type: "movie", ArrID: 77, ArrType: "radarr",
		TmdbID: 329865,
		Title:  "Arrival", Year: 2016, Tier: "library",
	})
	if err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	got, err := GetCatalogItemByID(db, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TmdbID != 329865 {
		t.Errorf("expected TmdbID=329865, got %d", got.TmdbID)
	}
}

func TestUpsertCatalogItem_UpdatesTitle(t *testing.T) {
	db := testCatalogDB(t)

	id, err := UpsertCatalogItem(db, CatalogItem{
		Type: "movie", TmdbID: 500, ArrType: "radarr",
		Title: "Provisional Title", Year: 2022, Tier: "queue",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	_, err = UpsertCatalogItem(db, CatalogItem{
		Type: "movie", TmdbID: 500, ArrType: "radarr",
		Title: "Final Title", Year: 2022, Tier: "queue",
	})
	if err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	got, err := GetCatalogItemByID(db, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Final Title" {
		t.Errorf("expected title updated, got %q", got.Title)
	}
}

func TestGetCatalogItemByFilePath(t *testing.T) {
	db := testCatalogDB(t)

	_, err := UpsertCatalogItem(db, CatalogItem{
		Type:       "movie",
		TmdbID:     101,
		ArrType:    "radarr",
		ArrID:      1,
		Title:      "Test Movie",
		Year:       2020,
		Tier:       "library",
		FilePath:   "/media/movies/test.mkv",
		Synopsis:   "A test film.",
		ArtworkURL: "http://jellyfin/Items/abc/Images/Primary",
	})
	if err != nil {
		t.Fatalf("UpsertCatalogItem: %v", err)
	}

	item, err := GetCatalogItemByFilePath(db, "/media/movies/test.mkv")
	if err != nil {
		t.Fatalf("GetCatalogItemByFilePath: %v", err)
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
	if item.ArtworkURL != "http://jellyfin/Items/abc/Images/Primary" {
		t.Errorf("artwork_url: got %q", item.ArtworkURL)
	}
}

func TestGetCatalogItemByFilePath_NotFound(t *testing.T) {
	db := testCatalogDB(t)
	item, err := GetCatalogItemByFilePath(db, "/media/movies/missing.mkv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item != nil {
		t.Errorf("expected nil, got item with title %q", item.Title)
	}
}

func TestListCatalogItems_FilterByTier(t *testing.T) {
	db := testCatalogDB(t)

	for _, tc := range []struct {
		tmdb int
		tier string
	}{
		{1, "queue"},
		{2, "pipeline"},
		{3, "library"},
	} {
		_, err := UpsertCatalogItem(db, CatalogItem{
			Type: "movie", TmdbID: tc.tmdb, ArrType: "radarr",
			Title: "Movie", Year: 2020, Tier: tc.tier,
		})
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	libs, err := ListCatalogItems(db, CatalogFilter{Tier: "library"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(libs) != 1 || libs[0].TmdbID != 3 {
		t.Errorf("expected 1 library item with tmdb_id=3, got %d items", len(libs))
	}
}
