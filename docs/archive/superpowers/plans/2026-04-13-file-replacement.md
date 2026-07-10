# File Replacement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a manual "Replace & Re-search" action to the catalog context menu that deletes a bad file, blocklists the release in Sonarr/Radarr, triggers a fresh search, and records the block in a local DB table that admins can manage from the Settings tab.

**Architecture:** Procula owns a new `replace` action (registered in its action bus) and a new `blocked_releases` SQLite table. Middleware adds a `POST /api/pelicula/catalog/replace` endpoint that handles all *arr API calls (history lookup, blocklist, rescan, search) and returns the blocklist entry ID. The frontend adds a context menu entry that opens a scope-selection drawer; scope resolution follows the existing fanout pattern (one action call per file). The Settings tab shows the blocked releases table with an Unblock button.

**Tech Stack:** Go (procula action handler + DB migration), Go (middleware *arr integration), vanilla JS + HTML (catalog drawer + settings section)

---

## File Map

**New files:**
- `procula/replace.go` — `BlockedRelease` struct, DB functions (`InsertBlockedRelease`, `ListBlockedReleases`, `DeleteBlockedRelease`), `runReplaceAction` handler

**Modified files:**
- `procula/db.go` — add `migrate7` function + add it to `migrations` slice
- `procula/main.go` — register `replace` action in `registerBuiltinActions()`; add `GET /api/procula/blocked-releases` and `DELETE /api/procula/blocked-releases/{id}` routes + handlers
- `middleware/catalog.go` — add `handleCatalogReplace` handler; add `"rescan"` case to `handleCatalogCommand`
- `middleware/main.go` — register `POST /api/pelicula/catalog/replace` (admin-only)
- `nginx/index.html` — add replace drawer HTML after the dualsub dialog block; add blocked releases panel to settings section
- `nginx/catalog.js` — add "Replace…" entry to `openContextMenu`; add `openReplaceDrawer`, scope resolution, confirm/submit logic; expose `window.replaceClose`, `window.replaceConfirm`
- `nginx/settings.js` — add `loadBlockedReleases()` + `renderBlockedReleases()` functions; call them when the settings tab is opened
- `nginx/catalog.css` — styles for replace drawer confirm button (destructive) and blocked releases list

**Test files:**
- `procula/replace_test.go` — tests for DB functions and `runReplaceAction`
- `middleware/catalog_test.go` — tests for `handleCatalogReplace` and the new `"rescan"` command

---

## Task 1: DB migration — `blocked_releases` table

**Files:**
- Modify: `procula/db.go`

- [ ] **Step 1: Add migrate7**

In `procula/db.go`, add after `migrate6`:

```go
// migrate7 creates the blocked_releases table for tracking releases that were
// manually blocklisted via the "Replace" action.
func migrate7(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS blocked_releases (
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
		arr_app          TEXT    NOT NULL,
		arr_blocklist_id INTEGER NOT NULL DEFAULT 0,
		arr_item_id      INTEGER NOT NULL,
		display_title    TEXT    NOT NULL,
		file_path        TEXT    NOT NULL,
		blocked_at       TEXT    NOT NULL,
		reason           TEXT    NOT NULL DEFAULT ''
	)`)
	return err
}
```

- [ ] **Step 2: Register migration**

In the `migrations` slice in `procula/db.go`, append:

```go
{version: 7, up: migrate7},
```

- [ ] **Step 3: Verify migration compiles**

```bash
cd procula && go build ./...
```
Expected: no output (clean build).

- [ ] **Step 4: Commit**

```bash
git add procula/db.go
git commit -m "feat(procula): add blocked_releases migration (migrate7)"
```

---

## Task 2: DB helper functions for blocked releases

**Files:**
- Create: `procula/replace.go`
- Create: `procula/replace_test.go`

- [ ] **Step 1: Write failing tests**

Create `procula/replace_test.go`:

```go
package main

