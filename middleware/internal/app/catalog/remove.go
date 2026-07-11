// remove.go — Internal endpoint for whole-title removal, called by Procula's
// "remove" action handler. procula never talks to Sonarr/Radarr directly;
// this is where the actual *arr DELETE happens.
package catalog

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"pelicula-api/httputil"
)

// HandleCatalogRemove deletes a whole title (movie or series) — including its
// files and metadata folder — from Sonarr/Radarr, then purges the
// corresponding catalog_items rows (and, for a series, the season/episode
// rows orphaned by removing the parent).
//
// Internal (Docker-network + shared-key) endpoint: gated exactly like
// HandleJellyfinRefresh in internal/app/hooks/proxy.go. Registered WITHOUT
// auth.Guard in router.go — Procula calls this directly, there is no session.
//
// POST /api/pelicula/catalog/remove
// Body: {"arr_type":"radarr"|"sonarr","arr_id":N}
// Response: {"removed":true,"arr_type":..,"arr_id":..,"title":..,"file_paths":[...]}
//
// Idempotent: a 404 from the *arr DELETE (title already gone) is treated as
// success, not an error — Procula's action handler may retry this call.
func (h *Handler) HandleCatalogRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Verify Procula's shared API key so only Procula can trigger a removal.
	// Empty key (test/dev, no PROCULA_API_KEY configured) skips the check —
	// same backward-compatible behavior as HandleJellyfinRefresh.
	if key := strings.TrimSpace(h.ProculaAPIKey); key != "" {
		provided := r.Header.Get("X-API-Key")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(key)) == 0 {
			httputil.WriteError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		ArrType string `json:"arr_type"`
		ArrID   int    `json:"arr_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ArrType != "radarr" && req.ArrType != "sonarr" {
		httputil.WriteError(w, "invalid arr_type", http.StatusBadRequest)
		return
	}
	if req.ArrID <= 0 {
		httputil.WriteError(w, "arr_id required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	filePaths := []string{}
	var title, deletePath string

	if req.ArrType == "radarr" {
		client := h.Arr.RadarrClient()
		if movie, err := client.GetMovie(ctx, "/api/v3", req.ArrID); err != nil {
			slog.Warn("remove: failed to fetch movie before delete",
				"component", "catalog", "arr_id", req.ArrID, "error", err)
		} else if movie != nil {
			title, _ = movie["title"].(string)
			if mf, ok := movie["movieFile"].(map[string]any); ok {
				if p, _ := mf["path"].(string); p != "" {
					filePaths = append(filePaths, p)
				}
			}
		}
		deletePath = fmt.Sprintf("/api/v3/movie/%d?deleteFiles=true&addImportExclusion=false", req.ArrID)
	} else {
		client := h.Arr.SonarrClient()
		if series, err := client.GetSeriesByID(ctx, "/api/v3", fmt.Sprintf("%d", req.ArrID)); err != nil {
			slog.Warn("remove: failed to fetch series before delete",
				"component", "catalog", "arr_id", req.ArrID, "error", err)
		} else if series != nil {
			title, _ = series["title"].(string)
		}
		files, err := client.GetEpisodeFiles(ctx, "/api/v3", req.ArrID)
		if err != nil {
			slog.Warn("remove: failed to fetch episode files before delete",
				"component", "catalog", "arr_id", req.ArrID, "error", err)
		}
		for _, f := range files {
			if p, _ := f["path"].(string); p != "" {
				filePaths = append(filePaths, p)
			}
		}
		deletePath = fmt.Sprintf("/api/v3/series/%d?deleteFiles=true&addImportExclusion=false", req.ArrID)
	}

	arrClient := h.Arr.RadarrClient()
	if req.ArrType == "sonarr" {
		arrClient = h.Arr.SonarrClient()
	}
	if err := arrClient.Delete(ctx, deletePath); err != nil {
		if !isArrNotFoundErr(err) {
			slog.Error("remove: *arr delete failed", "component", "catalog",
				"arr_type", req.ArrType, "arr_id", req.ArrID, "error", err)
			httputil.WriteError(w, req.ArrType+" delete failed", http.StatusBadGateway)
			return
		}
		// *arr reports the title as already gone — idempotent success.
		slog.Info("remove: *arr entry already absent, treating delete as success",
			"component", "catalog", "arr_type", req.ArrType, "arr_id", req.ArrID)
	}

	// Purge our own catalog_items row (and cascade any children left
	// parentless). Best-effort: the *arr side is already done at this point,
	// so a local index cleanup failure must not fail the request.
	store := storeFor(h.DB)
	if _, err := store.DeleteByArr(ctx, req.ArrType, req.ArrID); err != nil {
		slog.Error("remove: catalog_items delete failed", "component", "catalog",
			"arr_type", req.ArrType, "arr_id", req.ArrID, "error", err)
	} else if _, err := store.DeleteOrphanedChildren(ctx); err != nil {
		slog.Error("remove: orphaned-children cascade failed", "component", "catalog", "error", err)
	}

	if title == "" {
		title = fmt.Sprintf("%s #%d", req.ArrType, req.ArrID)
	}

	httputil.WriteJSON(w, map[string]any{
		"removed":    true,
		"arr_type":   req.ArrType,
		"arr_id":     req.ArrID,
		"title":      title,
		"file_paths": filePaths,
	})
}

// isArrNotFoundErr reports whether err — as surfaced by arr.Client.Delete,
// which formats transport/HTTP failures as "HTTP %d from %s" — represents a
// 404 response from the *arr app. A 404 on delete means the title is already
// gone, which HandleCatalogRemove treats as success rather than an error.
func isArrNotFoundErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 404 from")
}
