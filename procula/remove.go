// remove.go — "remove" action handler: whole-title removal from the library.
//
// procula NEVER talks to Sonarr/Radarr directly (see docs/ARCHITECTURE.md).
// This handler calls the middleware's internal /api/pelicula/catalog/remove
// endpoint, which performs the *arr DELETE (files + metadata folder) and
// purges its own catalog_items rows. procula's job here is limited to:
// triggering that call, purging its own catalog_flags rows for the deleted
// files, and scheduling a Jellyfin refresh.
package procula

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// removeRespBody is the shape returned by POST /api/pelicula/catalog/remove.
type removeRespBody struct {
	Removed   bool     `json:"removed"`
	ArrType   string   `json:"arr_type"`
	ArrID     int      `json:"arr_id"`
	Title     string   `json:"title"`
	FilePaths []string `json:"file_paths"`
}

// runRemoveAction is the handler for the "remove" action: deletes a whole
// title (movie or series) — including its files — from Sonarr/Radarr via the
// middleware, purges procula's catalog_flags for every affected path, and
// schedules a Jellyfin refresh.
// Params: arr_type (string, "radarr"|"sonarr", required), arr_id (int, required).
//
// Idempotent: if the *arr entry is already gone, the middleware still reports
// removed:true (it treats a 404 from *arr as success), and this handler still
// runs the flag purge + refresh before returning removed:true.
func runRemoveAction(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
	arrType, _ := job.Params["arr_type"].(string)
	arrIDf, _ := job.Params["arr_id"].(float64)
	arrID := int(arrIDf)

	if arrType != "radarr" && arrType != "sonarr" {
		return nil, fmt.Errorf("remove: arr_type must be 'radarr' or 'sonarr'")
	}
	if arrID <= 0 {
		return nil, fmt.Errorf("remove: arr_id required")
	}

	peliculaAPI := env("PELICULA_API_URL", "http://pelicula-api:8181")

	// Step 1: call the middleware's internal endpoint to delete the title
	// (files + metadata) from *arr and purge middleware-side catalog_items.
	reqBody := map[string]any{
		"arr_type": arrType,
		"arr_id":   arrID,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("remove: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		peliculaAPI+"/api/pelicula/catalog/remove", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("remove: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if proculaAPIKey != "" {
		req.Header.Set("X-API-Key", proculaAPIKey)
	}
	httpClient := newProculaClient(30 * time.Second)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("remove: middleware call failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("remove: middleware returned HTTP %d", resp.StatusCode)
	}
	var removeResp removeRespBody
	if err := json.NewDecoder(resp.Body).Decode(&removeResp); err != nil {
		return nil, fmt.Errorf("remove: decode response: %w", err)
	}

	// Step 2: purge procula's catalog_flags for every file the middleware
	// reported as deleted. Best-effort: a flag-purge failure must not fail
	// the action — the title is already gone from Sonarr/Radarr.
	for _, path := range removeResp.FilePaths {
		if err := DeleteFlagsForPath(appDB, path); err != nil {
			slog.Warn("remove: failed to purge catalog_flags for path",
				"component", "remove", "path", path, "error", err)
		}
	}

	// Step 3: trigger a Jellyfin refresh so the removed title disappears
	// from the library view. Uses the existing debounced scheduler.
	if err := scheduleJellyfinRefresh(peliculaAPI); err != nil {
		slog.Warn("remove: Jellyfin refresh failed (non-fatal)", "component", "remove", "error", err)
	}

	slog.Info("removed title from library",
		"component", "remove", "arr_type", arrType, "arr_id", arrID,
		"title", removeResp.Title, "file_count", len(removeResp.FilePaths))

	return map[string]any{
		"removed":    true,
		"arr_type":   arrType,
		"arr_id":     arrID,
		"file_paths": removeResp.FilePaths,
	}, nil
}