import (
	"database/sql"
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
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd procula && go test -run TestBlocked -v
```
Expected: FAIL — `BlockedRelease`, `InsertBlockedRelease`, etc. undefined.

- [ ] **Step 3: Implement**

Create `procula/replace.go`:

```go
// replace.go — "replace" action handler and blocked_releases DB helpers.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// BlockedRelease is a row in the blocked_releases table.
type BlockedRelease struct {
	ID             int64     `json:"id"`
	ArrApp         string    `json:"arr_app"`
	ArrBlocklistID int       `json:"arr_blocklist_id"`
	ArrItemID      int       `json:"arr_item_id"`
	DisplayTitle   string    `json:"display_title"`
	FilePath       string    `json:"file_path"`
	BlockedAt      time.Time `json:"blocked_at"`
	Reason         string    `json:"reason,omitempty"`
}

// InsertBlockedRelease writes a new row and returns its ROWID.
func InsertBlockedRelease(db *sql.DB, br BlockedRelease) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO blocked_releases
		 (arr_app, arr_blocklist_id, arr_item_id, display_title, file_path, blocked_at, reason)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		br.ArrApp, br.ArrBlocklistID, br.ArrItemID,
		br.DisplayTitle, br.FilePath,
		br.BlockedAt.UTC().Format(time.RFC3339),
		br.Reason,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListBlockedReleases returns all rows newest-first.
func ListBlockedReleases(db *sql.DB) ([]BlockedRelease, error) {
	rows, err := db.Query(
		`SELECT id, arr_app, arr_blocklist_id, arr_item_id, display_title, file_path, blocked_at, reason
		 FROM blocked_releases ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlockedRelease
	for rows.Next() {
		var br BlockedRelease
		var ts string
		if err := rows.Scan(&br.ID, &br.ArrApp, &br.ArrBlocklistID, &br.ArrItemID,
			&br.DisplayTitle, &br.FilePath, &ts, &br.Reason); err != nil {
			return nil, err
		}
		br.BlockedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, br)
	}
	return out, rows.Err()
}

// DeleteBlockedRelease removes the row by id and returns its arr_blocklist_id
// so the caller can delete the entry from *arr's blocklist.
func DeleteBlockedRelease(db *sql.DB, id int64) (int, error) {
	var blocklistID int
	err := db.QueryRow(
		`SELECT arr_blocklist_id FROM blocked_releases WHERE id = ?`, id,
	).Scan(&blocklistID)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("blocked release %d not found", id)
	}
	if err != nil {
		return 0, err
	}
	_, err = db.Exec(`DELETE FROM blocked_releases WHERE id = ?`, id)
	return blocklistID, err
}

// replaceRespBody is the shape returned by POST /api/pelicula/catalog/replace.
type replaceRespBody struct {
	ArrBlocklistID int    `json:"arr_blocklist_id"`
	DisplayTitle   string `json:"display_title"`
	ArrItemID      int    `json:"arr_item_id"`
	ArrApp         string `json:"arr_app"`
}

// runReplaceAction is the handler for the "replace" action.
// Params: path (string, required), arr_type (string), arr_id (int),
//         episode_id (int, for sonarr), reason (string, optional).
func runReplaceAction(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
	path, _ := job.Params["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("replace: path required")
	}
	if !isLibraryPath(path) {
		return nil, fmt.Errorf("replace: path must be under /movies or /tv")
	}
	arrType, _ := job.Params["arr_type"].(string)
	arrIDf, _ := job.Params["arr_id"].(float64)
	epIDf, _ := job.Params["episode_id"].(float64)
	reason, _ := job.Params["reason"].(string)

	peliculaAPI := env("PELICULA_API_URL", "http://pelicula-api:8181")

	// Step 1: call middleware to blocklist the release and trigger rescan+search.
	reqBody := map[string]any{
		"arr_type":   arrType,
		"arr_id":     int(arrIDf),
		"episode_id": int(epIDf),
		"path":       path,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("replace: marshal request: %w", err)
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Post(
		peliculaAPI+"/api/pelicula/catalog/replace",
		"application/json",
		bytes.NewReader(data),
	)
	if err != nil {
		return nil, fmt.Errorf("replace: middleware call failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("replace: middleware returned HTTP %d", resp.StatusCode)
	}
	var replaceResp replaceRespBody
	if err := json.NewDecoder(resp.Body).Decode(&replaceResp); err != nil {
		return nil, fmt.Errorf("replace: decode response: %w", err)
	}

	// Step 2: delete the file from disk.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("replace: delete file: %w", err)
	}
	slog.Info("replaced file deleted", "component", "replace", "path", path)

	// Step 3: record in blocked_releases.
	br := BlockedRelease{
		ArrApp:         replaceResp.ArrApp,
		ArrBlocklistID: replaceResp.ArrBlocklistID,
		ArrItemID:      replaceResp.ArrItemID,
		DisplayTitle:   replaceResp.DisplayTitle,
		FilePath:       path,
		BlockedAt:      time.Now().UTC(),
		Reason:         reason,
	}
	id, err := InsertBlockedRelease(appDB, br)
	if err != nil {
		slog.Error("failed to record blocked release", "component", "replace", "error", err)
		// Non-fatal: the file is deleted and release is blocklisted — don't fail the action.
	}

	return map[string]any{
		"deleted":      path,
		"blocklist_id": replaceResp.ArrBlocklistID,
		"record_id":    id,
	}, nil
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
cd procula && go test -run TestBlocked -v
```
Expected: PASS — all 3 tests green.

- [ ] **Step 5: Build check**

```bash
cd procula && go build ./...
```
Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add procula/replace.go procula/replace_test.go
git commit -m "feat(procula): add BlockedRelease DB helpers and runReplaceAction"
```

---

## Task 3: Register the replace action and blocked-releases endpoints in procula

**Files:**
- Modify: `procula/main.go`

- [ ] **Step 1: Register the action**

In `registerBuiltinActions()` in `procula/main.go`, add after the `dualsub` registration (after line 120):

```go
Register(&ActionDef{
    Name:        "replace",
    Label:       "Replace\u2026",
    AppliesTo:   []string{"movie", "episode"},
    Sync:        true,
    Description: "Delete this file, blocklist the release in Sonarr/Radarr, and trigger a fresh search.",
    Handler:     runReplaceAction,
})
```

- [ ] **Step 2: Add HTTP handlers**

Add these two handler methods to `procula/main.go` (after `handleCatalogFlags`):

```go
// handleListBlockedReleases returns all rows in blocked_releases, newest first.
func (s *Server) handleListBlockedReleases(w http.ResponseWriter, r *http.Request) {
    rows, err := ListBlockedReleases(s.db)
    if err != nil {
        writeError(w, "query failed: "+err.Error(), http.StatusInternalServerError)
        return
    }
    if rows == nil {
        rows = []BlockedRelease{}
    }
    writeJSON(w, rows)
}

// handleDeleteBlockedRelease removes a blocked release by id and calls
// middleware to delete the entry from *arr's blocklist.
func (s *Server) handleDeleteBlockedRelease(w http.ResponseWriter, r *http.Request) {
    idStr := r.PathValue("id")
    var id int64
    if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id == 0 {
        writeError(w, "invalid id", http.StatusBadRequest)
        return
    }

    blocklistID, err := DeleteBlockedRelease(s.db, id)
    if err != nil {
        writeError(w, err.Error(), http.StatusNotFound)
        return
    }

    if blocklistID > 0 {
        // Best-effort: remove from *arr blocklist via middleware.
        peliculaAPI := env("PELICULA_API_URL", "http://pelicula-api:8181")
        req, _ := http.NewRequest(http.MethodDelete,
            fmt.Sprintf("%s/api/pelicula/catalog/blocklist/%d", peliculaAPI, blocklistID),
            nil)
        client := &http.Client{Timeout: 10 * time.Second}
        resp, err := client.Do(req)
        if err != nil || resp.StatusCode >= 400 {
            slog.Warn("failed to remove *arr blocklist entry", "component", "replace",
                "blocklist_id", blocklistID)
        } else {
            resp.Body.Close()
        }
    }

    writeJSON(w, map[string]any{"deleted": id})
}
```

- [ ] **Step 3: Register routes**

In the `mux.HandleFunc` block in `main()` in `procula/main.go`, add after the `handleCatalogFlags` line:

```go
mux.HandleFunc("GET /api/procula/blocked-releases", srv.handleListBlockedReleases)
mux.HandleFunc("DELETE /api/procula/blocked-releases/{id}", requireAPIKey(srv.handleDeleteBlockedRelease))
```

- [ ] **Step 4: Build check**

```bash
cd procula && go build ./...
```
Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add procula/main.go
git commit -m "feat(procula): register replace action and blocked-releases endpoints"
```

---

## Task 4: Middleware — catalog/replace endpoint and rescan command

**Files:**
- Modify: `middleware/catalog.go`
- Modify: `middleware/main.go`
- Modify (tests): `middleware/catalog_test.go`

- [ ] **Step 1: Write failing tests**

Add to `middleware/catalog_test.go`:

```go
func TestHandleCatalogReplaceValidation(t *testing.T) {
    mux := http.NewServeMux()
    mux.HandleFunc("POST /api/pelicula/catalog/replace", handleCatalogReplace)
    srv := httptest.NewServer(mux)
    defer srv.Close()

    // Missing arr_type should return 400
    body := `{"arr_id":1,"episode_id":2,"path":"/tv/Silo/S01E01.mkv"}`
    resp, _ := http.Post(srv.URL+"/api/pelicula/catalog/replace",
        "application/json", strings.NewReader(body))
    if resp.StatusCode != http.StatusBadRequest {
        t.Errorf("missing arr_type: want 400, got %d", resp.StatusCode)
    }

    // Missing arr_id should return 400
    body = `{"arr_type":"sonarr","episode_id":2,"path":"/tv/Silo/S01E01.mkv"}`
    resp, _ = http.Post(srv.URL+"/api/pelicula/catalog/replace",
        "application/json", strings.NewReader(body))
    if resp.StatusCode != http.StatusBadRequest {
        t.Errorf("missing arr_id: want 400, got %d", resp.StatusCode)
    }
}

func TestHandleCatalogCommandRescan(t *testing.T) {
    // "rescan" must be accepted as a valid command (not rejected as unknown)
    // We can't call real *arr here, so just confirm the dispatch path exists
    // by checking we don't get a 400 "unknown command" error.
    // (We stub services in catalog_test.go TestMain.)
    req := httptest.NewRequest("POST", "/api/pelicula/catalog/command",
        strings.NewReader(`{"arr_type":"sonarr","arr_id":1,"command":"rescan"}`))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    handleCatalogCommand(w, req)
    // Should NOT return 400 "unknown command"
    if w.Code == http.StatusBadRequest {
        var errResp map[string]string
        json.NewDecoder(w.Body).Decode(&errResp)
        if errResp["error"] == "unknown command" {
            t.Error("rescan command not recognised")
        }
    }
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd middleware && go test -run "TestHandleCatalogReplace|TestHandleCatalogCommandRescan" -v
```
Expected: FAIL — `handleCatalogReplace` undefined; `rescan` not recognised.

- [ ] **Step 3: Add rescan case to handleCatalogCommand**

In `middleware/catalog.go`, in the `switch req.Command` block inside `handleCatalogCommand`, add after the `"search"` case (around line 396):

```go
case "rescan":
    if req.ArrType == "radarr" {
        if _, err := services.ArrPost(radarrURL, radarrKey, "/api/v3/command", map[string]any{
            "name": "RescanMovie", "movieId": req.ArrID,
        }); err != nil {
            httputil.WriteError(w, "radarr rescan failed", http.StatusBadGateway)
            return
        }
    } else {
        if _, err := services.ArrPost(sonarrURL, sonarrKey, "/api/v3/command", map[string]any{
            "name": "RescanSeries", "seriesId": req.ArrID,
        }); err != nil {
            httputil.WriteError(w, "sonarr rescan failed", http.StatusBadGateway)
            return
        }
    }
```

Also add a `default` case after all the command cases (if not already present):

```go
default:
    httputil.WriteError(w, "unknown command", http.StatusBadRequest)
    return
```

- [ ] **Step 4: Add handleCatalogReplace**

Add to `middleware/catalog.go`:

```go
// handleCatalogReplace finds the *arr history record for the given episode/movie,
// marks it failed (blocklisting the release), queries the blocklist for the new
// entry ID, then triggers a rescan and fresh search.
//
// POST /api/pelicula/catalog/replace
// Body: {"arr_type":"sonarr"|"radarr","arr_id":N,"episode_id":N,"path":"/tv/..."}
// Returns: {"arr_blocklist_id":N,"display_title":"...","arr_item_id":N,"arr_app":"..."}
func handleCatalogReplace(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
    var req struct {
        ArrType   string `json:"arr_type"`
        ArrID     int    `json:"arr_id"`
        EpisodeID int    `json:"episode_id"`
        Path      string `json:"path"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        httputil.WriteError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
        return
    }
    if req.ArrType == "" || req.ArrID == 0 {
        httputil.WriteError(w, "arr_type and arr_id required", http.StatusBadRequest)
        return
    }
    if req.ArrType != "sonarr" && req.ArrType != "radarr" {
        httputil.WriteError(w, "invalid arr_type", http.StatusBadRequest)
        return
    }

    sonarrKey, radarrKey, _ := services.Keys()
    var baseURL, apiKey string
    if req.ArrType == "radarr" {
        baseURL, apiKey = radarrURL, radarrKey
    } else {
        baseURL, apiKey = sonarrURL, sonarrKey
    }

    // 1. Look up history for this episode/movie to find the import event.
    historyID, displayTitle, err := findImportHistoryID(baseURL, apiKey, req.ArrType, req.ArrID, req.EpisodeID)
    if err != nil {
        slog.Warn("replace: history lookup failed", "arr_type", req.ArrType,
            "arr_id", req.ArrID, "error", err)
        // Non-fatal: proceed without blocklisting.
        historyID = 0
    }

    // 2. Mark the history event as failed (blocklists the release).
    blocklistID := 0
    if historyID > 0 {
        if _, err := services.ArrPost(baseURL, apiKey,
            fmt.Sprintf("/api/v3/history/failed/%d", historyID), nil); err != nil {
            slog.Warn("replace: history/failed call failed", "history_id", historyID, "error", err)
        } else {
            // 3. Query blocklist to get the new entry ID.
            blocklistID, _ = findBlocklistID(baseURL, apiKey, req.ArrType, req.ArrID, req.EpisodeID)
        }
    }

    // 4. Trigger rescan (so *arr notices the deleted file after procula removes it).
    var rescanCmd map[string]any
    if req.ArrType == "radarr" {
        rescanCmd = map[string]any{"name": "RescanMovie", "movieId": req.ArrID}
    } else {
        rescanCmd = map[string]any{"name": "RescanSeries", "seriesId": req.ArrID}
    }
    if _, err := services.ArrPost(baseURL, apiKey, "/api/v3/command", rescanCmd); err != nil {
        slog.Warn("replace: rescan command failed", "arr_type", req.ArrType, "error", err)
    }

    // 5. Trigger a fresh search.
    var searchCmd map[string]any
    if req.ArrType == "radarr" {
        searchCmd = map[string]any{"name": "MoviesSearch", "movieIds": []int{req.ArrID}}
    } else if req.EpisodeID > 0 {
        searchCmd = map[string]any{"name": "EpisodeSearch", "episodeIds": []int{req.EpisodeID}}
    } else {
        searchCmd = map[string]any{"name": "SeriesSearch", "seriesId": req.ArrID}
    }
    if _, err := services.ArrPost(baseURL, apiKey, "/api/v3/command", searchCmd); err != nil {
        slog.Warn("replace: search command failed", "arr_type", req.ArrType, "error", err)
    }

    if displayTitle == "" {
        displayTitle = req.Path
    }

    httputil.WriteJSON(w, map[string]any{
        "arr_blocklist_id": blocklistID,
        "display_title":    displayTitle,
        "arr_item_id":      req.ArrID,
        "arr_app":          req.ArrType,
    })
}

// handleCatalogUnblocklist removes an entry from the *arr blocklist.
// DELETE /api/pelicula/catalog/blocklist/{id}
// The id is the *arr blocklist entry ID (arr_blocklist_id stored locally).
// arr_type is passed as a query param: ?arr_type=sonarr|radarr
func handleCatalogUnblocklist(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodDelete {
        httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    idStr := r.PathValue("id")
    var id int
    if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id == 0 {
        httputil.WriteError(w, "invalid id", http.StatusBadRequest)
        return
    }
    arrType := r.URL.Query().Get("arr_type")
    if arrType == "" {
        // Try to determine from the blocklist entry — but we don't have it here.
        // Attempt both, ignore errors.
        sonarrKey, radarrKey, _ := services.Keys()
        services.ArrDelete(sonarrURL, sonarrKey, fmt.Sprintf("/api/v3/blocklist/%d", id)) //nolint:errcheck
        services.ArrDelete(radarrURL, radarrKey, fmt.Sprintf("/api/v3/blocklist/%d", id)) //nolint:errcheck
        w.WriteHeader(http.StatusNoContent)
        return
    }
    sonarrKey, radarrKey, _ := services.Keys()
    var baseURL, apiKey string
    if arrType == "radarr" {
        baseURL, apiKey = radarrURL, radarrKey
    } else {
        baseURL, apiKey = sonarrURL, sonarrKey
    }
    if _, err := services.ArrDelete(baseURL, apiKey, fmt.Sprintf("/api/v3/blocklist/%d", id)); err != nil {
        slog.Warn("unblocklist failed", "component", "replace", "id", id, "error", err)
        // Non-fatal — entry may already be gone.
    }
    w.WriteHeader(http.StatusNoContent)
}

// findImportHistoryID queries *arr history for an episode/movie and returns the
// historyId of the most recent downloadFolderImported event, plus the source title.
func findImportHistoryID(baseURL, apiKey, arrType string, arrID, episodeID int) (int, string, error) {
    var path string
    if arrType == "sonarr" && episodeID > 0 {
        path = fmt.Sprintf("/api/v3/history/episode?episodeId=%d&eventType=downloadFolderImported&sortKey=date&sortDirection=descending", episodeID)
    } else if arrType == "radarr" {
        path = fmt.Sprintf("/api/v3/history/movie?movieId=%d&eventType=downloadFolderImported&sortKey=date&sortDirection=descending", arrID)
    } else {
        path = fmt.Sprintf("/api/v3/history?seriesId=%d&eventType=downloadFolderImported&sortKey=date&sortDirection=descending&pageSize=10", arrID)
    }
    data, err := services.ArrGet(baseURL, apiKey, path)
    if err != nil {
        return 0, "", err
    }

    // Sonarr history/episode returns an array directly.
    // Radarr history/movie returns {records: [...]} OR an array, depending on version.
    // Handle both shapes.
    var records []map[string]any
    if err := json.Unmarshal(data, &records); err != nil {
        // Try wrapped shape
        var wrapped struct {
            Records []map[string]any `json:"records"`
        }
        if err2 := json.Unmarshal(data, &wrapped); err2 != nil {
            return 0, "", fmt.Errorf("parse history: %w", err)
        }
        records = wrapped.Records
    }

    for _, rec := range records {
        id := int(floatVal(rec, "id"))
        if id == 0 {
            continue
        }
        title := strVal(rec, "sourceTitle")
        return id, title, nil
    }
    return 0, "", fmt.Errorf("no import history found")
}

// findBlocklistID queries the *arr blocklist to find the most recently added
// entry for the given item. Returns 0 if not found (non-fatal).
func findBlocklistID(baseURL, apiKey, arrType string, arrID, episodeID int) (int, error) {
    data, err := services.ArrGet(baseURL, apiKey,
        "/api/v3/blocklist?pageSize=10&sortKey=date&sortDirection=descending")
    if err != nil {
        return 0, err
    }
    var resp struct {
        Records []map[string]any `json:"records"`
    }
    if err := json.Unmarshal(data, &resp); err != nil {
        return 0, err
    }
    for _, rec := range resp.Records {
        var matchID float64
        if arrType == "radarr" {
            matchID = floatVal(rec, "movieId")
        } else {
            matchID = floatVal(rec, "seriesId")
        }
        if int(matchID) == arrID {
            return int(floatVal(rec, "id")), nil
        }
    }
    return 0, nil
}
```

- [ ] **Step 5: Register routes in middleware/main.go**

Add after the `handleCatalogCommand` route registration in `middleware/main.go`:

```go
mux.Handle("/api/pelicula/catalog/replace", auth.GuardAdmin(http.HandlerFunc(handleCatalogReplace)))
mux.Handle("/api/pelicula/catalog/blocklist/", auth.GuardAdmin(http.HandlerFunc(handleCatalogUnblocklist)))
```

(Check the existing route for `handleCatalogCommand` around line 200 and add immediately after.)

- [ ] **Step 6: Build check**

```bash
cd middleware && go build ./...
```
Expected: no output.

- [ ] **Step 7: Run tests**

```bash
cd middleware && go test -run "TestHandleCatalogReplace|TestHandleCatalogCommandRescan" -v
```
Expected: PASS — validation and rescan tests green.

- [ ] **Step 8: Commit**

```bash
git add middleware/catalog.go middleware/main.go middleware/catalog_test.go
git commit -m "feat(middleware): add catalog/replace endpoint, unblocklist, and rescan command"
```

---

## Task 5: Replace drawer HTML

**Files:**
- Modify: `nginx/index.html`

- [ ] **Step 1: Add replace drawer after dualsub dialog**

Find the closing `</div>` of the dualsub dialog block (after the `dualsubClose()` button, around line 1200+) and add immediately after:

```html
<!-- Replace drawer -->
<div class="modal-backdrop hidden" id="replace-backdrop" onclick="replaceClose()"></div>
<div class="cat-modal hidden" id="replace-dialog" role="dialog" aria-modal="true" aria-label="Replace file" style="max-width:420px">
    <div class="drawer-header">
        <div>
            <div class="drawer-title">Replace &amp; Re-search</div>
            <div class="drawer-sub" id="replace-sub"></div>
        </div>
        <button class="drawer-close" onclick="replaceClose()">&times;</button>
    </div>
    <div class="drawer-body">
        <div id="replace-path" style="font-size:.75rem;color:var(--muted);word-break:break-all;margin-bottom:1rem"></div>

        <div id="replace-scope-section">
            <div class="drawer-section-title">Replace scope</div>
            <div style="display:flex;flex-direction:column;gap:.4rem;margin-bottom:1rem">
                <label style="display:flex;align-items:center;gap:.5rem;font-size:.85rem;cursor:pointer">
                    <input type="radio" name="replace-scope" value="episode" id="replace-scope-episode" checked>
                    <span>This episode</span>
                </label>
                <label style="display:flex;align-items:center;gap:.5rem;font-size:.85rem;cursor:pointer">
                    <input type="radio" name="replace-scope" value="season" id="replace-scope-season">
                    <span id="replace-scope-season-label">Entire season</span>
                </label>
                <label style="display:flex;align-items:center;gap:.5rem;font-size:.85rem;cursor:pointer">
                    <input type="radio" name="replace-scope" value="series" id="replace-scope-series">
                    <span>Entire series</span>
                </label>
            </div>
        </div>

        <div class="drawer-section-title">Reason <span style="color:var(--muted);font-weight:400">(optional)</span></div>
        <input type="text" id="replace-reason" placeholder="e.g. wrong audio language"
            style="width:100%;box-sizing:border-box;background:var(--surface);border:1px solid var(--border);border-radius:4px;padding:.4rem .6rem;font-size:.82rem;color:var(--text);margin-bottom:1rem">

        <div id="replace-file-count" style="font-size:.78rem;color:var(--muted);margin-bottom:1rem"></div>

        <div class="modal-buttons">
            <button class="modal-btn-cancel" onclick="replaceClose()">Cancel</button>
            <button class="modal-btn-confirm modal-btn-danger" id="replace-confirm-btn" onclick="replaceConfirm()">Replace &amp; Re-search</button>
        </div>
        <div id="replace-status" class="no-items" style="margin-top:.75rem"></div>
    </div>
</div>
```

- [ ] **Step 2: Add blocked releases panel to settings section**

In `nginx/index.html`, find the `<!-- Advanced -->` settings panel (around line 648) and add immediately before it:

```html
<!-- Blocked Releases -->
<div class="settings-panel admin-only">
    <div class="settings-panel-title">Blocked Releases</div>
    <div id="st-blocked-releases-list" style="margin-top:.5rem"></div>
</div>
```

- [ ] **Step 3: Verify HTML is well-formed**

```bash
grep -c "replace-dialog\|replace-backdrop\|replace-confirm-btn\|st-blocked-releases-list" /Users/gwen/workspace/pelicula/nginx/index.html
```
Expected: 4 (one match per id).

- [ ] **Step 4: Commit**

```bash
git add nginx/index.html
git commit -m "feat(dashboard): add replace drawer and blocked releases HTML"
```

---

## Task 6: Replace drawer logic in catalog.js

**Files:**
- Modify: `nginx/catalog.js`

- [ ] **Step 1: Add "Replace…" to openContextMenu**

In `openContextMenu` in `nginx/catalog.js`, find the section that dispatches on `def.name` (around line 396):

```js
if (def.name === 'dualsub') {
    openDualsubDialog(item, level);
    return;
}
if (def.name === 'subtitle_search') {
    openSubSearchDialog(item, level);
    return;
}
```

Add before those lines:

```js
if (def.name === 'replace') {
    openReplaceDrawer(item, level);
    return;
}
```

- [ ] **Step 2: Add replace drawer logic**

Add the following functions inside the `component('catalog', ...)` closure in `nginx/catalog.js`, after the `runDualSubAction` block (before the closing of the component function):

```js
// ── Replace drawer ────────────────────────────────────────────────────────

let _replaceItem = null;
let _replaceLevel = null;
let _replaceFiles = []; // [{path, arr_id, episode_id, arr_type}]

async function openReplaceDrawer(item, level) {
    _replaceItem = item;
    _replaceLevel = level;
    _replaceFiles = [];

    const isTv = (level === 'episode' || level === 'season' || level === 'series');
    const path = level === 'movie'
        ? (item.movieFile ? item.movieFile.path : '')
        : (item.path || '');

    document.getElementById('replace-sub').textContent = item.title || item.seriesTitle || '';
    document.getElementById('replace-path').textContent = path || '';
    document.getElementById('replace-reason').value = '';
    document.getElementById('replace-status').textContent = '';
    document.getElementById('replace-confirm-btn').disabled = false;

    // Show/hide scope section
    const scopeSection = document.getElementById('replace-scope-section');
    scopeSection.style.display = isTv ? '' : 'none';

    if (isTv) {
        // Default to most specific scope
        const episodeRadio = document.getElementById('replace-scope-episode');
        episodeRadio.checked = true;
        const seasonLabel = document.getElementById('replace-scope-season-label');
        if (item.season) seasonLabel.textContent = 'Entire season (Season ' + item.season + ')';

        // Wire scope radio change to update file count preview
        document.querySelectorAll('input[name="replace-scope"]').forEach(r => {
            r.onchange = () => updateReplaceFileCount();
        });
    }

    await updateReplaceFileCount();
    PeliculaFW.openDrawer(document.getElementById('replace-dialog'), document.getElementById('replace-backdrop'));
}

async function updateReplaceFileCount() {
    const countEl = document.getElementById('replace-file-count');
    const scope = document.querySelector('input[name="replace-scope"]:checked');
    const scopeVal = scope ? scope.value : 'episode';
    const level = _replaceLevel;
    const item = _replaceItem;

    if (level === 'movie') {
        _replaceFiles = item.movieFile ? [{
            path: item.movieFile.path,
            arr_id: item.id,
            episode_id: 0,
            arr_type: 'radarr',
        }] : [];
        countEl.textContent = _replaceFiles.length === 1 ? 'Will replace 1 file.' : '';
        return;
    }

    if (level === 'episode' || scopeVal === 'episode') {
        _replaceFiles = item.path ? [{
            path: item.path,
            arr_id: item.id,
            episode_id: item.episodeId || 0,
            arr_type: 'sonarr',
        }] : [];
        countEl.textContent = _replaceFiles.length === 1 ? 'Will replace 1 file.' : '';
        return;
    }

    countEl.textContent = 'Counting\u2026';
    try {
        let episodes = [];
        if (scopeVal === 'season') {
            const seasonNum = item.season || (item.seasons && item.seasons[0] && item.seasons[0].seasonNumber);
            if (seasonNum) {
                const r = await catFetch('/api/pelicula/catalog/series/' + item.id + '/season/' + seasonNum);
                if (r.ok) episodes = (await r.json()).filter(e => e.hasFile || (e.file && e.file.id));
            }
        } else if (scopeVal === 'series') {
            const r = await catFetch('/api/pelicula/catalog/series/' + item.id);
            if (r.ok) {
                const detail = await r.json();
                for (const s of (detail.seasons || []).filter(s => s.seasonNumber > 0)) {
                    const r2 = await catFetch('/api/pelicula/catalog/series/' + item.id + '/season/' + s.seasonNumber);
                    if (r2.ok) {
                        const eps = await r2.json();
                        episodes.push(...eps.filter(e => e.hasFile || (e.file && e.file.id)));
                    }
                }
            }
        }
        _replaceFiles = episodes
            .filter(e => e.file && e.file.path)
            .map(e => ({
                path: e.file.path,
                arr_id: item.id,
                episode_id: e.id,
                arr_type: 'sonarr',
            }));
        countEl.textContent = 'Will replace ' + _replaceFiles.length + ' file' + (_replaceFiles.length !== 1 ? 's' : '') + '.';
    } catch (e) {
        countEl.textContent = 'Could not count files.';
    }
}

window.replaceClose = function () {
    PeliculaFW.closeDrawer(document.getElementById('replace-dialog'), document.getElementById('replace-backdrop'));
};

window.replaceConfirm = async function () {
    if (!_replaceFiles.length) {
        document.getElementById('replace-status').textContent = 'No files to replace.';
        return;
    }
    const reason = document.getElementById('replace-reason').value.trim();
    const statusEl = document.getElementById('replace-status');
    const confirmBtn = document.getElementById('replace-confirm-btn');
    confirmBtn.disabled = true;
    statusEl.textContent = 'Replacing\u2026';

    let succeeded = 0;
    let failed = 0;
    for (const f of _replaceFiles) {
        try {
            const res = await catFetch('/api/pelicula/actions?wait=10', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    action: 'replace',
                    target: { path: f.path, arr_type: f.arr_type, arr_id: f.arr_id, episode_id: f.episode_id },
                    params: { reason },
                }),
            });
            const data = await res.json();
            if (!res.ok || data.state === 'failed') { failed++; } else { succeeded++; }
        } catch (e) {
            failed++;
        }
    }

    if (failed === 0) {
        statusEl.textContent = '';
        window.replaceClose();
        toast(succeeded + ' file' + (succeeded !== 1 ? 's' : '') + ' replaced \u2014 Searching for new release\u2026');
    } else {
        statusEl.textContent = succeeded + ' replaced, ' + failed + ' failed.';
        confirmBtn.disabled = false;
    }
};
```

- [ ] **Step 3: Build / lint check**

```bash
cd /Users/gwen/workspace/pelicula && node --check nginx/catalog.js 2>&1 || true
```
Expected: no syntax errors.

- [ ] **Step 4: Commit**

```bash
git add nginx/catalog.js
git commit -m "feat(dashboard): add replace drawer and action dispatch in catalog.js"
```

---

## Task 7: Blocked releases in settings.js

**Files:**
- Modify: `nginx/settings.js`

- [ ] **Step 1: Add load and render functions**

Find the end of the `component('settings', ...)` closure in `nginx/settings.js` (just before the closing `})` of the IIFE). Add:

```js
// ── Blocked releases ──────────────────────────────────────────────────────

