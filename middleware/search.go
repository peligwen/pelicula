package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
)

type SearchResult struct {
	Type     string `json:"type"`     // "movie" or "series"
	Title    string `json:"title"`
	Year     int    `json:"year"`
	Overview string `json:"overview"`
	Poster   string `json:"poster"`
	TmdbID   int    `json:"tmdbId,omitempty"`
	TvdbID   int    `json:"tvdbId,omitempty"`
	Added    bool   `json:"added"`
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, map[string]any{"results": []SearchResult{}})
		return
	}

	typeFilter := r.URL.Query().Get("type") // "movie", "series", or "" for both

	encoded := url.QueryEscape(q)
	var movies, series []SearchResult
	var mu sync.Mutex
	var wg sync.WaitGroup

	sonarrKey, radarrKey, _ := services.Keys()

	// Search Radarr (movies)
	if typeFilter != "series" {
	wg.Add(1)
	go func() {
		defer wg.Done()
		data, err := services.ArrGet(radarrURL, radarrKey, "/api/v3/movie/lookup?term="+encoded)
		if err != nil {
			slog.Error("radarr search error", "component", "search", "error", err)
			return
		}

		// Get existing movies to check "added" status
		existingData, _ := services.ArrGet(radarrURL, radarrKey, "/api/v3/movie")
		existingIDs := make(map[int]bool)
		var existing []map[string]any
		if json.Unmarshal(existingData, &existing) == nil {
			for _, m := range existing {
				if id, ok := m["tmdbId"].(float64); ok {
					existingIDs[int(id)] = true
				}
			}
		}

		var rawMovies []map[string]any
		if json.Unmarshal(data, &rawMovies) != nil {
			return
		}

		mu.Lock()
		for _, m := range rawMovies {
			tmdbID := int(floatVal(m, "tmdbId"))
			poster := ""
			if images, ok := m["images"].([]any); ok {
				for _, img := range images {
					if imgMap, ok := img.(map[string]any); ok {
						if imgMap["coverType"] == "poster" {
							poster = strVal(imgMap, "remoteUrl")
							break
						}
					}
				}
			}
			movies = append(movies, SearchResult{
				Type:     "movie",
				Title:    strVal(m, "title"),
				Year:     int(floatVal(m, "year")),
				Overview: strVal(m, "overview"),
				Poster:   poster,
				TmdbID:   tmdbID,
				Added:    existingIDs[tmdbID],
			})
		}
		mu.Unlock()
	}()
	}

	// Search Sonarr (series)
	if typeFilter != "movie" {
	wg.Add(1)
	go func() {
		defer wg.Done()
		data, err := services.ArrGet(sonarrURL, sonarrKey, "/api/v3/series/lookup?term="+encoded)
		if err != nil {
			slog.Error("sonarr search error", "component", "search", "error", err)
			return
		}

		existingData, _ := services.ArrGet(sonarrURL, sonarrKey, "/api/v3/series")
		existingIDs := make(map[int]bool)
		var existing []map[string]any
		if json.Unmarshal(existingData, &existing) == nil {
			for _, s := range existing {
				if id, ok := s["tvdbId"].(float64); ok {
					existingIDs[int(id)] = true
				}
			}
		}

		var shows []map[string]any
		if json.Unmarshal(data, &shows) != nil {
			return
		}

		mu.Lock()
		for _, s := range shows {
			tvdbID := int(floatVal(s, "tvdbId"))
			tmdbID := int(floatVal(s, "tmdbId")) // present in Sonarr for many shows
			poster := ""
			if images, ok := s["images"].([]any); ok {
				for _, img := range images {
					if imgMap, ok := img.(map[string]any); ok {
						if imgMap["coverType"] == "poster" {
							poster = strVal(imgMap, "remoteUrl")
							break
						}
					}
				}
			}
			series = append(series, SearchResult{
				Type:     "series",
				Title:    strVal(s, "title"),
				Year:     int(floatVal(s, "year")),
				Overview: strVal(s, "overview"),
				Poster:   poster,
				TvdbID:   tvdbID,
				TmdbID:   tmdbID,
				Added:    existingIDs[tvdbID],
			})
		}
		mu.Unlock()
	}()
	}

	wg.Wait()

	// Interleave movies and series so both types appear in top results
	results := make([]SearchResult, 0, len(movies)+len(series))
	mi, si := 0, 0
	for mi < len(movies) || si < len(series) {
		if si < len(series) {
			results = append(results, series[si])
			si++
		}
		if mi < len(movies) {
			results = append(results, movies[mi])
			mi++
		}
	}

	writeJSON(w, map[string]any{"results": results})
}

func handleSearchAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Type   string `json:"type"`
		TmdbID int    `json:"tmdbId"`
		TvdbID int    `json:"tvdbId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	switch req.Type {
	case "movie":
		arrID, err := addMovieInternal(req.TmdbID, 0, "")
		if err != nil {
			writeError(w, "failed to add movie: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"status": "added", "arr_id": arrID})
	case "series":
		arrID, err := addSeriesInternal(req.TvdbID, 0, "")
		if err != nil {
			writeError(w, "failed to add series: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"status": "added", "arr_id": arrID})
	default:
		writeError(w, "type must be 'movie' or 'series'", http.StatusBadRequest)
	}
}

// addMovieInternal adds a movie to Radarr and returns the Radarr internal ID.
// If profileID is 0 the first available quality profile is used.
// If rootPath is "" the default "/movies" path is used.
func addMovieInternal(tmdbID, profileID int, rootPath string) (int, error) {
	_, radarrKey, _ := services.Keys()
	data, err := services.ArrGet(radarrURL, radarrKey, "/api/v3/movie/lookup/tmdb?tmdbId="+itoa(tmdbID))
	if err != nil {
		return 0, fmt.Errorf("look up movie: %w", err)
	}
	var movie map[string]any
	if err := json.Unmarshal(data, &movie); err != nil {
		return 0, fmt.Errorf("parse movie data: %w", err)
	}

	if profileID == 0 {
		profData, err := services.ArrGet(radarrURL, radarrKey, "/api/v3/qualityprofile")
		if err == nil {
			var profiles []map[string]any
			if json.Unmarshal(profData, &profiles) == nil && len(profiles) > 0 {
				if id, ok := profiles[0]["id"].(float64); ok {
					profileID = int(id)
				}
			}
		}
		if profileID == 0 {
			profileID = 1
		}
	}
	if rootPath == "" {
		rootPath = "/movies"
	}

	payload := map[string]any{
		"tmdbId":           tmdbID,
		"title":            movie["title"],
		"qualityProfileId": profileID,
		"rootFolderPath":   rootPath,
		"monitored":        true,
		"addOptions": map[string]any{
			"searchForMovie": true,
		},
	}
	resp, err := services.ArrPost(radarrURL, radarrKey, "/api/v3/movie", payload)
	if err != nil {
		return 0, fmt.Errorf("add movie: %w", err)
	}
	var added map[string]any
	json.Unmarshal(resp, &added)
	return int(floatVal(added, "id")), nil
}

// addSeriesInternal adds a series to Sonarr and returns the Sonarr internal ID.
// If profileID is 0 the first available quality profile is used.
// If rootPath is "" the default "/tv" path is used.
func addSeriesInternal(tvdbID, profileID int, rootPath string) (int, error) {
	sonarrKey, _, _ := services.Keys()
	data, err := services.ArrGet(sonarrURL, sonarrKey, "/api/v3/series/lookup?term=tvdb:"+itoa(tvdbID))
	if err != nil {
		return 0, fmt.Errorf("look up series: %w", err)
	}
	var shows []map[string]any
	if err := json.Unmarshal(data, &shows); err != nil || len(shows) == 0 {
		return 0, fmt.Errorf("series not found")
	}
	show := shows[0]

	if profileID == 0 {
		profData, err := services.ArrGet(sonarrURL, sonarrKey, "/api/v3/qualityprofile")
		if err == nil {
			var profiles []map[string]any
			if json.Unmarshal(profData, &profiles) == nil && len(profiles) > 0 {
				if id, ok := profiles[0]["id"].(float64); ok {
					profileID = int(id)
				}
			}
		}
		if profileID == 0 {
			profileID = 1
		}
	}
	if rootPath == "" {
		rootPath = "/tv"
	}

	payload := map[string]any{
		"tvdbId":           tvdbID,
		"title":            show["title"],
		"qualityProfileId": profileID,
		"rootFolderPath":   rootPath,
		"monitored":        true,
		"seasonFolder":     true,
		"addOptions": map[string]any{
			"searchForMissingEpisodes": true,
		},
	}
	resp, err := services.ArrPost(sonarrURL, sonarrKey, "/api/v3/series", payload)
	if err != nil {
		return 0, fmt.Errorf("add series: %w", err)
	}
	var added map[string]any
	json.Unmarshal(resp, &added)
	return int(floatVal(added, "id")), nil
}

// handleArrMeta returns quality profiles and root folders from Radarr and Sonarr.
// Used by the admin settings UI to populate request profile dropdowns.
func handleArrMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sonarrKey, radarrKey, _ := services.Keys()

	type profileEntry struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	type rootEntry struct {
		Path string `json:"path"`
	}
	type arrMeta struct {
		QualityProfiles []profileEntry `json:"qualityProfiles"`
		RootFolders     []rootEntry    `json:"rootFolders"`
	}

	fetchProfiles := func(baseURL, apiKey string) []profileEntry {
		data, err := services.ArrGet(baseURL, apiKey, "/api/v3/qualityprofile")
		if err != nil {
			return nil
		}
		var raw []map[string]any
		if json.Unmarshal(data, &raw) != nil {
			return nil
		}
		var out []profileEntry
		for _, p := range raw {
			out = append(out, profileEntry{
				ID:   int(floatVal(p, "id")),
				Name: strVal(p, "name"),
			})
		}
		return out
	}
	fetchRoots := func(baseURL, apiKey string) []rootEntry {
		data, err := services.ArrGet(baseURL, apiKey, "/api/v3/rootfolder")
		if err != nil {
			return nil
		}
		var raw []map[string]any
		if json.Unmarshal(data, &raw) != nil {
			return nil
		}
		var out []rootEntry
		for _, f := range raw {
			out = append(out, rootEntry{Path: strVal(f, "path")})
		}
		return out
	}

	writeJSON(w, map[string]any{
		"radarr": arrMeta{
			QualityProfiles: fetchProfiles(radarrURL, radarrKey),
			RootFolders:     fetchRoots(radarrURL, radarrKey),
		},
		"sonarr": arrMeta{
			QualityProfiles: fetchProfiles(sonarrURL, sonarrKey),
			RootFolders:     fetchRoots(sonarrURL, sonarrKey),
		},
	})
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}
