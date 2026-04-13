// catalog.go — thin Radarr/Sonarr proxies powering the dashboard Catalog tab.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"pelicula-api/httputil"
	"strings"
)

type catalogResponse struct {
	Movies []json.RawMessage `json:"movies"`
	Series []json.RawMessage `json:"series"`
}

// handleCatalogList returns movies and series from Radarr+Sonarr in parallel.
func handleCatalogList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	typ := r.URL.Query().Get("type")

	sonarrKey, radarrKey, _ := services.Keys()

	type arrFetch struct {
		data []byte
		err  error
	}
	radarrCh := make(chan arrFetch, 1)
	sonarrCh := make(chan arrFetch, 1)

	go func() {
		if typ == "series" || radarrKey == "" {
			radarrCh <- arrFetch{}
			return
		}
		body, err := services.ArrGet(radarrURL, radarrKey, "/api/v3/movie")
		radarrCh <- arrFetch{data: body, err: err}
	}()
	go func() {
		if typ == "movie" || sonarrKey == "" {
			sonarrCh <- arrFetch{}
			return
		}
		body, err := services.ArrGet(sonarrURL, sonarrKey, "/api/v3/series")
		sonarrCh <- arrFetch{data: body, err: err}
	}()

	resp := catalogResponse{Movies: []json.RawMessage{}, Series: []json.RawMessage{}}
	if rf := <-radarrCh; rf.err == nil && len(rf.data) > 0 {
		resp.Movies = filterByTitle(rf.data, q)
	}
	if sf := <-sonarrCh; sf.err == nil && len(sf.data) > 0 {
		resp.Series = filterByTitle(sf.data, q)
	}
	httputil.WriteJSON(w, resp)
}

// filterByTitle applies a case-insensitive substring filter to the "title"
// field of a JSON array.
func filterByTitle(data []byte, q string) []json.RawMessage {
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return []json.RawMessage{}
	}
	if q == "" {
		return items
	}
	out := make([]json.RawMessage, 0, len(items))
	for _, raw := range items {
		var probe struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(raw, &probe) == nil {
			if strings.Contains(strings.ToLower(probe.Title), q) {
				out = append(out, raw)
			}
		}
	}
	return out
}

// handleCatalogSeriesDetail proxies Sonarr /api/v3/series/{id}.
func handleCatalogSeriesDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteError(w, "series id required", http.StatusBadRequest)
		return
	}
	sonarrKey, _, _ := services.Keys()
	body, err := services.ArrGet(sonarrURL, sonarrKey, "/api/v3/series/"+url.PathEscape(id))
	if err != nil {
		httputil.WriteError(w, "sonarr unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body) //nolint:errcheck
}

// handleCatalogSeason merges Sonarr episode and episodefile lists.
func handleCatalogSeason(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	seriesID := r.PathValue("id")
	seasonNum := r.PathValue("n")
	if seriesID == "" || seasonNum == "" {
		httputil.WriteError(w, "series id and season required", http.StatusBadRequest)
		return
	}
	sonarrKey, _, _ := services.Keys()

	epData, err := services.ArrGet(sonarrURL, sonarrKey,
		"/api/v3/episode?seriesId="+url.QueryEscape(seriesID)+"&seasonNumber="+url.QueryEscape(seasonNum))
	if err != nil {
		httputil.WriteError(w, "sonarr episode fetch failed", http.StatusBadGateway)
		return
	}
	fileData, err := services.ArrGet(sonarrURL, sonarrKey,
		"/api/v3/episodefile?seriesId="+url.QueryEscape(seriesID))
	if err != nil {
		httputil.WriteError(w, "sonarr episodefile fetch failed", http.StatusBadGateway)
		return
	}

	var files []map[string]any
	_ = json.Unmarshal(fileData, &files)
	byID := map[float64]map[string]any{}
	for _, f := range files {
		if idF, ok := f["id"].(float64); ok {
			byID[idF] = f
		}
	}
	var eps []map[string]any
	_ = json.Unmarshal(epData, &eps)
	for _, e := range eps {
		if fid, ok := e["episodeFileId"].(float64); ok && fid > 0 {
			if file, ok := byID[fid]; ok {
				e["file"] = file
			}
		}
	}
	httputil.WriteJSON(w, eps)
}

// handleCatalogFlags proxies GET /api/procula/catalog/flags unchanged.
func handleCatalogFlags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp, err := services.client.Get(proculaURL + "/api/procula/catalog/flags")
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body) //nolint:errcheck
}

