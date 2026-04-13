package main

import (
	"testing"
	"time"
)

func TestBlockedReleasesRoundTrip(t *testing.T) {
	db := testDB(t)

	br := BlockedRelease{
		ArrApp:         "sonarr",
		ArrBlocklistID: 42,
		ArrItemID:      7,
		DisplayTitle:   "Silo S01E01",
		FilePath:       "/tv/Silo/Season 01/Silo.S01E01.mkv",
		BlockedAt:      time.Now().UTC().Truncate(time.Second),
		Reason:         "Italian audio",
	}

	id, err := InsertBlockedRelease(db, br)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	rows, err := ListBlockedReleases(db)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].DisplayTitle != "Silo S01E01" {
		t.Errorf("title mismatch: %q", rows[0].DisplayTitle)
	}
}

func TestDeleteBlockedRelease(t *testing.T) {
	db := testDB(t)

	br := BlockedRelease{
		ArrApp: "radarr", ArrBlocklistID: 99, ArrItemID: 3,
		DisplayTitle: "Interstellar", FilePath: "/movies/Interstellar.mkv",
		BlockedAt: time.Now().UTC(),
	}
	id, err := InsertBlockedRelease(db, br)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	blocklistID, err := DeleteBlockedRelease(db, id)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if blocklistID != 99 {
		t.Errorf("expected blocklistID 99, got %d", blocklistID)
	}

	rows, err := ListBlockedReleases(db)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows after delete, got %d", len(rows))
	}
}

func TestDeleteBlockedReleaseNotFound(t *testing.T) {
	db := testDB(t)
	_, err := DeleteBlockedRelease(db, 9999)
	if err == nil {
		t.Fatal("expected error for missing id")
	}
}