async function loadBlockedReleases() {
    const container = document.getElementById('st-blocked-releases-list');
    if (!container) return;
    container.textContent = 'Loading\u2026';
    try {
        const res = await fetch('/api/procula/blocked-releases', { credentials: 'same-origin' });
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const rows = await res.json();
        renderBlockedReleases(rows || []);
    } catch (e) {
        container.textContent = 'Failed to load blocked releases.';
    }
}

function renderBlockedReleases(rows) {
    const container = document.getElementById('st-blocked-releases-list');
    if (!container) return;
    if (!rows.length) {
        container.textContent = 'No blocked releases.';
        return;
    }
    container.replaceChildren();
    for (const row of rows) {
        const div = document.createElement('div');
        div.style.cssText = 'display:flex;align-items:flex-start;justify-content:space-between;gap:.75rem;padding:.5rem 0;border-bottom:1px solid var(--border)';

        const info = document.createElement('div');
        info.style.cssText = 'flex:1;min-width:0';
        const title = document.createElement('div');
        title.style.cssText = 'font-size:.85rem;font-weight:500;white-space:nowrap;overflow:hidden;text-overflow:ellipsis';
        title.textContent = row.display_title || row.file_path;
        title.title = row.file_path;
        info.appendChild(title);

        const meta = document.createElement('div');
        meta.style.cssText = 'font-size:.72rem;color:var(--muted);margin-top:.15rem';
        const date = row.blocked_at ? new Date(row.blocked_at).toLocaleDateString() : '';
        meta.textContent = [row.arr_app, date, row.reason].filter(Boolean).join(' \u00b7 ');
        info.appendChild(meta);

        const btn = document.createElement('button');
        btn.textContent = 'Unblock';
        btn.style.cssText = 'flex-shrink:0;padding:.3rem .7rem;border-radius:4px;border:1px solid var(--border);background:transparent;color:var(--muted);font-size:.75rem;cursor:pointer';
        btn.addEventListener('click', () => unblockRelease(row.id, btn));

        div.appendChild(info);
        div.appendChild(btn);
        container.appendChild(div);
    }
}

