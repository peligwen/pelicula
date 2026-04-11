package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"pelicula-api/httputil"
	"sync"
	"time"
)

// ── Export types ─────────────────────────────────────────────────────────────

const currentBackupVersion = 2

type BackupExport struct {
	Version         int             `json:"version"`
	PeliculaVersion string          `json:"pelicula_version,omitempty"`
	Exported        string          `json:"exported"`
	Movies          []MovieExport   `json:"movies"`
	Series          []SeriesExport  `json:"series"`
	Roles           []RolesEntry    `json:"roles,omitempty"`
	Invites         []InviteExport  `json:"invites,omitempty"`
	Requests        []RequestExport `json:"requests,omitempty"`
}

// InviteExport captures the full state of an invite for backup/restore.
type InviteExport struct {
	Token     string     `json:"token"`
	Label     string     `json:"label,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	CreatedBy string     `json:"created_by"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	MaxUses   *int       `json:"max_uses,omitempty"`
	Uses      int        `json:"uses"`
	Revoked   bool       `json:"revoked"`
}

// RequestExport captures the full state of a media request for backup/restore.
type RequestExport struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	TmdbID      int            `json:"tmdb_id"`
	TvdbID      int            `json:"tvdb_id"`
	Title       string         `json:"title"`
	Year        int            `json:"year"`
	Poster      string         `json:"poster,omitempty"`
	RequestedBy string         `json:"requested_by"`
	State       RequestState   `json:"state"`
	Reason      string         `json:"reason,omitempty"`
	ArrID       int            `json:"arr_id,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	History     []RequestEvent `json:"history"`
}

type MovieExport struct {
	Title          string          `json:"title"`
	Year           int             `json:"year"`
	TmdbID         int             `json:"tmdbId"`
	ImdbID         string          `json:"imdbId,omitempty"`
	Path           string          `json:"path"`
	QualityProfile string          `json:"qualityProfile"`
	Monitored      bool            `json:"monitored"`
	HasFile        bool            `json:"hasFile"`
	Tags           []string        `json:"tags"`
	FileInfo       *FileInfoExport `json:"fileInfo,omitempty"`
}

type SeriesExport struct {
	Title          string         `json:"title"`
	Year           int            `json:"year"`
	TvdbID         int            `json:"tvdbId"`
	TmdbID         int            `json:"tmdbId,omitempty"`
	ImdbID         string         `json:"imdbId,omitempty"`
	Path           string         `json:"path"`
	QualityProfile string         `json:"qualityProfile"`
	Monitored      bool           `json:"monitored"`
	HasFile        bool           `json:"hasFile"`
	Tags           []string       `json:"tags"`
	Seasons        []SeasonExport `json:"seasons"`
}

type SeasonExport struct {
	SeasonNumber int  `json:"seasonNumber"`
	Monitored    bool `json:"monitored"`
}

type FileInfoExport struct {
	RelativePath string `json:"relativePath"`
	Size         int64  `json:"size"`
	Quality      string `json:"quality"`
}

// ── Import result ─────────────────────────────────────────────────────────────

type ImportResult struct {
	MoviesAdded   int      `json:"moviesAdded"`
	MoviesSkipped int      `json:"moviesSkipped"`
	MoviesFailed  int      `json:"moviesFailed"`
	SeriesAdded   int      `json:"seriesAdded"`
	SeriesSkipped int      `json:"seriesSkipped"`
	SeriesFailed  int      `json:"seriesFailed"`
	Errors        []string `json:"errors,omitempty"`
}

// ── Handlers ─────────────────────────────────────────────────────────────────

func handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sonarrKey, radarrKey, _ := services.Keys()
	if radarrKey == "" || sonarrKey == "" {
		httputil.WriteError(w, "API keys not loaded — is the stack wired?", http.StatusServiceUnavailable)
		return
	}

	var (
		movies []MovieExport
		series []SeriesExport
		mu     sync.Mutex
		wg     sync.WaitGroup
		errs   []error
	)

	// Export movies from Radarr
	wg.Add(1)
	go func() {
		defer wg.Done()
		ms, err := exportMovies(radarrKey)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Errorf("radarr: %w", err))
			return
		}
		movies = ms
	}()

	// Export series from Sonarr
	wg.Add(1)
	go func() {
		defer wg.Done()
		ss, err := exportSeries(sonarrKey)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Errorf("sonarr: %w", err))
			return
		}
		series = ss
	}()

	wg.Wait()

	if len(errs) > 0 {
		httputil.WriteError(w, errs[0].Error(), http.StatusBadGateway)
		return
	}

	// Export roles (from auth middleware's rolesStore, may be nil if auth is off)
	var roles []RolesEntry
	if authMiddleware != nil && authMiddleware.rolesStore != nil {
		roles = authMiddleware.rolesStore.All()
	}

	// Export invites
	var invites []InviteExport
	if inviteStore != nil {
		for _, iws := range inviteStore.ListInvites() {
			invites = append(invites, InviteExport{
				Token:     iws.Token,
				Label:     iws.Label,
				CreatedAt: iws.CreatedAt,
				CreatedBy: iws.CreatedBy,
				ExpiresAt: iws.ExpiresAt,
				MaxUses:   iws.MaxUses,
				Uses:      iws.Uses,
				Revoked:   iws.Revoked,
			})
		}
	}

	// Export requests
	var requests []RequestExport
	if requestStore != nil {
		for _, req := range requestStore.all() {
			requests = append(requests, RequestExport{
				ID:          req.ID,
				Type:        req.Type,
				TmdbID:      req.TmdbID,
				TvdbID:      req.TvdbID,
				Title:       req.Title,
				Year:        req.Year,
				Poster:      req.Poster,
				RequestedBy: req.RequestedBy,
				State:       req.State,
				Reason:      req.Reason,
				ArrID:       req.ArrID,
				CreatedAt:   req.CreatedAt,
				UpdatedAt:   req.UpdatedAt,
				History:     req.History,
			})
		}
	}

	export := BackupExport{
		Version:  currentBackupVersion,
		Exported: time.Now().UTC().Format(time.RFC3339),
		Movies:   movies,
		Series:   series,
		Roles:    roles,
		Invites:  invites,
		Requests: requests,
	}

	slog.Info("export complete", "component", "export",
		"movies", len(movies), "series", len(series),
		"roles", len(roles), "invites", len(invites), "requests", len(requests))

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="pelicula-backup-%s.json"`,
			time.Now().UTC().Format("2006-01-02")))
	json.NewEncoder(w).Encode(export)
}

func handleImportBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 100<<20) // 100 MB
	var backup BackupExport
	if err := json.NewDecoder(r.Body).Decode(&backup); err != nil {
		httputil.WriteError(w, "invalid backup JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if backup.Version < 1 || backup.Version > currentBackupVersion {
		httputil.WriteError(w, fmt.Sprintf("unsupported backup version %d", backup.Version), http.StatusBadRequest)
		return
	}

	sonarrKey, radarrKey, _ := services.Keys()
	if radarrKey == "" || sonarrKey == "" {
		httputil.WriteError(w, "API keys not loaded — is the stack wired?", http.StatusServiceUnavailable)
		return
	}

	result := &ImportResult{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		importMovies(radarrKey, backup.Movies, result, &mu)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		importSeries(sonarrKey, backup.Series, result, &mu)
	}()

	wg.Wait()

	// v2+ backups may include roles, invites, and requests.
	if backup.Version >= 2 {
		importRoles(backup.Roles, result)
		importInvites(backup.Invites, result)
		importRequests(backup.Requests, result)
	}

	slog.Info("import complete", "component", "export",
		"movies_added", result.MoviesAdded, "movies_skipped", result.MoviesSkipped,
		"series_added", result.SeriesAdded, "series_skipped", result.SeriesSkipped,
		"errors", len(result.Errors))

	httputil.WriteJSON(w, result)
}

