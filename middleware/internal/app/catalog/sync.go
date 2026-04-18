package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"
)

type jellyfinItem struct {
	ID          string            `json:"Id"`
	Overview    string            `json:"Overview"`
	ImageTags   map[string]string `json:"ImageTags"`
	ProviderIDs map[string]string `json:"ProviderIds"`
}

const jellyfinCacheTTL = 5 * time.Minute

const jellyfinServiceUser = "pelicula-internal"

// ProculaJobSource carries the parsed fields from a Procula import hook payload.
// Used by UpsertFromHook to create catalog records when a download completes,
// and forwarded verbatim to Procula's jobs endpoint.
type ProculaJobSource struct {
	Type                   string `json:"type"`
	Title                  string `json:"title"`
	Year                   int    `json:"year"`
	Path                   string `json:"path"`
	Size                   int64  `json:"size"`
	ArrID                  int    `json:"arr_id"`
	ArrType                string `json:"arr_type"`
	EpisodeID              int    `json:"episode_id,omitempty"`
	SeasonNumber           int    `json:"season_number,omitempty"`
	EpisodeNumber          int    `json:"episode_number,omitempty"`
	TmdbID                 int    `json:"tmdb_id,omitempty"`
	TvdbID                 int    `json:"tvdb_id,omitempty"`
	DownloadHash           string `json:"download_hash"`
	ExpectedRuntimeMinutes int    `json:"expected_runtime_minutes"`
}

