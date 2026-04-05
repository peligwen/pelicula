package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
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
			tmdbID := int(floatVal(s, "tmdbId")) // present in Sonarr for many shows; used for Jellyseerr routing
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

	// When Jellyseerr is enabled and configured, route add requests through it.
	// This gives request tracking, approval workflow, and per-user attribution.
	// Falls back to direct *arr if Jellyseerr has no key yet or TMDB ID is unavailable.
	if os.Getenv("JELLYSEERR_ENABLED") == "true" {
		services.mu.RLock()
		jsKey := services.JellyseerrKey
		services.mu.RUnlock()
		if jsKey != "" && req.TmdbID != 0 {
			requestViaJellyseerr(w, req.Type, req.TmdbID, jsKey)
			return
		}
	}

	switch req.Type {
	case "movie":
		addMovie(w, req.TmdbID)
	case "series":
		addSeries(w, req.TvdbID)
	default:
		writeError(w, "type must be 'movie' or 'series'", http.StatusBadRequest)
	}
}

func requestViaJellyseerr(w http.ResponseWriter, mediaType string, tmdbID int, apiKey string) {
	jType := "movie"
	if mediaType == "series" {
		jType = "tv"
	}
	payload, _ := json.Marshal(map[string]any{
		"mediaType": jType,
		"mediaId":   tmdbID,
	})
	req, err := http.NewRequest("POST", "http://jellyseerr:5055/api/v1/request",
		bytes.NewReader(payload))
	if err != nil {
		writeError(w, "request build failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeError(w, "Jellyseerr unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		writeError(w, fmt.Sprintf("Jellyseerr returned HTTP %d", resp.StatusCode), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"status": "requested"})
}

func addMovie(w http.ResponseWriter, tmdbID int) {
	_, radarrKey, _ := services.Keys()
	// Look up movie details first
	data, err := services.ArrGet(radarrURL, radarrKey, "/api/v3/movie/lookup/tmdb?tmdbId="+itoa(tmdbID))
	if err != nil {
		writeError(w, "failed to look up movie: "+err.Error(), http.StatusBadGateway)
		return
	}

	var movie map[string]any
	if err := json.Unmarshal(data, &movie); err != nil {
		writeError(w, "failed to parse movie data", http.StatusInternalServerError)
		return
	}

	// Get first quality profile
	profileID := 1
	profData, err := services.ArrGet(radarrURL, radarrKey, "/api/v3/qualityprofile")
	if err == nil {
		var profiles []map[string]any
		if json.Unmarshal(profData, &profiles) == nil && len(profiles) > 0 {
			if id, ok := profiles[0]["id"].(float64); ok {
				profileID = int(id)
			}
		}
	}

	payload := map[string]any{
		"tmdbId":           tmdbID,
		"title":            movie["title"],
		"qualityProfileId": profileID,
		"rootFolderPath":   "/movies",
		"monitored":        true,
		"addOptions": map[string]any{
			"searchForMovie": true,
		},
	}

	_, err = services.ArrPost(radarrURL, radarrKey, "/api/v3/movie", payload)
	if err != nil {
		writeError(w, "failed to add movie: "+err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, map[string]string{"status": "added"})
}

func addSeries(w http.ResponseWriter, tvdbID int) {
	sonarrKey, _, _ := services.Keys()
	// Look up series details
	data, err := services.ArrGet(sonarrURL, sonarrKey, "/api/v3/series/lookup?term=tvdb:"+itoa(tvdbID))
	if err != nil {
		writeError(w, "failed to look up series: "+err.Error(), http.StatusBadGateway)
		return
	}

	var shows []map[string]any
	if err := json.Unmarshal(data, &shows); err != nil || len(shows) == 0 {
		writeError(w, "series not found", http.StatusNotFound)
		return
	}
	show := shows[0]

	// Get first quality profile
	profileID := 1
	profData, err := services.ArrGet(sonarrURL, sonarrKey, "/api/v3/qualityprofile")
	if err == nil {
		var profiles []map[string]any
		if json.Unmarshal(profData, &profiles) == nil && len(profiles) > 0 {
			if id, ok := profiles[0]["id"].(float64); ok {
				profileID = int(id)
			}
		}
	}

	payload := map[string]any{
		"tvdbId":           tvdbID,
		"title":            show["title"],
		"qualityProfileId": profileID,
		"rootFolderPath":   "/tv",
		"monitored":        true,
		"seasonFolder":     true,
		"addOptions": map[string]any{
			"searchForMissingEpisodes": true,
		},
	}

	_, err = services.ArrPost(sonarrURL, sonarrKey, "/api/v3/series", payload)
	if err != nil {
		writeError(w, "failed to add series: "+err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, map[string]string{"status": "added"})
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}
