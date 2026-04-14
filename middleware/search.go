package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"pelicula-api/clients"
	"pelicula-api/httputil"
	"sync"
)

type SearchResult struct {
	Type     string `json:"type"` // "movie" or "series"
	Title    string `json:"title"`
	Year     int    `json:"year"`
	Overview string `json:"overview"`
	Poster   string `json:"poster"`
	TmdbID   int    `json:"tmdbId,omitempty"`
	TvdbID   int    `json:"tvdbId,omitempty"`
	Added    bool   `json:"added"`
	// Enriched metadata
	Genres        []string `json:"genres,omitempty"`
	Certification string   `json:"certification,omitempty"`
	Runtime       int      `json:"runtime,omitempty"`     // minutes
	Rating        float64  `json:"rating,omitempty"`      // IMDb preferred, falls back to TMDB
	Network       string   `json:"network,omitempty"`     // series only
	SeasonCount   int      `json:"seasonCount,omitempty"` // series only
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query().Get("q")
	if q == "" {
		httputil.WriteJSON(w, map[string]any{"results": []SearchResult{}})
		return
	}

	typeFilter := r.URL.Query().Get("type") // "movie", "series", or "" for both

	encoded := url.QueryEscape(q)
	var movies, series []SearchResult
	var mu sync.Mutex
	var wg sync.WaitGroup

	sonarrKey, radarrKey, prowlarrKey := services.Keys()

	envMu.Lock()
	envVars, _ := parseEnvFile(envPath)
	envMu.Unlock()
	searchMode := envVars["SEARCH_MODE"] // "" or "tmdb" = TMDB/TVDB; "indexer" = filter by Prowlarr

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
				sr := SearchResult{
					Type:          "movie",
					Title:         strVal(m, "title"),
					Year:          int(floatVal(m, "year")),
					Overview:      strVal(m, "overview"),
					Poster:        poster,
					TmdbID:        tmdbID,
					Added:         existingIDs[tmdbID],
					Certification: strVal(m, "certification"),
					Runtime:       int(floatVal(m, "runtime")),
				}
				if genres, ok := m["genres"].([]any); ok {
					for _, g := range genres {
						if s, ok := g.(string); ok {
							sr.Genres = append(sr.Genres, s)
						}
					}
				}
				if ratings, ok := m["ratings"].(map[string]any); ok {
					if imdb, ok := ratings["imdb"].(map[string]any); ok {
						sr.Rating = floatVal(imdb, "value")
					}
					if sr.Rating == 0 {
						if tmdbR, ok := ratings["tmdb"].(map[string]any); ok {
							sr.Rating = floatVal(tmdbR, "value")
						}
					}
				}
				movies = append(movies, sr)
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
				sr := SearchResult{
					Type:          "series",
					Title:         strVal(s, "title"),
					Year:          int(floatVal(s, "year")),
					Overview:      strVal(s, "overview"),
					Poster:        poster,
					TvdbID:        tvdbID,
					TmdbID:        tmdbID,
					Added:         existingIDs[tvdbID],
					Certification: strVal(s, "certification"),
					Runtime:       int(floatVal(s, "runtime")),
					Network:       strVal(s, "network"),
				}
				if genres, ok := s["genres"].([]any); ok {
					for _, g := range genres {
						if gs, ok := g.(string); ok {
							sr.Genres = append(sr.Genres, gs)
						}
					}
				}
				if ratings, ok := s["ratings"].(map[string]any); ok {
					if imdb, ok := ratings["imdb"].(map[string]any); ok {
						sr.Rating = floatVal(imdb, "value")
					}
					if sr.Rating == 0 {
						if tmdbR, ok := ratings["tmdb"].(map[string]any); ok {
							sr.Rating = floatVal(tmdbR, "value")
						}
					}
				}
				if stats, ok := s["statistics"].(map[string]any); ok {
					sr.SeasonCount = int(floatVal(stats, "seasonCount"))
				}
				series = append(series, sr)
			}
			mu.Unlock()
		}()
	}

	// Search Prowlarr indexers (indexer mode only)
	var availTmdbIDs map[int]bool // nil = don't filter (error or not in indexer mode)
	var availTvdbIDs map[int]bool
	if searchMode == "indexer" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := "/api/v1/search?query=" + encoded + "&type=search&limit=100"
			data, err := services.ArrGet(prowlarrURL, prowlarrKey, path)
			if err != nil {
				slog.Warn("prowlarr search unavailable, degrading to unfiltered results", "component", "search", "error", err)
				return // nil maps = no filtering
			}
			var releases []map[string]any
			if json.Unmarshal(data, &releases) != nil {
				return
			}
			tmdbSet := make(map[int]bool)
			tvdbSet := make(map[int]bool)
			for _, rel := range releases {
				if id, ok := rel["tmdbId"].(float64); ok && id > 0 {
					tmdbSet[int(id)] = true
				}
				if id, ok := rel["tvdbId"].(float64); ok && id > 0 {
					tvdbSet[int(id)] = true
				}
			}
			mu.Lock()
			availTmdbIDs = tmdbSet
			availTvdbIDs = tvdbSet
			mu.Unlock()
		}()
	}

	wg.Wait()

	// Filter to indexer availability when in indexer mode
	if searchMode == "indexer" && availTmdbIDs != nil {
		filtered := movies[:0]
		for _, m := range movies {
			if availTmdbIDs[m.TmdbID] {
				filtered = append(filtered, m)
			}
		}
		movies = filtered
	}
	if searchMode == "indexer" && availTvdbIDs != nil {
		filtered := series[:0]
		for _, s := range series {
			if availTvdbIDs[s.TvdbID] {
				filtered = append(filtered, s)
			}
		}
		series = filtered
	}

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

	httputil.WriteJSON(w, map[string]any{"results": results})
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
		Title  string `json:"title"`
		Year   int    `json:"year"`
		Poster string `json:"poster"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	var arrID int
	var err error
	switch req.Type {
	case "movie":
		arrID, err = addMovieInternal(req.TmdbID, 0, "")
		if err != nil {
			httputil.WriteError(w, "failed to add movie: "+err.Error(), http.StatusBadGateway)
			return
		}
	case "series":
		arrID, err = addSeriesInternal(req.TvdbID, 0, "")
		if err != nil {
			httputil.WriteError(w, "failed to add series: "+err.Error(), http.StatusBadGateway)
			return
		}
	default:
		httputil.WriteError(w, "type must be 'movie' or 'series'", http.StatusBadRequest)
		return
	}

	httputil.WriteJSON(w, map[string]any{"status": "added", "arr_id": arrID})
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

	httputil.WriteJSON(w, map[string]any{
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

// arrFulfiller is the production clients.Fulfiller backed by Sonarr/Radarr.
type arrFulfiller struct{}

// NewArrFulfiller returns a clients.Fulfiller that delegates to the existing
// package-level addMovieInternal/addSeriesInternal helpers.
func NewArrFulfiller() clients.Fulfiller { return &arrFulfiller{} }

func (f *arrFulfiller) AddMovie(tmdbID, profileID int, rootPath string) (int, error) {
	return addMovieInternal(tmdbID, profileID, rootPath)
}

func (f *arrFulfiller) AddSeries(tvdbID, profileID int, rootPath string) (int, error) {
	return addSeriesInternal(tvdbID, profileID, rootPath)
}