// importRoles upserts roles from a v2 backup into the roles store.
func importRoles(roles []RolesEntry, result *ImportResult) {
	if len(roles) == 0 {
		return
	}
	if authMiddleware == nil || authMiddleware.rolesStore == nil {
		slog.Warn("roles import skipped: rolesStore not available (auth mode is not jellyfin)", "component", "export")
		return
	}
	for _, entry := range roles {
		if err := authMiddleware.rolesStore.Upsert(entry.JellyfinID, entry.Username, entry.Role); err != nil {
			slog.Warn("failed to upsert role from backup", "component", "export",
				"jellyfin_id", entry.JellyfinID, "error", err)
			result.Errors = append(result.Errors, fmt.Sprintf("role %q (id:%s): %v", entry.Username, entry.JellyfinID, err))
		}
	}
}

// importInvites inserts invites from a v2 backup, skipping tokens that already exist.
func importInvites(invites []InviteExport, result *ImportResult) {
	if len(invites) == 0 || inviteStore == nil {
		return
	}
	for _, inv := range invites {
		if err := inviteStore.InsertFull(inv); err != nil {
			slog.Warn("failed to insert invite from backup", "component", "export",
				"token", inv.Token[:8]+"...", "error", err)
			// Don't add to errors — duplicate tokens are expected and silently skipped
		}
	}
}

// importRequests inserts requests from a v2 backup, skipping IDs that already exist.
func importRequests(requests []RequestExport, result *ImportResult) {
	if len(requests) == 0 || requestStore == nil {
		return
	}
	for _, req := range requests {
		if err := requestStore.InsertFull(req); err != nil {
			slog.Warn("failed to insert request from backup", "component", "export",
				"id", req.ID, "error", err)
			// Don't add to errors — duplicate IDs are expected and silently skipped
		}
	}
}

// ── Export helpers ────────────────────────────────────────────────────────────

func exportMovies(apiKey string) ([]MovieExport, error) {
	// Quality profiles: id → name
	profMap, err := loadProfileMap(radarrURL, apiKey)
	if err != nil {
		return nil, fmt.Errorf("quality profiles: %w", err)
	}

	// Tags: id → label
	tagMap, err := loadTagMap(radarrURL, apiKey)
	if err != nil {
		return nil, fmt.Errorf("tags: %w", err)
	}

	data, err := services.ArrGet(radarrURL, apiKey, "/api/v3/movie")
	if err != nil {
		return nil, fmt.Errorf("movies: %w", err)
	}

	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse movies: %w", err)
	}

	out := make([]MovieExport, 0, len(raw))
	for _, m := range raw {
		me := MovieExport{
			Title:          strVal(m, "title"),
			Year:           int(floatVal(m, "year")),
			TmdbID:         int(floatVal(m, "tmdbId")),
			ImdbID:         strVal(m, "imdbId"),
			Path:           strVal(m, "path"),
			QualityProfile: profMap[int(floatVal(m, "qualityProfileId"))],
			Monitored:      boolField(m, "monitored"),
			HasFile:        boolField(m, "hasFile"),
			Tags:           resolveTagLabels(m, tagMap),
		}

		// File info from movieFile object
		if mf, ok := m["movieFile"].(map[string]any); ok {
			fi := &FileInfoExport{
				RelativePath: strVal(mf, "relativePath"),
				Size:         intVal(mf, "size"),
			}
			if q, ok := mf["quality"].(map[string]any); ok {
				if qi, ok := q["quality"].(map[string]any); ok {
					fi.Quality = strVal(qi, "name")
				}
			}
			me.FileInfo = fi
		}

		out = append(out, me)
	}
	return out, nil
}

