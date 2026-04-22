package procula

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// TestRunReplaceAction_AbortsWhenBlocklistIDZero verifies that runReplaceAction
// returns an error and does NOT delete the file when the middleware returns
// arr_blocklist_id == 0.
func TestRunReplaceAction_AbortsWhenBlocklistIDZero(t *testing.T) {
	// Stub middleware that returns arr_blocklist_id: 0.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := replaceRespBody{
			ArrBlocklistID: 0,
			ArrItemID:      1,
			ArrApp:         "radarr",
			DisplayTitle:   "Test Movie",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer srv.Close()

	// Create a real file to verify it is NOT deleted.
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.mkv")
	if err := os.WriteFile(filePath, []byte("fake media"), 0644); err != nil {
		t.Fatalf("create file: %v", err)
	}

	// Override isLibraryPath to accept the temp dir path for this test.
	old := isLibraryPathFn
	isLibraryPathFn = func(p string) bool { return strings.HasPrefix(p, dir) }
	t.Cleanup(func() { isLibraryPathFn = old })

	// Override appDB for this test.
	db := testDB(t)
	oldDB := appDB
	appDB = db
	t.Cleanup(func() { appDB = oldDB })

	t.Setenv("PELICULA_API_URL", srv.URL)

	q := newTestQueue(t)
	job, _ := q.Create(testSource(filePath))
	jobPtr, _ := q.Get(job.ID)
	jobPtr.Params = map[string]any{
		"path":     filePath,
		"arr_id":   float64(1),
		"arr_type": "radarr",
	}

	_, err := runReplaceAction(context.Background(), q, jobPtr)
	if err == nil {
		t.Fatal("expected error when arr_blocklist_id == 0, got nil")
	}
	if !strings.Contains(err.Error(), "no import history found") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "no import history found")
	}

	// File must NOT have been deleted.
	if _, statErr := os.Stat(filePath); os.IsNotExist(statErr) {
		t.Error("file was deleted even though arr_blocklist_id == 0 (guard should have prevented deletion)")
	}
}