// floatVal is a helper for extracting float64 from map[string]any.
func floatVal(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

// fetchJellyfinLibrary fetches all Movie/Series items from Jellyfin and caches them.
func (h *Handler) fetchJellyfinLibrary(jf JellyfinMetaClient) ([]jellyfinItem, error) {
	h.jfCache.mu.Lock()
	if time.Since(h.jfCache.fetchedAt) < jellyfinCacheTTL {
		items := h.jfCache.items
		h.jfCache.mu.Unlock()
		return items, nil
	}
	h.jfCache.mu.Unlock()

	userID := jf.GetJellyfinUserID()
	if userID == "" {
		// Try to resolve from the API
		body, err := jf.JellyfinGet("/Users", jf.GetJellyfinAPIKey())
		if err != nil {
			return nil, fmt.Errorf("jellyfin users list: %w", err)
		}
		var users []struct {
			ID   string `json:"Id"`
			Name string `json:"Name"`
		}
		if err := json.Unmarshal(body, &users); err != nil {
			return nil, fmt.Errorf("jellyfin users parse: %w", err)
		}
		for _, u := range users {
			if u.Name == jellyfinServiceUser {
				userID = u.ID
				break
			}
		}
		if userID == "" && len(users) > 0 {
			userID = users[0].ID
		}
		if userID == "" {
			return nil, fmt.Errorf("no Jellyfin users found")
		}
		jf.SetJellyfinUserID(userID)
	}

	jellyfinLibraryCap := 5000
	if v := os.Getenv("JELLYFIN_LIBRARY_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			jellyfinLibraryCap = n
		}
	}

	path := fmt.Sprintf("/Users/%s/Items?IncludeItemTypes=Movie,Series&Fields=Overview,ImageTags,ProviderIds&Recursive=true&Limit=%d", userID, jellyfinLibraryCap)
	body, err := jf.JellyfinGet(path, jf.GetJellyfinAPIKey())
	if err != nil {
		return nil, fmt.Errorf("jellyfin library fetch: %w", err)
	}

	var resp struct {
		Items []jellyfinItem `json:"Items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("jellyfin library parse: %w", err)
	}

	count := len(resp.Items)
	pct := float64(count) / float64(jellyfinLibraryCap) * 100
	if pct >= 80 {
		slog.Warn("jellyfin library response near cap",
			"component", "catalog_sync",
			"count", count,
			"cap", jellyfinLibraryCap,
			"pct", fmt.Sprintf("%.0f%%", pct),
			"hint", "consider increasing JELLYFIN_LIBRARY_LIMIT",
		)
	}

	h.jfCache.mu.Lock()
	if time.Since(h.jfCache.fetchedAt) >= jellyfinCacheTTL {
		h.jfCache.items = resp.Items
		h.jfCache.fetchedAt = time.Now()
		slog.Info("jellyfin library cached", "component", "catalog_sync", "count", count)
	}
	items := h.jfCache.items
	h.jfCache.mu.Unlock()
	return items, nil
}

// UpsertFromHook creates or updates catalog records when a download completes.
func UpsertFromHook(ctx context.Context, db *sql.DB, source ProculaJobSource) error {
	switch source.Type {
	case "movie":
		_, err := UpsertCatalogItem(ctx, db, CatalogItem{
			Type:     "movie",
			TmdbID:   source.TmdbID,
			ArrID:    source.ArrID,
			ArrType:  source.ArrType,
			Title:    source.Title,
			Year:     source.Year,
			Tier:     "pipeline",
			FilePath: source.Path,
		})
		if err != nil {
			return fmt.Errorf("upsert movie: %w", err)
		}
		return nil

	case "episode":
		seriesID, err := UpsertCatalogItem(ctx, db, CatalogItem{
			Type:    "series",
			TvdbID:  source.TvdbID,
			TmdbID:  source.TmdbID,
			ArrID:   source.ArrID,
			ArrType: source.ArrType,
			Title:   source.Title,
			Year:    source.Year,
			Tier:    "pipeline",
		})
		if err != nil {
			return fmt.Errorf("upsert series: %w", err)
		}

		seasonTitle := fmt.Sprintf("%s Season %d", source.Title, source.SeasonNumber)
		seasonID, err := UpsertCatalogItem(ctx, db, CatalogItem{
			Type:         "season",
			ParentID:     seriesID,
			SeasonNumber: source.SeasonNumber,
			Title:        seasonTitle,
			Year:         source.Year,
			Tier:         "pipeline",
		})
		if err != nil {
			return fmt.Errorf("upsert season: %w", err)
		}

		_, err = UpsertCatalogItem(ctx, db, CatalogItem{
			Type:          "episode",
			ParentID:      seasonID,
			EpisodeID:     source.EpisodeID,
			SeasonNumber:  source.SeasonNumber,
			EpisodeNumber: source.EpisodeNumber,
			ArrType:       source.ArrType,
			Title:         source.Title,
			Year:          source.Year,
			Tier:          "pipeline",
			FilePath:      source.Path,
		})
		if err != nil {
			return fmt.Errorf("upsert episode: %w", err)
		}
		return nil

	default:
		return fmt.Errorf("UpsertFromHook: unknown source type %q", source.Type)
	}
}

// BackfillFromArr scans all movies in Radarr and all series in Sonarr,
// upserting catalog records for items in the existing library.
func BackfillFromArr(ctx context.Context, db *sql.DB, svc ArrClient, radarrURL, sonarrURL string) error {
	sonarrKey, radarrKey, _ := svc.Keys()

	if radarrKey != "" {
		if err := backfillRadarr(ctx, db, svc, radarrURL, radarrKey); err != nil {
			slog.Error("backfill radarr failed", "component", "catalog_sync", "error", err)
		}
	}
	if sonarrKey != "" {
		if err := backfillSonarr(ctx, db, svc, sonarrURL, sonarrKey); err != nil {
			slog.Error("backfill sonarr failed", "component", "catalog_sync", "error", err)
		}
	}
	slog.Info("catalog backfill complete", "component", "catalog_sync")
	return nil
}

func backfillRadarr(ctx context.Context, db *sql.DB, svc ArrClient, radarrURL, apiKey string) error {
	data, err := svc.ArrGet(radarrURL, apiKey, "/api/v3/movie")
	if err != nil {
		return fmt.Errorf("radarr list: %w", err)
	}
	var movies []map[string]any
	if err := json.Unmarshal(data, &movies); err != nil {
		return fmt.Errorf("radarr parse: %w", err)
	}

	for _, m := range movies {
		title, _ := m["title"].(string)
		if title == "" {
			continue
		}
		tier := "queue"
		hasFile, _ := m["hasFile"].(bool)
		if hasFile {
			tier = "library"
		}
		filePath := ""
		if mf, ok := m["movieFile"].(map[string]any); ok {
			filePath, _ = mf["path"].(string)
		}
		if _, err := UpsertCatalogItem(ctx, db, CatalogItem{
			Type:     "movie",
			TmdbID:   int(floatVal(m, "tmdbId")),
			ArrID:    int(floatVal(m, "id")),
			ArrType:  "radarr",
			Title:    title,
			Year:     int(floatVal(m, "year")),
			Tier:     tier,
			FilePath: filePath,
		}); err != nil {
			slog.Error("backfill: upsert movie", "component", "catalog_sync", "title", title, "error", err)
		}
	}
	slog.Info("backfill radarr complete", "component", "catalog_sync", "count", len(movies))
	return nil
}

const episodeConcurrency = 10

func backfillSonarr(ctx context.Context, db *sql.DB, svc ArrClient, sonarrURL, apiKey string) error {
	data, err := svc.ArrGet(sonarrURL, apiKey, "/api/v3/series")
	if err != nil {
		return fmt.Errorf("sonarr list: %w", err)
	}
	var seriesList []map[string]any
	if err := json.Unmarshal(data, &seriesList); err != nil {
		return fmt.Errorf("sonarr parse: %w", err)
	}

	type seriesMeta struct {
		catalogID string
		title     string
		year      int
		tier      string
	}
	seriesByArrID := make(map[int]seriesMeta, len(seriesList))

	for _, s := range seriesList {
		title, _ := s["title"].(string)
		if title == "" {
			continue
		}
		arrID := int(floatVal(s, "id"))
		tvdbID := int(floatVal(s, "tvdbId"))
		tmdbID := int(floatVal(s, "tmdbId"))
		year := int(floatVal(s, "year"))

		tier := "queue"
		if stats, ok := s["statistics"].(map[string]any); ok {
			if int(floatVal(stats, "episodeFileCount")) > 0 {
				tier = "library"
			}
		}

		catalogID, err := UpsertCatalogItem(ctx, db, CatalogItem{
			Type:    "series",
			TvdbID:  tvdbID,
			TmdbID:  tmdbID,
			ArrID:   arrID,
			ArrType: "sonarr",
			Title:   title,
			Year:    year,
			Tier:    tier,
		})
		if err != nil {
			slog.Error("backfill: upsert series", "component", "catalog_sync", "title", title, "error", err)
			continue
		}
		seriesByArrID[arrID] = seriesMeta{catalogID: catalogID, title: title, year: year, tier: tier}
	}

	arrIDs := make([]int, 0, len(seriesByArrID))
	for id := range seriesByArrID {
		arrIDs = append(arrIDs, id)
	}

	var (
		allEpisodesMu sync.Mutex
		allEpisodes   []map[string]any
		fetchErr      error
		fetchErrMu    sync.Mutex
		wg            sync.WaitGroup
		sem           = make(chan struct{}, episodeConcurrency)
	)

	for _, id := range arrIDs {
		fetchErrMu.Lock()
		curErr := fetchErr
		fetchErrMu.Unlock()
		if curErr != nil {
			break
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(arrID int) {
			defer wg.Done()
			defer func() { <-sem }()

			path := "/api/v3/episode?seriesId=" + strconv.Itoa(arrID)
			epData, err := svc.ArrGet(sonarrURL, apiKey, path)
			if err != nil {
				slog.Error("backfill: fetch episodes", "component", "catalog_sync",
					"arr_id", arrID, "error", err)
				fetchErrMu.Lock()
				if fetchErr == nil {
					fetchErr = err
				}
				fetchErrMu.Unlock()
				return
			}
			var batch []map[string]any
			if err := json.Unmarshal(epData, &batch); err != nil {
				slog.Error("backfill: parse episodes", "component", "catalog_sync",
					"arr_id", arrID, "error", err)
				return
			}
			allEpisodesMu.Lock()
			allEpisodes = append(allEpisodes, batch...)
			allEpisodesMu.Unlock()
		}(id)
	}
	wg.Wait()

	if fetchErr != nil {
		return fmt.Errorf("sonarr episode fetch: %w", fetchErr)
	}

	type seasonKey struct {
		arrID     int
		seasonNum int
	}
	seasonsSeen := map[seasonKey]bool{}
	for _, ep := range allEpisodes {
		arrID := int(floatVal(ep, "seriesId"))
		seasonNum := int(floatVal(ep, "seasonNumber"))
		if seasonNum == 0 {
			continue
		}
		key := seasonKey{arrID, seasonNum}
		if seasonsSeen[key] {
			continue
		}
		seasonsSeen[key] = true

		meta, ok := seriesByArrID[arrID]
		if !ok {
			continue
		}
		if _, err := UpsertCatalogItem(ctx, db, CatalogItem{
			Type:         "season",
			ParentID:     meta.catalogID,
			SeasonNumber: seasonNum,
			Title:        fmt.Sprintf("%s Season %d", meta.title, seasonNum),
			Year:         meta.year,
			Tier:         meta.tier,
		}); err != nil {
			slog.Error("backfill: upsert season", "component", "catalog_sync",
				"title", fmt.Sprintf("%s Season %d", meta.title, seasonNum), "error", err)
		}
	}

	slog.Info("backfill sonarr complete", "component", "catalog_sync",
		"series", len(seriesList), "episodes", len(allEpisodes))
	return nil
}

// MaybeSyncJellyfinMetadata syncs Jellyfin metadata for an item if stale (>24h) or never synced.
// Safe to call in a goroutine — logs errors, never panics.
func (h *Handler) MaybeSyncJellyfinMetadata(item *CatalogItem) {
	if item == nil {
		return
	}
	if item.Type != "movie" && item.Type != "series" {
		return
	}
	if item.MetadataSyncedAt != "" {
		t, err := time.Parse(time.RFC3339, item.MetadataSyncedAt)
		if err == nil && time.Since(t) < 24*time.Hour {
			return
		}
	}
	if err := h.SyncJellyfinMetadata(item); err != nil {
		slog.Error("jellyfin metadata sync", "component", "catalog_sync", "id", item.ID, "error", err)
	}
}

// SyncJellyfinMetadata fetches artwork and synopsis from Jellyfin and persists them.
func (h *Handler) SyncJellyfinMetadata(item *CatalogItem) error {
	jellyfinID, artworkURL, synopsis, err := h.fetchJellyfinItemMeta(item)
	if err != nil {
		return err
	}
	syncedAt := time.Now().UTC().Format(time.RFC3339)
	if err := UpdateCatalogMetadata(context.Background(), h.DB, item.ID, jellyfinID, artworkURL, synopsis, syncedAt); err != nil {
		return fmt.Errorf("persist metadata: %w", err)
	}
	if jellyfinID != "" {
		slog.Info("jellyfin metadata synced", "component", "catalog_sync", "id", item.ID, "jellyfin_id", jellyfinID)
	}
	return nil
}

// fetchJellyfinItemMeta looks up a catalog item in the cached Jellyfin library.
func (h *Handler) fetchJellyfinItemMeta(item *CatalogItem) (string, string, string, error) {
	items, err := h.fetchJellyfinLibrary(h.Jf)
	if err != nil {
		return "", "", "", err
	}

	var tmdbKey, tvdbKey string
	if item.TmdbID != 0 {
		tmdbKey = fmt.Sprintf("%d", item.TmdbID)
	}
	if item.TvdbID != 0 {
		tvdbKey = fmt.Sprintf("%d", item.TvdbID)
	}
	if tmdbKey == "" && tvdbKey == "" {
		return "", "", "", nil
	}

	for _, jfItem := range items {
		matched := false
		if tmdbKey != "" && jfItem.ProviderIDs["Tmdb"] == tmdbKey {
			matched = true
		}
		if !matched && tvdbKey != "" && jfItem.ProviderIDs["Tvdb"] == tvdbKey {
			matched = true
		}
		if !matched {
			continue
		}

		artworkURL := ""
		if _, ok := jfItem.ImageTags["Primary"]; ok {
			artworkURL = fmt.Sprintf("/jellyfin/Items/%s/Images/Primary", jfItem.ID)
		}
		return jfItem.ID, artworkURL, jfItem.Overview, nil
	}
	return "", "", "", nil
}
