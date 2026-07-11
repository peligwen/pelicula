// Package search implements the unified search handler (TMDB/TVDB/Prowlarr)
// and the add-to-arr functionality for movies and series.
package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"pelicula-api/clients"
	"pelicula-api/httputil"
	"pelicula-api/internal/app/catalog"
	"pelicula-api/internal/app/library"
	arr "pelicula-api/internal/clients/arr"
	"pelicula-api/internal/config"
)

// ArrClient is the subset of services.Clients that the search package needs.
type ArrClient interface {
	Keys() (sonarr, radarr, prowlarr string)
	SonarrClient() *arr.Client
	RadarrClient() *arr.Client
	ProwlarrClient() *arr.Client
}

// Handler holds all dependencies for the unified search endpoints.
type Handler struct {
	Services    ArrClient
	SonarrURL   string
	RadarrURL   string
	ProwlarrURL string
	LibHandler  *library.Handler
	searchMode  string // "" or "tmdb" for TMDB/TVDB; "indexer" for Prowlarr filtering

	// ArrCache is the shared Radarr/Sonarr full-library cache also used by
	// catalog.Handler and missingwatcher.Watcher (see bootstrap's
	// arrCatalogCache). HandleSearch uses it to compute each result's "added"
	// flag without a redundant full-library fetch on every search request.
	// Optional; nil falls back to a direct typed-client fetch (e.g. in tests).
	ArrCache *catalog.CatalogCache

	// now is injectable for tests; production code leaves it nil (falls back to time.Now).
	now func() time.Time

	cache struct {
		mu      sync.Mutex
		entries map[string]indexerSearchEntry
	}
}

