// Package search implements the unified search handler (TMDB/TVDB/Prowlarr)
// and the add-to-arr functionality for movies and series.
package search

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"pelicula-api/httputil"
	"pelicula-api/internal/app/library"
	services "pelicula-api/internal/app/services"
	"pelicula-api/internal/config"
)

// Handler holds all dependencies for the unified search endpoints.
type Handler struct {
	Services    *services.Clients
	SonarrURL   string
	RadarrURL   string
	ProwlarrURL string
	LibHandler  *library.Handler
	// TODO: wire tmdbKey into direct TMDB API calls when they are added
	searchMode string // "" or "tmdb" for TMDB/TVDB; "indexer" for Prowlarr filtering

	cache struct {
		mu      sync.Mutex
		entries map[string]indexerSearchEntry
	}
}

// New constructs a Handler. tmdbKey and searchMode are resolved at construction
// from the parsed .env; no per-request .env reads occur.
func New(svc *services.Clients, sonarrURL, radarrURL, prowlarrURL string, libHandler *library.Handler, tmdbKey, searchMode string) *Handler {
	h := &Handler{
		Services:    svc,
		SonarrURL:   sonarrURL,
		RadarrURL:   radarrURL,
		ProwlarrURL: prowlarrURL,
		LibHandler:  libHandler,
		searchMode:  searchMode,
	}
	h.cache.entries = make(map[string]indexerSearchEntry)
	return h
}

// ---- indexer search cache ----

type indexerSearchEntry struct {
	data      []byte
	fetchedAt time.Time
}

const indexerSearchTTL = 2 * time.Minute

func (h *Handler) cachedIndexerSearch(query string) ([]byte, error) {
	_, _, prowlarrKey := h.Services.Keys()
	key := strings.ToLower(strings.TrimSpace(query))

	h.cache.mu.Lock()
	if e, ok := h.cache.entries[key]; ok && time.Since(e.fetchedAt) < indexerSearchTTL {
		h.cache.mu.Unlock()
		return e.data, nil
	}
	h.cache.mu.Unlock()

	path := "/api/v1/search?query=" + url.QueryEscape(query) + "&type=search&limit=100"
	data, err := h.Services.ArrGet(h.ProwlarrURL, prowlarrKey, path)
	if err != nil {
		return nil, err
	}

	h.cache.mu.Lock()
	// Evict stale entries (lazy eviction — avoid unbounded growth)
	for k, e := range h.cache.entries {
		if time.Since(e.fetchedAt) >= indexerSearchTTL {
			delete(h.cache.entries, k)
		}
	}
	h.cache.entries[key] = indexerSearchEntry{data: data, fetchedAt: time.Now()}
	h.cache.mu.Unlock()

	return data, nil
}

// ---- SearchResult type ----

// SearchResult is the JSON response type for the unified search endpoint.
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

// ---- Handlers ----