func exportSeries(apiKey string) ([]SeriesExport, error) {
	profMap, err := loadProfileMap(sonarrURL, apiKey)
	if err != nil {
		return nil, fmt.Errorf("quality profiles: %w", err)
	}

	tagMap, err := loadTagMap(sonarrURL, apiKey)
	if err != nil {
		return nil, fmt.Errorf("tags: %w", err)
	}

	data, err := services.ArrGet(sonarrURL, apiKey, "/api/v3/series")
	if err != nil {
		return nil, fmt.Errorf("series: %w", err)
	}

	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse series: %w", err)
	}

	out := make([]SeriesExport, 0, len(raw))
	for _, s := range raw {
		se := SeriesExport{
			Title:          strVal(s, "title"),
			Year:           int(floatVal(s, "year")),
			TvdbID:         int(floatVal(s, "tvdbId")),
			TmdbID:         int(floatVal(s, "tmdbId")),
			ImdbID:         strVal(s, "imdbId"),
			Path:           strVal(s, "path"),
			QualityProfile: profMap[int(floatVal(s, "qualityProfileId"))],
			Monitored:      boolField(s, "monitored"),
			HasFile: func() bool {
				if stats, ok := s["statistics"].(map[string]any); ok {
					return boolField(stats, "hasFile")
				}
				return false
			}(),
			Tags:    resolveTagLabels(s, tagMap),
			Seasons: extractSeasons(s),
		}
		out = append(out, se)
	}
	return out, nil
}

// ── Import helpers ────────────────────────────────────────────────────────────

func importMovies(apiKey string, movies []MovieExport, result *ImportResult, mu *sync.Mutex) {
	// Existing movies: tmdbId → true
	existing := loadExistingMovieIDs(apiKey)

	// Quality profile name → id
	profMap, _ := loadProfileNameMap(radarrURL, apiKey)

	// Tag label → id (creating missing tags)
	tagMap, _ := ensureTags(radarrURL, apiKey, collectMovieTags(movies))

	for _, m := range movies {
		if m.TmdbID == 0 {
			mu.Lock()
			result.MoviesFailed++
			result.Errors = append(result.Errors, fmt.Sprintf("movie %q: no tmdbId", m.Title))
			mu.Unlock()
			continue
		}
		if existing[m.TmdbID] {
			mu.Lock()
			result.MoviesSkipped++
			mu.Unlock()
			continue
		}

		profileID := resolveProfileID(m.QualityProfile, profMap)
		tagIDs := resolveTagIDs(m.Tags, tagMap)

		payload := map[string]any{
			"tmdbId":           m.TmdbID,
			"title":            m.Title,
			"year":             m.Year,
			"qualityProfileId": profileID,
			"rootFolderPath":   "/movies",
			"monitored":        m.Monitored,
			"tags":             tagIDs,
			"addOptions": map[string]any{
				"searchForMovie": false,
			},
		}

		if _, err := services.ArrPost(radarrURL, apiKey, "/api/v3/movie", payload); err != nil {
			mu.Lock()
			result.MoviesFailed++
			result.Errors = append(result.Errors, fmt.Sprintf("movie %q (tmdb:%d): %v", m.Title, m.TmdbID, err))
			mu.Unlock()
			continue
		}

		mu.Lock()
		result.MoviesAdded++
		mu.Unlock()
	}
}

