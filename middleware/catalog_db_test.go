package main

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
}
