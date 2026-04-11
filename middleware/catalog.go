// catalog.go — thin Radarr/Sonarr proxies powering the dashboard Catalog tab.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type catalogResponse struct {
	Movies []json.RawMessage `json:"movies"`
	Series []json.RawMessage `json:"series"`
}

// handleCatalogList returns movies and series from Radarr+Sonarr in parallel.
func handleCatalogList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
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
	writeJSON(w, resp)
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
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, "series id required", http.StatusBadRequest)
		return
	}
	sonarrKey, _, _ := services.Keys()
	body, err := services.ArrGet(sonarrURL, sonarrKey, "/api/v3/series/"+url.PathEscape(id))
	if err != nil {
		writeError(w, "sonarr unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body) //nolint:errcheck
}

// handleCatalogSeason merges Sonarr episode and episodefile lists.
func handleCatalogSeason(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	seriesID := r.PathValue("id")
	seasonNum := r.PathValue("n")
	if seriesID == "" || seasonNum == "" {
		writeError(w, "series id and season required", http.StatusBadRequest)
		return
	}
	sonarrKey, _, _ := services.Keys()

	epData, err := services.ArrGet(sonarrURL, sonarrKey,
		"/api/v3/episode?seriesId="+url.QueryEscape(seriesID)+"&seasonNumber="+url.QueryEscape(seasonNum))
	if err != nil {
		writeError(w, "sonarr episode fetch failed", http.StatusBadGateway)
		return
	}
	fileData, err := services.ArrGet(sonarrURL, sonarrKey,
		"/api/v3/episodefile?seriesId="+url.QueryEscape(seriesID))
	if err != nil {
		writeError(w, "sonarr episodefile fetch failed", http.StatusBadGateway)
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
	writeJSON(w, eps)
}

// handleCatalogItemHistory returns recent job history for a file path.
func handleCatalogItemHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, "path required", http.StatusBadRequest)
		return
	}
	resp, err := services.client.Get(proculaURL + "/api/procula/jobs")
	if err != nil {
		writeError(w, "procula unavailable", http.StatusBadGateway)
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
	writeJSON(w, matching)
}
