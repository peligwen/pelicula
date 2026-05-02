package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"pelicula-api/httputil"
	"pelicula-api/internal/peligrosa"
)

// HandleImportBackup serves POST /api/pelicula/import-backup — restores from a
// previously exported JSON backup file.
func (h *Handler) HandleImportBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 100<<20) // 100 MB
	var bk BackupExport
	if err := json.NewDecoder(r.Body).Decode(&bk); err != nil {
		httputil.WriteError(w, "invalid backup JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if bk.Version < 1 || bk.Version > currentBackupVersion {
		httputil.WriteError(w, fmt.Sprintf("unsupported backup version %d", bk.Version), http.StatusBadRequest)
		return
	}

	sonarrKey, radarrKey, _ := h.Svc.Keys()
	if radarrKey == "" || sonarrKey == "" {
		httputil.WriteError(w, "API keys not loaded — is the stack wired?", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	result := &ImportResult{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.importMovies(ctx, radarrKey, bk.Movies, result, &mu)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.importSeries(ctx, sonarrKey, bk.Series, result, &mu)
	}()

	wg.Wait()

	// v2+ backups may include roles, invites, and requests.
	if bk.Version >= 2 {
		h.importRoles(ctx, bk.Roles, result)
		h.importInvites(ctx, bk.Invites, result)
		h.importRequests(ctx, bk.Requests, result)
	}

	slog.Info("import complete", "component", "export",
		"movies_added", result.MoviesAdded, "movies_skipped", result.MoviesSkipped,
		"series_added", result.SeriesAdded, "series_skipped", result.SeriesSkipped,
		"errors", len(result.Errors))

	httputil.WriteJSON(w, result)
}

// importRoles upserts roles from a v2 backup into the roles store.
func (h *Handler) importRoles(ctx context.Context, roles []peligrosa.RolesEntry, result *ImportResult) {
	if len(roles) == 0 {
		return
	}
	if h.Auth == nil || h.Auth.Roles() == nil {
		slog.Warn("roles import skipped: rolesStore not available (auth mode is not jellyfin)", "component", "export")
		return
	}
	for _, entry := range roles {
		if err := h.Auth.Roles().Upsert(ctx, entry.JellyfinID, entry.Username, entry.Role); err != nil {
			slog.Warn("failed to upsert role from backup", "component", "export",
				"jellyfin_id", entry.JellyfinID, "error", err)
			result.Errors = append(result.Errors, fmt.Sprintf("role %q (id:%s): %v", entry.Username, entry.JellyfinID, err))
		}
	}
}

// importInvites inserts invites from a v2 backup, skipping tokens that already exist.
func (h *Handler) importInvites(ctx context.Context, invites []peligrosa.InviteExport, result *ImportResult) {
	if len(invites) == 0 || h.Invites == nil {
		return
	}
	for _, inv := range invites {
		if err := h.Invites.InsertFull(ctx, inv); err != nil {
			slog.Warn("failed to insert invite from backup", "component", "export",
				"token", fmt.Sprintf("%.8s...", inv.Token), "error", err)
			// Don't add to errors — duplicate tokens are expected and silently skipped
		}
	}
}

// importRequests inserts requests from a v2 backup, skipping IDs that already exist.
func (h *Handler) importRequests(ctx context.Context, requests []peligrosa.RequestExport, result *ImportResult) {
	if len(requests) == 0 || h.Requests == nil {
		return
	}
	for _, req := range requests {
		if err := h.Requests.InsertFull(ctx, req); err != nil {
			slog.Warn("failed to insert request from backup", "component", "export",
				"id", req.ID, "error", err)
			// Don't add to errors — duplicate IDs are expected and silently skipped
		}
	}
}

// importMovies restores movies from a backup into Radarr.
func (h *Handler) importMovies(ctx context.Context, apiKey string, movies []MovieExport, result *ImportResult, mu *sync.Mutex) {
	_ = ctx // wired for when ArrClient adopts ctx (R16.5)
	existing := h.loadExistingMovieIDs(apiKey)
	profMap, _ := h.loadProfileNameMap(h.RadarrURL, apiKey)
	tagMap, _ := h.ensureTags(h.RadarrURL, apiKey, collectMovieTags(movies))

	radarrRoot := h.Lib.FirstLibraryPath("radarr", "/media/movies")

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

		profileID := ResolveProfileID(m.QualityProfile, profMap)
		tagIDs := ResolveTagIDs(m.Tags, tagMap)

		payload := map[string]any{
			"tmdbId":           m.TmdbID,
			"title":            m.Title,
			"year":             m.Year,
			"qualityProfileId": profileID,
			"rootFolderPath":   radarrRoot,
			"monitored":        m.Monitored,
			"tags":             tagIDs,
			"addOptions": map[string]any{
				"searchForMovie": false,
			},
		}

		if _, err := h.Svc.ArrPost(h.RadarrURL, apiKey, "/api/v3/movie", payload); err != nil {
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

// importSeries restores series from a backup into Sonarr.
func (h *Handler) importSeries(ctx context.Context, apiKey string, series []SeriesExport, result *ImportResult, mu *sync.Mutex) {
	_ = ctx // wired for when ArrClient adopts ctx (R16.5)
	existing := h.loadExistingSeriesIDs(apiKey)
	profMap, _ := h.loadProfileNameMap(h.SonarrURL, apiKey)
	tagMap, _ := h.ensureTags(h.SonarrURL, apiKey, collectSeriesTags(series))

	sonarrRoot := h.Lib.FirstLibraryPath("sonarr", "/media/tv")

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

		profileID := ResolveProfileID(s.QualityProfile, profMap)
		tagIDs := ResolveTagIDs(s.Tags, tagMap)

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
			"rootFolderPath":   sonarrRoot,
			"monitored":        s.Monitored,
			"seasonFolder":     true,
			"tags":             tagIDs,
			"seasons":          seasons,
			"addOptions": map[string]any{
				"searchForMissingEpisodes": false,
			},
		}

		if _, err := h.Svc.ArrPost(h.SonarrURL, apiKey, "/api/v3/series", payload); err != nil {
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

// ── Restore-only map helpers ──────────────────────────────────────────────────

// loadProfileNameMap returns name → id for quality profiles (used during restore).
func (h *Handler) loadProfileNameMap(baseURL, apiKey string) (map[string]int, error) {
	data, err := h.Svc.ArrGet(baseURL, apiKey, "/api/v3/qualityprofile")
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

// ensureTags creates any missing tags and returns label → id map.
func (h *Handler) ensureTags(baseURL, apiKey string, labels []string) (map[string]int, error) {
	data, err := h.Svc.ArrGet(baseURL, apiKey, "/api/v3/tag")
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
		resp, err := h.Svc.ArrPost(baseURL, apiKey, "/api/v3/tag", map[string]any{"label": label})
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
func (h *Handler) loadExistingMovieIDs(apiKey string) map[int]bool {
	data, err := h.Svc.ArrGet(h.RadarrURL, apiKey, "/api/v3/movie")
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
func (h *Handler) loadExistingSeriesIDs(apiKey string) map[int]bool {
	data, err := h.Svc.ArrGet(h.SonarrURL, apiKey, "/api/v3/series")
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

// ── Pure helpers (no receiver) ────────────────────────────────────────────────

// ResolveProfileID looks up profile ID by name.
// If not found, logs a warning and falls back to the first available profile
// (or 1 if the map is empty). The warning makes profile mismatches visible.
func ResolveProfileID(name string, nameMap map[string]int) int {
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

// ResolveTagIDs converts tag labels to IDs.
func ResolveTagIDs(labels []string, labelMap map[string]int) []int {
	ids := make([]int, 0, len(labels))
	for _, label := range labels {
		if id, ok := labelMap[label]; ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// collectMovieTags gathers all unique tag labels from a movie list.
func collectMovieTags(movies []MovieExport) []string {
	return UniqueStrings(func(add func(string)) {
		for _, m := range movies {
			for _, t := range m.Tags {
				add(t)
			}
		}
	})
}

// collectSeriesTags gathers all unique tag labels from a series list.
func collectSeriesTags(series []SeriesExport) []string {
	return UniqueStrings(func(add func(string)) {
		for _, s := range series {
			for _, t := range s.Tags {
				add(t)
			}
		}
	})
}