// handleCatalogDetail returns {path, flags, job, synopsis, artwork_url} for a specific media path.
// It fetches the flag row and the newest matching job from procula, plus catalog metadata.
func handleCatalogDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		httputil.WriteError(w, "path required", http.StatusBadRequest)
		return
	}

	type flagsWrap struct {
		Rows []map[string]any `json:"rows"`
	}
	var fw flagsWrap
	if resp, err := services.client.Get(proculaURL + "/api/procula/catalog/flags"); err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		_ = json.Unmarshal(body, &fw)
	}

	flags := []map[string]any{}
	for _, row := range fw.Rows {
		if p, _ := row["path"].(string); p == path {
			if f, ok := row["flags"].([]any); ok {
				for _, item := range f {
					if m, ok := item.(map[string]any); ok {
						flags = append(flags, m)
					}
				}
			}
			break
		}
	}

	var matched map[string]any
	if resp, err := services.client.Get(proculaURL + "/api/procula/jobs"); err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var all []map[string]any
		_ = json.Unmarshal(body, &all)
		for _, j := range all {
			src, _ := j["source"].(map[string]any)
			if src == nil {
				continue
			}
			if p, _ := src["path"].(string); p == path {
				matched = j
			}
		}
	}

	// Resolve synopsis, artwork, and title from the catalog DB.
	// For episodes: walk up episode → season → series to find the item that carries them.
	// Also trigger a background Jellyfin metadata sync so subsequent requests see fresh data.
	synopsis, artworkURL, title, metadataSyncedAt := "", "", "", ""
	inCatalog := false
	if catalogDB != nil {
		if item, err := GetCatalogItemByFilePath(catalogDB, path); err == nil && item != nil {
			inCatalog = true
			synopsis = item.Synopsis
			artworkURL = item.ArtworkURL
			title = item.Title
			metadataSyncedAt = item.MetadataSyncedAt
			if item.Type == "movie" {
				go maybeSyncJellyfinMetadata(item)
			} else if item.Type == "episode" {
				if season, err := GetCatalogItemByID(catalogDB, item.ParentID); err == nil && season != nil {
					if series, err := GetCatalogItemByID(catalogDB, season.ParentID); err == nil && series != nil {
						if synopsis == "" {
							synopsis = series.Synopsis
						}
						if artworkURL == "" {
							artworkURL = series.ArtworkURL
						}
						if metadataSyncedAt == "" {
							metadataSyncedAt = series.MetadataSyncedAt
						}
						go maybeSyncJellyfinMetadata(series)
					}
				}
			}
		}
	}

	httputil.WriteJSON(w, map[string]any{
		"path":               path,
		"flags":              flags,
		"job":                matched,
		"synopsis":           synopsis,
		"artwork_url":        artworkURL,
		"title":              title,
		"in_catalog":         inCatalog,
		"metadata_synced_at": metadataSyncedAt,
	})
}

// handleCatalogItemHistory returns recent job history for a file path.
func handleCatalogItemHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		httputil.WriteError(w, "path required", http.StatusBadRequest)
		return
	}
	resp, err := services.client.Get(proculaURL + "/api/procula/jobs")
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var all []map[string]any
	_ = json.Unmarshal(body, &all)
	var matching []map[string]any
	for _, j := range all {
		src, _ := j["source"].(map[string]any)
		if src == nil {
			continue
		}
		if p, _ := src["path"].(string); p == path {
			matching = append(matching, j)
		}
	}
	httputil.WriteJSON(w, matching)
}

func handleCatalogItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f := CatalogFilter{
		Type:  r.URL.Query().Get("type"),
		Tier:  r.URL.Query().Get("tier"),
		Query: r.URL.Query().Get("q"),
	}
	items, err := ListCatalogItems(catalogDB, f)
	if err != nil {
		slog.Error("list catalog items", "component", "catalog", "error", err)
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	httputil.WriteJSON(w, items)
}

func handleCatalogItemDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteError(w, "missing id", http.StatusBadRequest)
		return
	}
	item, err := GetCatalogItemByID(catalogDB, id)
	if err != nil {
		slog.Error("get catalog item", "component", "catalog", "id", id, "error", err)
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if item == nil {
		httputil.WriteError(w, "not found", http.StatusNotFound)
		return
	}
	go maybeSyncJellyfinMetadata(item)
	httputil.WriteJSON(w, item)
}

func handleCatalogBackfill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	go BackfillFromArr(catalogDB, services)
	httputil.WriteJSON(w, map[string]string{"status": "started"})
}

