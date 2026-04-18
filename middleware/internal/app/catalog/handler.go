// handler.go — HTTP handlers for the catalog tab, moved from cmd/pelicula-api/catalog.go.
package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"pelicula-api/httputil"
)

// ProxyClient is the subset of an HTTP client needed by the catalog handler
// to make outbound requests to Procula.
type ProxyClient interface {
	Get(url string) (*http.Response, error)
}

// jellyfinCacheState is a short-lived in-process cache of Jellyfin library items.
type jellyfinCacheState struct {
	mu        sync.Mutex
	items     []jellyfinItem
	fetchedAt time.Time
}

// Handler holds the dependencies for catalog HTTP handlers.
// No package-level globals — wire this from main() and call the Handle* methods.
type Handler struct {
	DB         *sql.DB
	Arr        ArrClient
	Jf         JellyfinMetaClient
	Client     ProxyClient // outbound HTTP client (for Procula calls)
	ProculaURL string
	RadarrURL  string
	SonarrURL  string
	jfCache    jellyfinCacheState
}

type catalogResponse struct {
	Movies []json.RawMessage `json:"movies"`
	Series []json.RawMessage `json:"series"`
}

// HandleCatalogList returns movies and series from Radarr+Sonarr in parallel.
func (h *Handler) HandleCatalogList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	typ := r.URL.Query().Get("type")

	sonarrKey, radarrKey, _ := h.Arr.Keys()

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
		body, err := h.Arr.ArrGet(h.RadarrURL, radarrKey, "/api/v3/movie")
		radarrCh <- arrFetch{data: body, err: err}
	}()
	go func() {
		if typ == "movie" || sonarrKey == "" {
			sonarrCh <- arrFetch{}
			return
		}
		body, err := h.Arr.ArrGet(h.SonarrURL, sonarrKey, "/api/v3/series")
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

// HandleCatalogSeriesDetail proxies Sonarr /api/v3/series/{id}.
func (h *Handler) HandleCatalogSeriesDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteError(w, "series id required", http.StatusBadRequest)
		return
	}
	sonarrKey, _, _ := h.Arr.Keys()
	body, err := h.Arr.ArrGet(h.SonarrURL, sonarrKey, "/api/v3/series/"+url.PathEscape(id))
	if err != nil {
		httputil.WriteError(w, "sonarr unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body) //nolint:errcheck
}

// HandleCatalogSeason merges Sonarr episode and episodefile lists.
func (h *Handler) HandleCatalogSeason(w http.ResponseWriter, r *http.Request) {
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
	sonarrKey, _, _ := h.Arr.Keys()

	epData, err := h.Arr.ArrGet(h.SonarrURL, sonarrKey,
		"/api/v3/episode?seriesId="+url.QueryEscape(seriesID)+"&seasonNumber="+url.QueryEscape(seasonNum))
	if err != nil {
		httputil.WriteError(w, "sonarr episode fetch failed", http.StatusBadGateway)
		return
	}
	fileData, err := h.Arr.ArrGet(h.SonarrURL, sonarrKey,
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

// HandleCatalogFlags proxies GET /api/procula/catalog/flags unchanged.
func (h *Handler) HandleCatalogFlags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp, err := h.Client.Get(h.ProculaURL + "/api/procula/catalog/flags")
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

// HandleCatalogDetail returns {path, flags, job, synopsis, artwork_url} for a specific media path.
func (h *Handler) HandleCatalogDetail(w http.ResponseWriter, r *http.Request) {
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
	if resp, err := h.Client.Get(h.ProculaURL + "/api/procula/catalog/flags"); err == nil {
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
	if resp, err := h.Client.Get(h.ProculaURL + "/api/procula/jobs"); err == nil {
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

	synopsis, artworkURL, title, metadataSyncedAt := "", "", "", ""
	inCatalog := false
	if h.DB != nil {
		if item, err := GetCatalogItemByFilePath(r.Context(), h.DB, path); err == nil && item != nil {
			inCatalog = true
			synopsis = item.Synopsis
			artworkURL = item.ArtworkURL
			title = item.Title
			metadataSyncedAt = item.MetadataSyncedAt
			if item.Type == "movie" {
				go h.MaybeSyncJellyfinMetadata(item)
			} else if item.Type == "episode" {
				if season, err := GetCatalogItemByID(r.Context(), h.DB, item.ParentID); err == nil && season != nil {
					if series, err := GetCatalogItemByID(r.Context(), h.DB, season.ParentID); err == nil && series != nil {
						if synopsis == "" {
							synopsis = series.Synopsis
						}
						if artworkURL == "" {
							artworkURL = series.ArtworkURL
						}
						if metadataSyncedAt == "" {
							metadataSyncedAt = series.MetadataSyncedAt
						}
						go h.MaybeSyncJellyfinMetadata(series)
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

// HandleCatalogItemHistory returns recent job history for a file path.
func (h *Handler) HandleCatalogItemHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		httputil.WriteError(w, "path required", http.StatusBadRequest)
		return
	}
	resp, err := h.Client.Get(h.ProculaURL + "/api/procula/jobs")
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

// HandleCatalogItems lists catalog items with optional type/tier/query filters.
func (h *Handler) HandleCatalogItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f := CatalogFilter{
		Type:  r.URL.Query().Get("type"),
		Tier:  r.URL.Query().Get("tier"),
		Query: r.URL.Query().Get("q"),
	}
	items, err := ListCatalogItems(r.Context(), h.DB, f)
	if err != nil {
		slog.Error("list catalog items", "component", "catalog", "error", err)
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	httputil.WriteJSON(w, items)
}

// HandleCatalogItemDetail returns a single catalog item by ID.
func (h *Handler) HandleCatalogItemDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteError(w, "missing id", http.StatusBadRequest)
		return
	}
	item, err := GetCatalogItemByID(r.Context(), h.DB, id)
	if err != nil {
		slog.Error("get catalog item", "component", "catalog", "id", id, "error", err)
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if item == nil {
		httputil.WriteError(w, "not found", http.StatusNotFound)
		return
	}
	go h.MaybeSyncJellyfinMetadata(item)
	httputil.WriteJSON(w, item)
}

// HandleCatalogBackfill triggers a background backfill from Radarr+Sonarr.
func (h *Handler) HandleCatalogBackfill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	go BackfillFromArr(context.Background(), h.DB, h.Arr, h.RadarrURL, h.SonarrURL) //nolint:errcheck
	httputil.WriteJSON(w, map[string]string{"status": "started"})
}

// arrTarget captures the per-arr-type parameters used by HandleCatalogCommand.
type arrTarget struct {
	baseURL      string
	apiKey       string
	itemPath     string
	searchCmd    string
	searchIDKey  string
	searchIDList bool
	rescanCmd    string
	rescanIDKey  string
}

// HandleCatalogCommand proxies force-search and unmonitor commands to Radarr/Sonarr.
// POST /api/pelicula/catalog/command
// Body: {"arr_type":"radarr"|"sonarr","arr_id":N,"command":"search"|"unmonitor"|"rescan"}
func (h *Handler) HandleCatalogCommand(w http.ResponseWriter, r *http.Request) {
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
	sonarrKey, radarrKey, _ := h.Arr.Keys()

	targets := map[string]arrTarget{
		"radarr": {
			baseURL:      h.RadarrURL,
			apiKey:       radarrKey,
			itemPath:     "/api/v3/movie",
			searchCmd:    "MoviesSearch",
			searchIDKey:  "movieIds",
			searchIDList: true,
			rescanCmd:    "RescanMovie",
			rescanIDKey:  "movieId",
		},
		"sonarr": {
			baseURL:      h.SonarrURL,
			apiKey:       sonarrKey,
			itemPath:     "/api/v3/series",
			searchCmd:    "SeriesSearch",
			searchIDKey:  "seriesId",
			searchIDList: false,
			rescanCmd:    "RescanSeries",
			rescanIDKey:  "seriesId",
		},
	}
	t := targets[req.ArrType]

	switch req.Command {
	case "search":
		var searchID any
		if t.searchIDList {
			searchID = []int{req.ArrID}
		} else {
			searchID = req.ArrID
		}
		if _, err := h.Arr.ArrPost(t.baseURL, t.apiKey, "/api/v3/command", map[string]any{
			"name":        t.searchCmd,
			t.searchIDKey: searchID,
		}); err != nil {
			httputil.WriteError(w, req.ArrType+" search failed", http.StatusBadGateway)
			return
		}
	case "rescan":
		if _, err := h.Arr.ArrPost(t.baseURL, t.apiKey, "/api/v3/command", map[string]any{
			"name":        t.rescanCmd,
			t.rescanIDKey: req.ArrID,
		}); err != nil {
			httputil.WriteError(w, req.ArrType+" rescan failed", http.StatusBadGateway)
			return
		}
	case "unmonitor":
		itemURL := fmt.Sprintf("%s/%d", t.itemPath, req.ArrID)
		body, err := h.Arr.ArrGet(t.baseURL, t.apiKey, itemURL)
		if err != nil {
			httputil.WriteError(w, req.ArrType+" fetch failed", http.StatusBadGateway)
			return
		}
		var item map[string]any
		if err := json.Unmarshal(body, &item); err != nil {
			httputil.WriteError(w, "invalid "+req.ArrType+" response", http.StatusBadGateway)
			return
		}
		item["monitored"] = false
		if _, err := h.Arr.ArrPut(t.baseURL, t.apiKey, itemURL, item); err != nil {
			httputil.WriteError(w, req.ArrType+" update failed", http.StatusBadGateway)
			return
		}
	default:
		httputil.WriteError(w, "unknown command", http.StatusBadRequest)
		return
	}
	httputil.WriteJSON(w, map[string]string{"status": "ok"})
}

// HandleCatalogReplace marks a release as failed (blocklisting it), then triggers
// a rescan and fresh search.
// POST /api/pelicula/catalog/replace
// Body: {"arr_type":"sonarr"|"radarr","arr_id":N,"episode_id":N,"path":"/tv/..."}
func (h *Handler) HandleCatalogReplace(w http.ResponseWriter, r *http.Request) {
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

	sonarrKey, radarrKey, _ := h.Arr.Keys()
	var baseURL, apiKey string
	if req.ArrType == "radarr" {
		baseURL, apiKey = h.RadarrURL, radarrKey
	} else {
		baseURL, apiKey = h.SonarrURL, sonarrKey
	}

	historyID, displayTitle, err := h.findImportHistoryID(baseURL, apiKey, req.ArrType, req.ArrID, req.EpisodeID)
	if err != nil {
		slog.Warn("replace: history lookup failed", "arr_type", req.ArrType, "arr_id", req.ArrID, "error", err)
		historyID = 0
	}

	blocklistID := 0
	if historyID > 0 {
		if _, err := h.Arr.ArrPost(baseURL, apiKey,
			fmt.Sprintf("/api/v3/history/failed/%d", historyID), nil); err != nil {
			slog.Warn("replace: history/failed call failed", "history_id", historyID, "error", err)
		} else {
			blocklistID, _ = h.findBlocklistID(baseURL, apiKey, req.ArrType, req.ArrID)
		}
	}

	var rescanCmd map[string]any
	if req.ArrType == "radarr" {
		rescanCmd = map[string]any{"name": "RescanMovie", "movieId": req.ArrID}
	} else {
		rescanCmd = map[string]any{"name": "RescanSeries", "seriesId": req.ArrID}
	}
	if _, err := h.Arr.ArrPost(baseURL, apiKey, "/api/v3/command", rescanCmd); err != nil {
		slog.Warn("replace: rescan command failed", "arr_type", req.ArrType, "error", err)
	}

	var searchCmd map[string]any
	if req.ArrType == "radarr" {
		searchCmd = map[string]any{"name": "MoviesSearch", "movieIds": []int{req.ArrID}}
	} else if req.EpisodeID > 0 {
		searchCmd = map[string]any{"name": "EpisodeSearch", "episodeIds": []int{req.EpisodeID}}
	} else {
		searchCmd = map[string]any{"name": "SeriesSearch", "seriesId": req.ArrID}
	}
	if _, err := h.Arr.ArrPost(baseURL, apiKey, "/api/v3/command", searchCmd); err != nil {
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

// HandleCatalogUnblocklist removes an entry from the *arr blocklist.
// DELETE /api/pelicula/catalog/blocklist/{id}
func (h *Handler) HandleCatalogUnblocklist(w http.ResponseWriter, r *http.Request) {
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
	sonarrKey, radarrKey, _ := h.Arr.Keys()
	if arrType == "radarr" {
		h.Arr.ArrDelete(h.RadarrURL, radarrKey, fmt.Sprintf("/api/v3/blocklist/%d", id)) //nolint:errcheck
	} else if arrType == "sonarr" {
		h.Arr.ArrDelete(h.SonarrURL, sonarrKey, fmt.Sprintf("/api/v3/blocklist/%d", id)) //nolint:errcheck
	} else {
		h.Arr.ArrDelete(h.SonarrURL, sonarrKey, fmt.Sprintf("/api/v3/blocklist/%d", id)) //nolint:errcheck
		h.Arr.ArrDelete(h.RadarrURL, radarrKey, fmt.Sprintf("/api/v3/blocklist/%d", id)) //nolint:errcheck
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleCatalogQualityProfiles returns quality profile id→name maps for Radarr and Sonarr.
// GET /api/pelicula/catalog/qualityprofiles
// Response: {"radarr":{"1":"HD-1080p",...},"sonarr":{"4":"HD TV",...}}
func (h *Handler) HandleCatalogQualityProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sonarrKey, radarrKey, _ := h.Arr.Keys()

	type fetch struct {
		data []byte
		err  error
	}
	rCh := make(chan fetch, 1)
	sCh := make(chan fetch, 1)
	go func() {
		body, err := h.Arr.ArrGet(h.RadarrURL, radarrKey, "/api/v3/qualityprofile")
		rCh <- fetch{body, err}
	}()
	go func() {
		body, err := h.Arr.ArrGet(h.SonarrURL, sonarrKey, "/api/v3/qualityprofile")
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

// findImportHistoryID queries *arr history for an episode/movie and returns the
// historyId of the most recent downloadFolderImported event, plus the source title.
func (h *Handler) findImportHistoryID(baseURL, apiKey, arrType string, arrID, episodeID int) (int, string, error) {
	var path string
	if arrType == "sonarr" {
		if episodeID == 0 {
			return 0, "", fmt.Errorf("episode_id required for sonarr history lookup")
		}
		path = fmt.Sprintf("/api/v3/history/episode?episodeId=%d&eventType=downloadFolderImported&sortKey=date&sortDirection=descending", episodeID)
	} else {
		path = fmt.Sprintf("/api/v3/history/movie?movieId=%d&eventType=downloadFolderImported&sortKey=date&sortDirection=descending", arrID)
	}
	data, err := h.Arr.ArrGet(baseURL, apiKey, path)
	if err != nil {
		return 0, "", err
	}

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
		title, _ := rec["sourceTitle"].(string)
		return id, title, nil
	}
	return 0, "", fmt.Errorf("no import history found")
}

// findBlocklistID queries the *arr blocklist to find the most recently added
// entry for the given item. Returns 0 if not found (non-fatal).
func (h *Handler) findBlocklistID(baseURL, apiKey, arrType string, arrID int) (int, error) {
	data, err := h.Arr.ArrGet(baseURL, apiKey,
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