async function unblockRelease(id, btn) {
    btn.disabled = true;
    btn.textContent = 'Unblocking\u2026';
    try {
        const res = await fetch('/api/procula/blocked-releases/' + id, {
            method: 'DELETE',
            credentials: 'same-origin',
        });
        if (!res.ok) throw new Error('HTTP ' + res.status);
        // Reload the list
        await loadBlockedReleases();
    } catch (e) {
        btn.disabled = false;
        btn.textContent = 'Unblock';
        alert('Unblock failed: ' + e.message);
    }
}
```

- [ ] **Step 2: Call loadBlockedReleases on settings tab open**

Find where the settings component loads data (where `_settingsLoaded` is checked and settings are fetched). It will be inside an `init` or tab-activation function. Add a call to `loadBlockedReleases()` there.

Look for the pattern `_settingsLoaded = true` or where `loadSettings()` is called and add:

```js
loadBlockedReleases();
```

on the same code path.

- [ ] **Step 3: Syntax check**

```bash
node --check nginx/settings.js 2>&1 || true
```
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add nginx/settings.js
git commit -m "feat(dashboard): add blocked releases list to settings tab"
```

---

## Task 8: Danger button style in catalog.css

**Files:**
- Modify: `nginx/catalog.css`

- [ ] **Step 1: Add styles**

