// catalog.go — thin Radarr/Sonarr proxies powering the dashboard Catalog tab.
package main

import (
	"encoding/json"
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

	// Resolve synopsis and artwork from the catalog DB.
	// For episodes: walk up episode → season → series to find the item that carries them.
	synopsis, artworkURL := "", ""
	if catalogDB != nil {
		if item, err := GetCatalogItemByFilePath(catalogDB, path); err == nil && item != nil {
			synopsis = item.Synopsis
			artworkURL = item.ArtworkURL
			if synopsis == "" && artworkURL == "" && item.Type == "episode" {
				if season, err := GetCatalogItemByID(catalogDB, item.ParentID); err == nil && season != nil {
					if series, err := GetCatalogItemByID(catalogDB, season.ParentID); err == nil && series != nil {
						synopsis = series.Synopsis
						artworkURL = series.ArtworkURL
					}
				}
			}
		}
	}

	httputil.WriteJSON(w, map[string]any{
		"path":        path,
		"flags":       flags,
		"job":         matched,
		"synopsis":    synopsis,
		"artwork_url": artworkURL,
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