func importSeries(apiKey string, series []SeriesExport, result *ImportResult, mu *sync.Mutex) {
	// Existing series: tvdbId → true
	existing := loadExistingSeriesIDs(apiKey)

	// Quality profile name → id
	profMap, _ := loadProfileNameMap(sonarrURL, apiKey)

	// Tag label → id (creating missing tags)
	tagMap, _ := ensureTags(sonarrURL, apiKey, collectSeriesTags(series))

	for _, s := range series {
		if s.TvdbID == 0 {
			mu.Lock()
			result.SeriesFailed++
			result.Errors = append(result.Errors, fmt.Sprintf("series %q: no tvdbId", s.Title))
			mu.Unlock()
			continue
		}
		if existing[s.TvdbID] {
			mu.Lock()
			result.SeriesSkipped++
			mu.Unlock()
			continue
		}

		profileID := resolveProfileID(s.QualityProfile, profMap)
		tagIDs := resolveTagIDs(s.Tags, tagMap)

		seasons := make([]map[string]any, 0, len(s.Seasons))
		for _, season := range s.Seasons {
			seasons = append(seasons, map[string]any{
				"seasonNumber": season.SeasonNumber,
				"monitored":    season.Monitored,
			})
		}

		payload := map[string]any{
			"tvdbId":           s.TvdbID,
			"title":            s.Title,
			"year":             s.Year,
			"qualityProfileId": profileID,
			"rootFolderPath":   "/tv",
			"monitored":        s.Monitored,
			"seasonFolder":     true,
			"tags":             tagIDs,
			"seasons":          seasons,
			"addOptions": map[string]any{
				"searchForMissingEpisodes": false,
			},
		}

		if _, err := services.ArrPost(sonarrURL, apiKey, "/api/v3/series", payload); err != nil {
			mu.Lock()
			result.SeriesFailed++
			result.Errors = append(result.Errors, fmt.Sprintf("series %q (tvdb:%d): %v", s.Title, s.TvdbID, err))
			mu.Unlock()
			continue
		}

		mu.Lock()
		result.SeriesAdded++
		mu.Unlock()
	}
}

// ── Shared helpers ────────────────────────────────────────────────────────────

// loadProfileMap returns id → name for quality profiles.
func loadProfileMap(baseURL, apiKey string) (map[int]string, error) {
	data, err := services.ArrGet(baseURL, apiKey, "/api/v3/qualityprofile")
	if err != nil {
		return nil, err
	}
	var profiles []map[string]any
	if err := json.Unmarshal(data, &profiles); err != nil {
		return nil, err
	}
	m := make(map[int]string, len(profiles))
	for _, p := range profiles {
		m[int(floatVal(p, "id"))] = strVal(p, "name")
	}
	return m, nil
}

// loadProfileNameMap returns name → id for quality profiles.
func loadProfileNameMap(baseURL, apiKey string) (map[string]int, error) {
	data, err := services.ArrGet(baseURL, apiKey, "/api/v3/qualityprofile")
	if err != nil {
		return nil, err
	}
	var profiles []map[string]any
	if err := json.Unmarshal(data, &profiles); err != nil {
		return nil, err
	}
	m := make(map[string]int, len(profiles))
	for _, p := range profiles {
		m[strVal(p, "name")] = int(floatVal(p, "id"))
	}
	return m, nil
}

// loadTagMap returns id → label for tags.
func loadTagMap(baseURL, apiKey string) (map[int]string, error) {
	data, err := services.ArrGet(baseURL, apiKey, "/api/v3/tag")
	if err != nil {
		return nil, err
	}
	var tags []map[string]any
	if err := json.Unmarshal(data, &tags); err != nil {
		return nil, err
	}
	m := make(map[int]string, len(tags))
	for _, t := range tags {
		m[int(floatVal(t, "id"))] = strVal(t, "label")
	}
	return m, nil
}

// ensureTags creates any missing tags and returns label → id map.
func ensureTags(baseURL, apiKey string, labels []string) (map[string]int, error) {
	data, err := services.ArrGet(baseURL, apiKey, "/api/v3/tag")
	if err != nil {
		return nil, err
	}
	var existing []map[string]any
	if err := json.Unmarshal(data, &existing); err != nil {
		return nil, err
	}
	m := make(map[string]int, len(existing))
	for _, t := range existing {
		m[strVal(t, "label")] = int(floatVal(t, "id"))
	}

	for _, label := range labels {
		if _, ok := m[label]; ok {
			continue
		}
		resp, err := services.ArrPost(baseURL, apiKey, "/api/v3/tag", map[string]any{"label": label})
		if err != nil {
			continue // non-fatal: missing tag just won't be applied
		}
		var created map[string]any
		if err := json.Unmarshal(resp, &created); err == nil {
			m[label] = int(floatVal(created, "id"))
		}
	}
	return m, nil
}

