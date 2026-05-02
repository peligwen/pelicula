package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"pelicula-api/httputil"
	"pelicula-api/internal/peligrosa"
)

// HandleExport serves GET /api/pelicula/export — streams the full backup as JSON.
func (h *Handler) HandleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sonarrKey, radarrKey, _ := h.Svc.Keys()
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

	wg.Add(1)
	go func() {
		defer wg.Done()
		ms, err := h.exportMovies(radarrKey)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Errorf("radarr: %w", err))
			return
		}
		movies = ms
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		ss, err := h.exportSeries(sonarrKey)
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
	var roles []peligrosa.RolesEntry
	if h.Auth != nil && h.Auth.Roles() != nil {
		roles = h.Auth.Roles().All()
	}

	// Export invites
	var invites []peligrosa.InviteExport
	if h.Invites != nil {
		for _, iws := range h.Invites.ListInvites(r.Context()) {
			invites = append(invites, peligrosa.InviteExport{
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
	var requests []peligrosa.RequestExport
	if h.Requests != nil {
		for _, req := range h.Requests.All(context.Background()) {
			requests = append(requests, peligrosa.RequestExport{
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
	json.NewEncoder(w).Encode(export) //nolint:errcheck
}

// exportMovies fetches all movies from Radarr and maps them to MovieExport.
func (h *Handler) exportMovies(apiKey string) ([]MovieExport, error) {
	profMap, err := h.loadProfileMap(h.RadarrURL, apiKey)
	if err != nil {
		return nil, fmt.Errorf("quality profiles: %w", err)
	}

	tagMap, err := h.loadTagMap(h.RadarrURL, apiKey)
	if err != nil {
		return nil, fmt.Errorf("tags: %w", err)
	}

	data, err := h.Svc.ArrGet(h.RadarrURL, apiKey, "/api/v3/movie")
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
			Tags:           ResolveTagLabels(m, tagMap),
		}

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

// exportSeries fetches all series from Sonarr and maps them to SeriesExport.
func (h *Handler) exportSeries(apiKey string) ([]SeriesExport, error) {
	profMap, err := h.loadProfileMap(h.SonarrURL, apiKey)
	if err != nil {
		return nil, fmt.Errorf("quality profiles: %w", err)
	}

	tagMap, err := h.loadTagMap(h.SonarrURL, apiKey)
	if err != nil {
		return nil, fmt.Errorf("tags: %w", err)
	}

	data, err := h.Svc.ArrGet(h.SonarrURL, apiKey, "/api/v3/series")
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
			Tags:    ResolveTagLabels(s, tagMap),
			Seasons: ExtractSeasons(s),
		}
		out = append(out, se)
	}
	return out, nil
}

// ── Shared map helpers ────────────────────────────────────────────────────────

// loadProfileMap returns id → name for quality profiles.
func (h *Handler) loadProfileMap(baseURL, apiKey string) (map[int]string, error) {
	data, err := h.Svc.ArrGet(baseURL, apiKey, "/api/v3/qualityprofile")
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

// loadTagMap returns id → label for tags.
func (h *Handler) loadTagMap(baseURL, apiKey string) (map[int]string, error) {
	data, err := h.Svc.ArrGet(baseURL, apiKey, "/api/v3/tag")
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

// ── Pure helpers (no receiver) ────────────────────────────────────────────────

// ResolveTagLabels converts tag IDs from a raw *arr object to label strings.
func ResolveTagLabels(m map[string]any, tagMap map[int]string) []string {
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

// ExtractSeasons pulls season monitored status from a raw Sonarr series object.
func ExtractSeasons(s map[string]any) []SeasonExport {
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

// strVal extracts a string from a map[string]any; returns "" if absent or wrong type.
func strVal(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// floatVal extracts a float64 from a map[string]any; returns 0 if absent or wrong type.
func floatVal(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

// intVal extracts an int64 from a float64-typed map value (JSON numbers decode as float64).
func intVal(m map[string]any, key string) int64 {
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	return 0
}

// boolField reads a bool from a map[string]any.
func boolField(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

// UniqueStrings deduplicates strings produced by the given closure, preserving order.
func UniqueStrings(populate func(func(string))) []string {
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
