// replace.go — "replace" action handler and blocked_releases DB helpers.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
		`DELETE FROM blocked_releases WHERE id = ? RETURNING arr_blocklist_id`, id,
	).Scan(&blocklistID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("blocked release %d not found", id)
	}
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
//
//	episode_id (int, for sonarr), reason (string, optional).
func runReplaceAction(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
	path, _ := job.Params["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("replace: path required")
	}
	path = filepath.Clean(path)
	if !isLibraryPath(path) {
		return nil, fmt.Errorf("replace: path must be under /media/")
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