// loadExistingMovieIDs returns a set of tmdbIds already in Radarr.
func loadExistingMovieIDs(apiKey string) map[int]bool {
	data, err := services.ArrGet(radarrURL, apiKey, "/api/v3/movie")
	if err != nil {
		return nil
	}
	var movies []map[string]any
	if err := json.Unmarshal(data, &movies); err != nil {
		return nil
	}
	m := make(map[int]bool, len(movies))
	for _, mv := range movies {
		m[int(floatVal(mv, "tmdbId"))] = true
	}
	return m
}

// loadExistingSeriesIDs returns a set of tvdbIds already in Sonarr.
func loadExistingSeriesIDs(apiKey string) map[int]bool {
	data, err := services.ArrGet(sonarrURL, apiKey, "/api/v3/series")
	if err != nil {
		return nil
	}
	var series []map[string]any
	if err := json.Unmarshal(data, &series); err != nil {
		return nil
	}
	m := make(map[int]bool, len(series))
	for _, s := range series {
		m[int(floatVal(s, "tvdbId"))] = true
	}
	return m
}

// resolveProfileID looks up profile ID by name.
// If not found, logs a warning and falls back to the first available profile
// (or 1 if the map is empty). The warning makes profile mismatches visible.
func resolveProfileID(name string, nameMap map[string]int) int {
	if id, ok := nameMap[name]; ok {
		return id
	}
	// Fall back to first available profile
	for _, id := range nameMap {
		slog.Warn("quality profile not found, using fallback",
			"component", "export", "requested", name, "fallback_id", id)
		return id
	}
	slog.Warn("quality profile not found and no profiles available, using id=1",
		"component", "export", "requested", name)
	return 1
}

// resolveTagIDs converts tag labels to IDs.
func resolveTagIDs(labels []string, labelMap map[string]int) []int {
	ids := make([]int, 0, len(labels))
	for _, label := range labels {
		if id, ok := labelMap[label]; ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// resolveTagLabels converts tag IDs from a raw *arr object to label strings.
func resolveTagLabels(m map[string]any, tagMap map[int]string) []string {
	rawTags, _ := m["tags"].([]any)
	labels := make([]string, 0, len(rawTags))
	for _, t := range rawTags {
		if id, ok := t.(float64); ok {
			if label, ok := tagMap[int(id)]; ok {
				labels = append(labels, label)
			}
		}
	}
	return labels
}

// extractSeasons pulls season monitored status from a raw Sonarr series object.
func extractSeasons(s map[string]any) []SeasonExport {
	rawSeasons, _ := s["seasons"].([]any)
	out := make([]SeasonExport, 0, len(rawSeasons))
	for _, rs := range rawSeasons {
		sm, ok := rs.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, SeasonExport{
			SeasonNumber: int(floatVal(sm, "seasonNumber")),
			Monitored:    boolField(sm, "monitored"),
		})
	}
	return out
}

// boolField reads a bool from a map[string]any.
func boolField(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

// collectMovieTags gathers all unique tag labels from a movie list.
func collectMovieTags(movies []MovieExport) []string {
	return uniqueStrings(func(add func(string)) {
		for _, m := range movies {
			for _, t := range m.Tags {
				add(t)
			}
		}
	})
}

// collectSeriesTags gathers all unique tag labels from a series list.
func collectSeriesTags(series []SeriesExport) []string {
	return uniqueStrings(func(add func(string)) {
		for _, s := range series {
			for _, t := range s.Tags {
				add(t)
			}
		}
	})
}

// uniqueStrings deduplicates strings produced by the given closure.
func uniqueStrings(populate func(func(string))) []string {
	seen := make(map[string]struct{})
	var out []string
	populate(func(s string) {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	})
	return out
}