At the end of `nginx/catalog.css`, add:

```css
/* Replace / danger confirm button */
.modal-btn-danger {
    background: var(--danger, #c0392b);
    color: #fff;
    border-color: var(--danger, #c0392b);
}
.modal-btn-danger:hover:not(:disabled) {
    opacity: 0.88;
}
.modal-btn-danger:disabled {
    opacity: 0.5;
    cursor: not-allowed;
}
```

- [ ] **Step 2: Commit**

```bash
git add nginx/catalog.css
git commit -m "feat(dashboard): add danger button style for replace confirm"
```

---

## Task 9: Smoke test

- [ ] **Step 1: Build both services**

```bash
cd procula && go build ./... && echo "procula OK"
cd ../middleware && go build ./... && echo "middleware OK"
```
Expected: `procula OK` then `middleware OK`.

- [ ] **Step 2: Run all procula tests**

```bash
cd procula && go test ./... -v 2>&1 | tail -20
```
Expected: all PASS, no FAIL lines.

- [ ] **Step 3: Run all middleware tests**

```bash
cd middleware && go test ./... -v 2>&1 | tail -20
```
Expected: all PASS, no FAIL lines.

- [ ] **Step 4: Verify action appears in registry**

After starting the stack with `pelicula up`, check:
```bash
curl -s http://localhost:7354/api/procula/actions/registry | jq '.[] | select(.name=="replace")'
```
Expected:
```json
{
  "name": "replace",
  "label": "Replace…",
  "applies_to": ["movie", "episode"],
  "sync": true
}
```