// handleCatalogCommand proxies force-search and unmonitor commands to Radarr/Sonarr.
// POST /api/pelicula/catalog/command
// Body: {"arr_type":"radarr"|"sonarr","arr_id":N,"command":"search"|"unmonitor"}
func handleCatalogCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ArrType string `json:"arr_type"`
		ArrID   int    `json:"arr_id"`
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ArrID == 0 {
		httputil.WriteError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.ArrType != "radarr" && req.ArrType != "sonarr" {
		httputil.WriteError(w, "invalid arr_type", http.StatusBadRequest)
		return
	}
	sonarrKey, radarrKey, _ := services.Keys()

	switch req.Command {
	case "search":
		if req.ArrType == "radarr" {
			if _, err := services.ArrPost(radarrURL, radarrKey, "/api/v3/command", map[string]any{
				"name": "MoviesSearch", "movieIds": []int{req.ArrID},
			}); err != nil {
				httputil.WriteError(w, "radarr search failed", http.StatusBadGateway)
				return
			}
		} else {
			if _, err := services.ArrPost(sonarrURL, sonarrKey, "/api/v3/command", map[string]any{
				"name": "SeriesSearch", "seriesId": req.ArrID,
			}); err != nil {
				httputil.WriteError(w, "sonarr search failed", http.StatusBadGateway)
				return
			}
		}
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
	case "unmonitor":
		if req.ArrType == "radarr" {
			body, err := services.ArrGet(radarrURL, radarrKey, fmt.Sprintf("/api/v3/movie/%d", req.ArrID))
			if err != nil {
				httputil.WriteError(w, "radarr fetch failed", http.StatusBadGateway)
				return
			}
			var movie map[string]any
			if err := json.Unmarshal(body, &movie); err != nil {
				httputil.WriteError(w, "invalid radarr response", http.StatusBadGateway)
				return
			}
			movie["monitored"] = false
			if _, err := services.ArrPut(radarrURL, radarrKey, fmt.Sprintf("/api/v3/movie/%d", req.ArrID), movie); err != nil {
				httputil.WriteError(w, "radarr update failed", http.StatusBadGateway)
				return
			}
		} else {
			body, err := services.ArrGet(sonarrURL, sonarrKey, fmt.Sprintf("/api/v3/series/%d", req.ArrID))
			if err != nil {
				httputil.WriteError(w, "sonarr fetch failed", http.StatusBadGateway)
				return
			}
			var series map[string]any
			if err := json.Unmarshal(body, &series); err != nil {
				httputil.WriteError(w, "invalid sonarr response", http.StatusBadGateway)
				return
			}
			series["monitored"] = false
			if _, err := services.ArrPut(sonarrURL, sonarrKey, fmt.Sprintf("/api/v3/series/%d", req.ArrID), series); err != nil {
				httputil.WriteError(w, "sonarr update failed", http.StatusBadGateway)
				return
			}
		}
	default:
		httputil.WriteError(w, "unknown command", http.StatusBadRequest)
		return
	}
	httputil.WriteJSON(w, map[string]string{"status": "ok"})
}

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
			blocklistID, _ = findBlocklistID(baseURL, apiKey, req.ArrType, req.ArrID)
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
	sonarrKey, radarrKey, _ := services.Keys()
	if arrType == "radarr" {
		services.ArrDelete(radarrURL, radarrKey, fmt.Sprintf("/api/v3/blocklist/%d", id)) //nolint:errcheck
	} else if arrType == "sonarr" {
		services.ArrDelete(sonarrURL, sonarrKey, fmt.Sprintf("/api/v3/blocklist/%d", id)) //nolint:errcheck
	} else {
		// No arr_type provided: try both, ignore errors.
		services.ArrDelete(sonarrURL, sonarrKey, fmt.Sprintf("/api/v3/blocklist/%d", id)) //nolint:errcheck
		services.ArrDelete(radarrURL, radarrKey, fmt.Sprintf("/api/v3/blocklist/%d", id)) //nolint:errcheck
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
	// Radarr history/movie returns {records:[...]} or an array depending on version.
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
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
func findBlocklistID(baseURL, apiKey, arrType string, arrID int) (int, error) {
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

// handleCatalogQualityProfiles returns quality profile id→name maps for Radarr and Sonarr.
// GET /api/pelicula/catalog/qualityprofiles
// Response: {"radarr":{"1":"HD-1080p",...},"sonarr":{"4":"HD TV",...}}
func handleCatalogQualityProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sonarrKey, radarrKey, _ := services.Keys()

	type fetch struct {
		data []byte
		err  error
	}
	rCh := make(chan fetch, 1)
	sCh := make(chan fetch, 1)
	go func() {
		body, err := services.ArrGet(radarrURL, radarrKey, "/api/v3/qualityprofile")
		rCh <- fetch{body, err}
	}()
	go func() {
		body, err := services.ArrGet(sonarrURL, sonarrKey, "/api/v3/qualityprofile")
		sCh <- fetch{body, err}
	}()

	buildMap := func(data []byte) map[string]string {
		var profiles []map[string]any
		m := map[string]string{}
		if json.Unmarshal(data, &profiles) != nil {
			return m
		}
		for _, p := range profiles {
			if id, ok := p["id"].(float64); ok {
				if name, ok := p["name"].(string); ok {
					m[fmt.Sprintf("%.0f", id)] = name
				}
			}
		}
		return m
	}

	radarrMap := map[string]string{}
	sonarrMap := map[string]string{}
	if rr := <-rCh; rr.err == nil {
		radarrMap = buildMap(rr.data)
	}
	if sr := <-sCh; sr.err == nil {
		sonarrMap = buildMap(sr.data)
	}

	httputil.WriteJSON(w, map[string]any{
		"radarr": radarrMap,
		"sonarr": sonarrMap,
	})
}
