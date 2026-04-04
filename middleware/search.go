package main

import (
	"encoding/json"
	"fmt"
	"log"
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

	// Search Radarr (movies)
	if typeFilter != "series" {
	wg.Add(1)
	go func() {
		defer wg.Done()
		data, err := services.ArrGet(radarrURL, services.RadarrKey, "/api/v3/movie/lookup?term="+encoded)
		if err != nil {
			log.Printf("[search] radarr error: %v", err)
			return
		}

		// Get existing movies to check "added" status
		existingData, _ := services.ArrGet(radarrURL, services.RadarrKey, "/api/v3/movie")
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
		data, err := services.ArrGet(sonarrURL, services.SonarrKey, "/api/v3/series/lookup?term="+encoded)
		if err != nil {
			log.Printf("[search] sonarr error: %v", err)
			return
		}

		existingData, _ := services.ArrGet(sonarrURL, services.SonarrKey, "/api/v3/series")
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
		addMovie(w, req.TmdbID)
	case "series":
		addSeries(w, req.TvdbID)
	default:
		writeError(w, "type must be 'movie' or 'series'", http.StatusBadRequest)
	}
}

func addMovie(w http.ResponseWriter, tmdbID int) {
	// Look up movie details first
	data, err := services.ArrGet(radarrURL, services.RadarrKey, "/api/v3/movie/lookup/tmdb?tmdbId="+itoa(tmdbID))
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
	profData, err := services.ArrGet(radarrURL, services.RadarrKey, "/api/v3/qualityprofile")
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

	_, err = services.ArrPost(radarrURL, services.RadarrKey, "/api/v3/movie", payload)
	if err != nil {
		writeError(w, "failed to add movie: "+err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, map[string]string{"status": "added"})
}

func addSeries(w http.ResponseWriter, tvdbID int) {
	// Look up series details
	data, err := services.ArrGet(sonarrURL, services.SonarrKey, "/api/v3/series/lookup?term=tvdb:"+itoa(tvdbID))
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
	profData, err := services.ArrGet(sonarrURL, services.SonarrKey, "/api/v3/qualityprofile")
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

	_, err = services.ArrPost(sonarrURL, services.SonarrKey, "/api/v3/series", payload)
	if err != nil {
		writeError(w, "failed to add series: "+err.Error(), http.StatusBadGateway)
		return
	}

	writeJSON(w, map[string]string{"status": "added"})
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}