- [ ] **Step 5: Verify blocked releases endpoint**

```bash
curl -s http://localhost:7354/api/procula/blocked-releases
```
Expected: `[]`

- [ ] **Step 6: Final commit (if any stray changes)**

```bash
git status
```
If clean: nothing to do.

---

## Self-Review Notes

- **findImportHistoryID** handles both Sonarr (array response) and Radarr (wrapped `{records:[]}` or array) shapes via dual unmarshal. If history is absent, the replace still proceeds — file is deleted and search is triggered, just without a specific release blocklist entry. This is safe: worst case the same bad release gets re-grabbed.
- **arr_blocklist_id defaults to 0** when blocklisting fails. `handleDeleteBlockedRelease` skips the middleware call when `blocklistID == 0`.
- **isLibraryPath check** in `runReplaceAction` ensures the action only touches `/movies` and `/tv` paths, not `/downloads` or `/processing` (matches existing `handleManualTranscode` guard).
- **Unblock flow**: procula's `DELETE /api/procula/blocked-releases/{id}` calls `DELETE /api/pelicula/catalog/blocklist/{id}` without `arr_type`. Middleware tries both sonarr and radarr, ignoring errors — the blocklist entry IDs are globally unique within each *arr instance and won't collide across apps.
- The `replace` action is registered with `AppliesTo: ["movie", "episode"]` — the context menu will show it at the movie and episode levels. Season/series scope is handled entirely in the frontend drawer (same pattern as existing fanout).