// timeNow returns the current time using the injectable clock (for tests) or
// time.Now in production.
func (h *Handler) timeNow() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// New constructs a Handler. searchMode is resolved at construction from the
// parsed .env; no per-request .env reads occur.
func New(svc ArrClient, sonarrURL, radarrURL, prowlarrURL string, libHandler *library.Handler, searchMode string) *Handler {
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

// fetchExistingMovies returns the full Radarr movie list, used to compute
// each search result's "added" flag. It draws from the shared ArrCache when
// wired (avoiding a redundant full-library fetch on every search — see
// bootstrap's arrCatalogCache comment) and falls back to a direct typed-client
// fetch otherwise, mirroring missingwatcher.Watcher's same fallback pattern.
func (h *Handler) fetchExistingMovies(ctx context.Context) []map[string]any {
	if h.ArrCache != nil {
		data, err := h.ArrCache.GetMovies(ctx)
		if err != nil {
			return nil
		}
		var out []map[string]any
		if json.Unmarshal(data, &out) != nil {
			return nil
		}
		return out
	}
	existing, err := h.Services.RadarrClient().GetMovies(ctx, "/api/v3")
	if err != nil {
		return nil
	}
	return existing
}

// fetchExistingSeries is fetchExistingMovies' Sonarr counterpart.
func (h *Handler) fetchExistingSeries(ctx context.Context) []map[string]any {
	if h.ArrCache != nil {
		data, err := h.ArrCache.GetSeries(ctx)
		if err != nil {
			return nil
		}
		var out []map[string]any
		if json.Unmarshal(data, &out) != nil {
			return nil
		}
		return out
	}
	existing, err := h.Services.SonarrClient().GetSeries(ctx, "/api/v3")
	if err != nil {
		return nil
	}
	return existing
}

// ---- indexer search cache ----

type indexerSearchEntry struct {
	data      []byte
	fetchedAt time.Time
}

const indexerSearchTTL = 2 * time.Minute

func (h *Handler) cachedIndexerSearch(ctx context.Context, query string) ([]byte, error) {
	_, _, prowlarrKey := h.Services.Keys()
	key := strings.ToLower(strings.TrimSpace(query))

	h.cache.mu.Lock()
	if e, ok := h.cache.entries[key]; ok && h.timeNow().Sub(e.fetchedAt) < indexerSearchTTL {
		h.cache.mu.Unlock()
		return e.data, nil
	}
	h.cache.mu.Unlock()

	if prowlarrKey == "" {
		return nil, fmt.Errorf("prowlarr not configured")
	}
	path := "/api/v1/search?query=" + url.QueryEscape(query) + "&type=search&limit=100"
	data, err := h.Services.ProwlarrClient().Get(ctx, path)
	if err != nil {
		return nil, err
	}

	h.cache.mu.Lock()
	// Evict stale entries (lazy eviction — avoid unbounded growth)
	now := h.timeNow()
	for k, e := range h.cache.entries {
		if now.Sub(e.fetchedAt) >= indexerSearchTTL {
			delete(h.cache.entries, k)
		}
	}
	h.cache.entries[key] = indexerSearchEntry{data: data, fetchedAt: now}
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
	// Seasons is additive per-season metadata for series results, used by the
	// season-picker UI (Phase 2.2) to know what's available to select.
	Seasons []SeasonInfo `json:"seasons,omitempty"` // series only
}

// SeasonInfo is one season's additive metadata on a series search result.
type SeasonInfo struct {
	SeasonNumber int `json:"seasonNumber"`
	// EpisodeCount is populated only when the upstream Sonarr lookup's
	// statistics.totalEpisodeCount is present — it's often absent on
	// lookups, and this field is never fabricated when it's missing.
	EpisodeCount int `json:"episodeCount,omitempty"`
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

	var movies, series []SearchResult
	var mu sync.Mutex
	var wg sync.WaitGroup

	sonarrKey, radarrKey, prowlarrKey := h.Services.Keys()
	_ = prowlarrKey // used below in indexer mode

	// Search Radarr (movies)
	if typeFilter != "series" && radarrKey != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rawMovies, err := h.Services.RadarrClient().LookupMovie(r.Context(), "/api/v3", q)
			if err != nil {
				slog.Error("radarr search error", "component", "search", "error", err)
				return
			}

			// Get existing movies to check "added" status
			existingIDs := make(map[int]bool)
			for _, m := range h.fetchExistingMovies(r.Context()) {
				if id, ok := m["tmdbId"].(float64); ok {
					existingIDs[int(id)] = true
				}
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
	if typeFilter != "movie" && sonarrKey != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			shows, err := h.Services.SonarrClient().LookupSeries(r.Context(), "/api/v3", q)
			if err != nil {
				slog.Error("sonarr search error", "component", "search", "error", err)
				return
			}

			existingIDs := make(map[int]bool)
			for _, s := range h.fetchExistingSeries(r.Context()) {
				if id, ok := s["tvdbId"].(float64); ok {
					existingIDs[int(id)] = true
				}
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
					Seasons:  extractSeasonInfo(s),
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
			data, err := h.cachedIndexerSearch(r.Context(), q)
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
		// ProfileID/RootPath are optional add-time overrides for the "Add with
		// options…" dashboard modal. Absent/zero preserves today's default
		// exactly (first quality profile / FirstLibraryPath, gated through the
		// REQUESTS_*_PROFILE_ID / REQUESTS_*_ROOT env vars below). Both are
		// validated server-side against the relevant *arr before use — see
		// profileIDValid/rootPathValid.
		ProfileID int    `json:"profileId"`
		RootPath  string `json:"rootPath"`
		// Seasons is series-only: the season numbers to monitor. Absent/null
		// means "monitor all seasons" (today's default, byte-identical
		// payload). A non-nil empty array is rejected — there is no
		// "monitor nothing" add. See normalizeSeasons for the shape rules
		// and addSeriesInternal for existence validation against the lookup.
		Seasons []int `json:"seasons"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Seasons != nil && req.Type != "series" {
		httputil.WriteError(w, "seasons is only valid for series", http.StatusBadRequest)
		return
	}
	seasons, seasonsErr := normalizeSeasons(req.Seasons)
	if seasonsErr != "" {
		httputil.WriteError(w, seasonsErr, http.StatusBadRequest)
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
		if req.ProfileID != 0 {
			ok, verifyErr := h.profileIDValid(r.Context(), h.Services.RadarrClient(), req.ProfileID)
			if verifyErr != nil {
				slog.Error("validate radarr profileId failed", "component", "search", "error", verifyErr)
				httputil.WriteError(w, "Radarr is unreachable — check service health", http.StatusBadGateway)
				return
			}
			if !ok {
				httputil.WriteError(w, fmt.Sprintf("profileId %d is not a valid Radarr quality profile", req.ProfileID), http.StatusBadRequest)
				return
			}
			radarrProfileID = req.ProfileID
		}
		if req.RootPath != "" {
			if !h.rootPathValid("radarr", req.RootPath) {
				httputil.WriteError(w, fmt.Sprintf("rootPath %q is not a registered Radarr library", req.RootPath), http.StatusBadRequest)
				return
			}
			radarrRoot = req.RootPath
		}
		arrID, err = h.addMovieInternal(r.Context(), req.TmdbID, radarrProfileID, radarrRoot)
		if err != nil {
			slog.Error("add movie failed", "component", "search", "tmdbId", req.TmdbID, "error", err)
			httputil.WriteError(w, "Radarr is unreachable — check service health", http.StatusBadGateway)
			return
		}
	case "series":
		if req.ProfileID != 0 {
			ok, verifyErr := h.profileIDValid(r.Context(), h.Services.SonarrClient(), req.ProfileID)
			if verifyErr != nil {
				slog.Error("validate sonarr profileId failed", "component", "search", "error", verifyErr)
				httputil.WriteError(w, "Sonarr is unreachable — check service health", http.StatusBadGateway)
				return
			}
			if !ok {
				httputil.WriteError(w, fmt.Sprintf("profileId %d is not a valid Sonarr quality profile", req.ProfileID), http.StatusBadRequest)
				return
			}
			sonarrProfileID = req.ProfileID
		}
		if req.RootPath != "" {
			if !h.rootPathValid("sonarr", req.RootPath) {
				httputil.WriteError(w, fmt.Sprintf("rootPath %q is not a registered Sonarr library", req.RootPath), http.StatusBadRequest)
				return
			}
			sonarrRoot = req.RootPath
		}
		arrID, err = h.addSeriesInternal(r.Context(), req.TvdbID, sonarrProfileID, sonarrRoot, seasons)
		if err != nil {
			if errors.Is(err, clients.ErrInvalidSeasons) {
				httputil.WriteError(w, err.Error(), http.StatusBadRequest)
				return
			}
			slog.Error("add series failed", "component", "search", "tvdbId", req.TvdbID, "error", err)
			httputil.WriteError(w, "Sonarr is unreachable — check service health", http.StatusBadGateway)
			return
		}
	default:
		httputil.WriteError(w, "type must be 'movie' or 'series'", http.StatusBadRequest)
		return
	}

	httputil.WriteJSON(w, map[string]any{"status": "added", "arr_id": arrID})
}

// profileIDValid reports whether profileID matches an id returned by
// client's GetQualityProfiles — the same source addMovieInternal/
// addSeriesInternal's default (first profile) draws from, so a valid
// override is always a value the default could have produced. The error
// return is non-nil only when the profiles list itself could not be fetched
// (upstream unreachable); callers must not treat that the same as a false,
// nil-error mismatch.
func (h *Handler) profileIDValid(ctx context.Context, client *arr.Client, profileID int) (bool, error) {
	profiles, err := client.GetQualityProfiles(ctx, "/api/v3")
	if err != nil {
		return false, err
	}
	for _, p := range profiles {
		if int(floatVal(p, "id")) == profileID {
			return true, nil
		}
	}
	return false, nil
}

// rootPathValid reports whether rootPath matches a registered library's
// container path for the given arr ("radarr" or "sonarr") — the same source
// FirstLibraryPath (the default) draws from.
func (h *Handler) rootPathValid(arrName, rootPath string) bool {
	for _, lib := range h.LibHandler.GetLibraries() {
		if lib.Arr == arrName && lib.ContainerPath() == rootPath {
			return true
		}
	}
	return false
}

// addMovieInternal adds a movie to Radarr and returns the Radarr internal ID.
// If profileID is 0 the first available quality profile is used.
// If rootPath is "" the first radarr library's container path is used.
func (h *Handler) addMovieInternal(ctx context.Context, tmdbID, profileID int, rootPath string) (int, error) {
	_, radarrKey, _ := h.Services.Keys()
	if radarrKey == "" {
		return 0, fmt.Errorf("radarr not configured")
	}
	// Radarr's /movie/lookup/tmdb returns a single object, not an array.
	data, err := h.Services.RadarrClient().Get(ctx, "/api/v3/movie/lookup/tmdb?tmdbId="+itoa(tmdbID))
	if err != nil {
		return 0, fmt.Errorf("look up movie: %w", err)
	}
	var movie map[string]any
	if err := json.Unmarshal(data, &movie); err != nil {
		return 0, fmt.Errorf("parse movie data: %w", err)
	}

	if profileID == 0 {
		if profiles, err := h.Services.RadarrClient().GetQualityProfiles(ctx, "/api/v3"); err == nil && len(profiles) > 0 {
			if id, ok := profiles[0]["id"].(float64); ok {
				profileID = int(id)
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
	added, err := h.Services.RadarrClient().AddMovie(ctx, "/api/v3", payload)
	if err != nil {
		return 0, fmt.Errorf("add movie: %w", err)
	}
	return int(floatVal(added, "id")), nil
}

// addSeriesInternal adds a series to Sonarr and returns the Sonarr internal ID.
// If profileID is 0 the first available quality profile is used.
// If rootPath is "" the first sonarr library's container path is used.
// seasons selects which season numbers to monitor; nil means "monitor all
// seasons" and produces a payload byte-identical to the pre-season-support
// shape (no "seasons" key at all). A non-nil seasons is validated against the
// lookup's own season list — see buildSeasonsPayload — before Sonarr is ever
// called; a mismatch surfaces as clients.ErrInvalidSeasons.
func (h *Handler) addSeriesInternal(ctx context.Context, tvdbID, profileID int, rootPath string, seasons []int) (int, error) {
	sonarrKey, _, _ := h.Services.Keys()
	if sonarrKey == "" {
		return 0, fmt.Errorf("sonarr not configured")
	}
	shows, err := h.Services.SonarrClient().LookupSeries(ctx, "/api/v3", "tvdb:"+itoa(tvdbID))
	if err != nil {
		return 0, fmt.Errorf("look up series: %w", err)
	}
	if len(shows) == 0 {
		return 0, fmt.Errorf("series not found")
	}
	show := shows[0]

	if profileID == 0 {
		if profiles, err := h.Services.SonarrClient().GetQualityProfiles(ctx, "/api/v3"); err == nil && len(profiles) > 0 {
			if id, ok := profiles[0]["id"].(float64); ok {
				profileID = int(id)
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

	if seasons != nil {
		seasonsPayload, err := buildSeasonsPayload(show, seasons)
		if err != nil {
			return 0, err
		}
		payload["seasons"] = seasonsPayload
	}
	// Deliberately no addOptions.monitor: Sonarr applies that enum after
	// refresh and it would overwrite the explicit per-season monitored flags
	// set above. This exact shape (explicit seasons[].monitored, no
	// addOptions.monitor) is already proven against Sonarr v3 in
	// internal/app/backup/restore.go's importSeries.

	added, err := h.Services.SonarrClient().AddSeries(ctx, "/api/v3", payload)
	if err != nil {
		return 0, fmt.Errorf("add series: %w", err)
	}
	return int(floatVal(added, "id")), nil
}

// buildSeasonsPayload validates seasons (the requested season numbers)
// against show's own "seasons" lookup data and returns the full Sonarr
// seasons array: every season the lookup reports, each with an explicit
// "monitored" flag (true for a selected season, false otherwise — including
// specials/season 0 when selected or not). Returns clients.ErrInvalidSeasons
// (wrapped, with the offending numbers) if any requested season number does
// not exist for this series — existence is checked here, before Sonarr is
// ever called.
func buildSeasonsPayload(show map[string]any, seasons []int) ([]map[string]any, error) {
	rawSeasons, _ := show["seasons"].([]any)

	existing := make(map[int]bool, len(rawSeasons))
	for _, rs := range rawSeasons {
		if sm, ok := rs.(map[string]any); ok {
			existing[int(floatVal(sm, "seasonNumber"))] = true
		}
	}

	selected := make(map[int]bool, len(seasons))
	for _, n := range seasons {
		selected[n] = true
	}

	var badNums []int
	for n := range selected {
		if !existing[n] {
			badNums = append(badNums, n)
		}
	}
	if len(badNums) > 0 {
		sort.Ints(badNums)
		return nil, fmt.Errorf("%w: season(s) %v not found for this series", clients.ErrInvalidSeasons, badNums)
	}

	out := make([]map[string]any, 0, len(rawSeasons))
	for _, rs := range rawSeasons {
		sm, ok := rs.(map[string]any)
		if !ok {
			continue
		}
		num := int(floatVal(sm, "seasonNumber"))
		out = append(out, map[string]any{
			"seasonNumber": num,
			"monitored":    selected[num],
		})
	}
	return out, nil
}

// normalizeSeasons validates the shape of a seasons parameter shared by
// search/add and (mirrored independently in peligrosa, see that package's own
// copy — the two handlers are intentionally decoupled, per Fulfiller's doc
// comment) the request-queue create/approve endpoints: nil is valid ("all
// seasons"); a non-nil empty slice is rejected (there is no "monitor
// nothing" add); each number must be in [0,999]; at most 100 entries;
// duplicates are silently removed. Returns the deduped, sorted slice and an
// empty error string on success, or (nil, message) with message suitable for
// a 400 response body.
func normalizeSeasons(seasons []int) ([]int, string) {
	if seasons == nil {
		return nil, ""
	}
	if len(seasons) == 0 {
		return nil, "seasons must be a non-empty array of season numbers"
	}
	if len(seasons) > 100 {
		return nil, "seasons must contain at most 100 entries"
	}
	seen := make(map[int]bool, len(seasons))
	out := make([]int, 0, len(seasons))
	for _, n := range seasons {
		if n < 0 || n > 999 {
			return nil, fmt.Sprintf("season number %d out of range (0-999)", n)
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Ints(out)
	return out, ""
}

// HandleArrMeta returns quality profiles and root folders from Radarr and
// Sonarr, plus the registered Pelicula libraries for each arr. Used by the
// admin settings UI to populate request profile dropdowns and by the search
// "Add with options…" modal to populate its Target Library select.
func (h *Handler) HandleArrMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	type profileEntry struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	type rootEntry struct {
		Path string `json:"path"`
	}
	// libraryEntry is the registered-library counterpart to rootEntry: unlike
	// RootFolders (the *arr's own root-folder list, which need not match a
	// Pelicula library on custom-library setups), each entry here is exactly
	// a value rootPathValid accepts — see that function's doc comment.
	type libraryEntry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	type arrMeta struct {
		QualityProfiles []profileEntry `json:"qualityProfiles"`
		RootFolders     []rootEntry    `json:"rootFolders"`
		Libraries       []libraryEntry `json:"libraries"`
	}

	fetchProfiles := func(client *arr.Client) []profileEntry {
		raw, err := client.GetQualityProfiles(r.Context(), "/api/v3")
		if err != nil {
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
	fetchRoots := func(client *arr.Client) []rootEntry {
		raw, err := client.ListRootFolders(r.Context(), "/api/v3")
		if err != nil {
			return nil
		}
		var out []rootEntry
		for _, f := range raw {
			out = append(out, rootEntry{Path: strVal(f, "path")})
		}
		return out
	}
	// fetchLibraries filters the registered library list down to the given
	// arr ("radarr" or "sonarr"), mirroring rootPathValid's own filter so the
	// modal only ever offers paths the backend will accept. Always returns a
	// non-nil (possibly empty) slice so the field serializes as `[]`, never
	// `null`.
	fetchLibraries := func(arrName string) []libraryEntry {
		out := []libraryEntry{}
		for _, lib := range h.LibHandler.GetLibraries() {
			if lib.Arr == arrName {
				out = append(out, libraryEntry{Name: lib.Name, Path: lib.ContainerPath()})
			}
		}
		return out
	}

	httputil.WriteJSON(w, map[string]any{
		"radarr": arrMeta{
			QualityProfiles: fetchProfiles(h.Services.RadarrClient()),
			RootFolders:     fetchRoots(h.Services.RadarrClient()),
			Libraries:       fetchLibraries("radarr"),
		},
		"sonarr": arrMeta{
			QualityProfiles: fetchProfiles(h.Services.SonarrClient()),
			RootFolders:     fetchRoots(h.Services.SonarrClient()),
			Libraries:       fetchLibraries("sonarr"),
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

// extractSeasonInfo builds the additive seasons metadata for a series search
// result from a raw Sonarr lookup item's "seasons" array. EpisodeCount is
// populated only when that season's statistics.totalEpisodeCount is present
// in the lookup response — see SeasonInfo's doc comment.
func extractSeasonInfo(raw map[string]any) []SeasonInfo {
	rawSeasons, _ := raw["seasons"].([]any)
	if len(rawSeasons) == 0 {
		return nil
	}
	out := make([]SeasonInfo, 0, len(rawSeasons))
	for _, rs := range rawSeasons {
		sm, ok := rs.(map[string]any)
		if !ok {
			continue
		}
		si := SeasonInfo{SeasonNumber: int(floatVal(sm, "seasonNumber"))}
		if stats, ok := sm["statistics"].(map[string]any); ok {
			if ec, ok := stats["totalEpisodeCount"].(float64); ok {
				si.EpisodeCount = int(ec)
			}
		}
		out = append(out, si)
	}
	return out
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