// HandleSearch is the unified TMDB/TVDB/Prowlarr search handler.
func (h *Handler) HandleSearch(w http.ResponseWriter, r *http.Request) {
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

	sonarrKey, radarrKey, prowlarrKey := h.Services.Keys()
	_ = prowlarrKey // used below in indexer mode

	// Search Radarr (movies)
	if typeFilter != "series" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := h.Services.ArrGet(h.RadarrURL, radarrKey, "/api/v3/movie/lookup?term="+encoded)
			if err != nil {
				slog.Error("radarr search error", "component", "search", "error", err)
				return
			}

			// Get existing movies to check "added" status
			existingData, _ := h.Services.ArrGet(h.RadarrURL, radarrKey, "/api/v3/movie")
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
				sr := SearchResult{
					Type:     "movie",
					Title:    strVal(m, "title"),
					Year:     int(floatVal(m, "year")),
					Overview: strVal(m, "overview"),
					TmdbID:   tmdbID,
					Added:    existingIDs[tmdbID],
				}
				enrichSearchResult(&sr, m)
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
			data, err := h.Services.ArrGet(h.SonarrURL, sonarrKey, "/api/v3/series/lookup?term="+encoded)
			if err != nil {
				slog.Error("sonarr search error", "component", "search", "error", err)
				return
			}

			existingData, _ := h.Services.ArrGet(h.SonarrURL, sonarrKey, "/api/v3/series")
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
				sr := SearchResult{
					Type:     "series",
					Title:    strVal(s, "title"),
					Year:     int(floatVal(s, "year")),
					Overview: strVal(s, "overview"),
					TvdbID:   tvdbID,
					TmdbID:   tmdbID,
					Added:    existingIDs[tvdbID],
					Network:  strVal(s, "network"),
				}
				enrichSearchResult(&sr, s)
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
	if h.searchMode == "indexer" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := h.cachedIndexerSearch(q)
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
	if h.searchMode == "indexer" && availTmdbIDs != nil {
		filtered := movies[:0]
		for _, m := range movies {
			if availTmdbIDs[m.TmdbID] {
				filtered = append(filtered, m)
			}
		}
		movies = filtered
	}
	if h.searchMode == "indexer" && availTvdbIDs != nil {
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

// HandleSearchAdd handles adding a movie or series to Radarr/Sonarr.
func (h *Handler) HandleSearchAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
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

	// Read the same request profile env vars that handleRequestApprove uses,
	// so both add paths honour REQUESTS_RADARR_PROFILE_ID / REQUESTS_RADARR_ROOT.
	radarrProfileID := config.IntOr("REQUESTS_RADARR_PROFILE_ID", 0)
	radarrRoot := os.Getenv("REQUESTS_RADARR_ROOT")
	sonarrProfileID := config.IntOr("REQUESTS_SONARR_PROFILE_ID", 0)
	sonarrRoot := os.Getenv("REQUESTS_SONARR_ROOT")

	var arrID int
	var err error
	switch req.Type {
	case "movie":
		arrID, err = h.addMovieInternal(req.TmdbID, radarrProfileID, radarrRoot)
		if err != nil {
			httputil.WriteError(w, "failed to add movie: "+err.Error(), http.StatusBadGateway)
			return
		}
	case "series":
		arrID, err = h.addSeriesInternal(req.TvdbID, sonarrProfileID, sonarrRoot)
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
// If rootPath is "" the first radarr library's container path is used.
func (h *Handler) addMovieInternal(tmdbID, profileID int, rootPath string) (int, error) {
	_, radarrKey, _ := h.Services.Keys()
	data, err := h.Services.ArrGet(h.RadarrURL, radarrKey, "/api/v3/movie/lookup/tmdb?tmdbId="+itoa(tmdbID))
	if err != nil {
		return 0, fmt.Errorf("look up movie: %w", err)
	}
	var movie map[string]any
	if err := json.Unmarshal(data, &movie); err != nil {
		return 0, fmt.Errorf("parse movie data: %w", err)
	}

	if profileID == 0 {
		profData, err := h.Services.ArrGet(h.RadarrURL, radarrKey, "/api/v3/qualityprofile")
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
		rootPath = h.LibHandler.FirstLibraryPath("radarr", "/media/movies")
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
	resp, err := h.Services.ArrPost(h.RadarrURL, radarrKey, "/api/v3/movie", payload)
	if err != nil {
		return 0, fmt.Errorf("add movie: %w", err)
	}
	var added map[string]any
	json.Unmarshal(resp, &added) //nolint:errcheck
	return int(floatVal(added, "id")), nil
}

// addSeriesInternal adds a series to Sonarr and returns the Sonarr internal ID.
// If profileID is 0 the first available quality profile is used.
// If rootPath is "" the first sonarr library's container path is used.
func (h *Handler) addSeriesInternal(tvdbID, profileID int, rootPath string) (int, error) {
	sonarrKey, _, _ := h.Services.Keys()
	data, err := h.Services.ArrGet(h.SonarrURL, sonarrKey, "/api/v3/series/lookup?term=tvdb:"+itoa(tvdbID))
	if err != nil {
		return 0, fmt.Errorf("look up series: %w", err)
	}
	var shows []map[string]any
	if err := json.Unmarshal(data, &shows); err != nil || len(shows) == 0 {
		return 0, fmt.Errorf("series not found")
	}
	show := shows[0]

	if profileID == 0 {
		profData, err := h.Services.ArrGet(h.SonarrURL, sonarrKey, "/api/v3/qualityprofile")
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
		rootPath = h.LibHandler.FirstLibraryPath("sonarr", "/media/tv")
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
	resp, err := h.Services.ArrPost(h.SonarrURL, sonarrKey, "/api/v3/series", payload)
	if err != nil {
		return 0, fmt.Errorf("add series: %w", err)
	}
	var added map[string]any
	json.Unmarshal(resp, &added) //nolint:errcheck
	return int(floatVal(added, "id")), nil
}

// HandleArrMeta returns quality profiles and root folders from Radarr and Sonarr.
// Used by the admin settings UI to populate request profile dropdowns.
func (h *Handler) HandleArrMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sonarrKey, radarrKey, _ := h.Services.Keys()

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
		data, err := h.Services.ArrGet(baseURL, apiKey, "/api/v3/qualityprofile")
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
		data, err := h.Services.ArrGet(baseURL, apiKey, "/api/v3/rootfolder")
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
			QualityProfiles: fetchProfiles(h.RadarrURL, radarrKey),
			RootFolders:     fetchRoots(h.RadarrURL, radarrKey),
		},
		"sonarr": arrMeta{
			QualityProfiles: fetchProfiles(h.SonarrURL, sonarrKey),
			RootFolders:     fetchRoots(h.SonarrURL, sonarrKey),
		},
	})
}

// ---- private helpers ----

// extractPoster returns the remoteUrl of the first poster image in an *arr
// images array, or "" if none is found.
func extractPoster(raw map[string]any) string {
	if images, ok := raw["images"].([]any); ok {
		for _, img := range images {
			if imgMap, ok := img.(map[string]any); ok {
				if imgMap["coverType"] == "poster" {
					return strVal(imgMap, "remoteUrl")
				}
			}
		}
	}
	return ""
}

// enrichSearchResult fills in common metadata fields (poster, certification,
// runtime, genres, rating) from a raw *arr lookup item.  Movie-specific fields
// (TmdbID, Added) and series-specific fields (TvdbID, Network, SeasonCount)
// are set by the caller.
func enrichSearchResult(sr *SearchResult, raw map[string]any) {
	sr.Poster = extractPoster(raw)
	sr.Certification = strVal(raw, "certification")
	sr.Runtime = int(floatVal(raw, "runtime"))
	if genres, ok := raw["genres"].([]any); ok {
		for _, g := range genres {
			if s, ok := g.(string); ok {
				sr.Genres = append(sr.Genres, s)
			}
		}
	}
	if ratings, ok := raw["ratings"].(map[string]any); ok {
		if imdb, ok := ratings["imdb"].(map[string]any); ok {
			sr.Rating = floatVal(imdb, "value")
		}
		if sr.Rating == 0 {
			if tmdbR, ok := ratings["tmdb"].(map[string]any); ok {
				sr.Rating = floatVal(tmdbR, "value")
			}
		}
	}
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}

func strVal(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func floatVal(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}
