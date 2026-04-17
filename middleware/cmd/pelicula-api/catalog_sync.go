package main

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

// jellyfinItemCache is a short-lived in-process cache of Jellyfin library items
// keyed by provider IDs. Jellyfin 10.11+ ignores AnyProviderIdEquals on
// user-scoped queries, so we fetch the full library once and match client-side.
var jellyfinCache struct {
	mu        sync.Mutex
	items     []jellyfinItem
	fetchedAt time.Time
}

type jellyfinItem struct {
	ID          string            `json:"Id"`
	Overview    string            `json:"Overview"`
	ImageTags   map[string]string `json:"ImageTags"`
	ProviderIDs map[string]string `json:"ProviderIds"`
}

const jellyfinCacheTTL = 5 * time.Minute

// fetchJellyfinLibrary fetches all Movie/Series items from Jellyfin and caches them.
// The mutex is NOT held across the outbound HTTP request — only around cache reads
// and writes — to avoid serializing all callers on network latency.
func fetchJellyfinLibrary(svc *ServiceClients) ([]jellyfinItem, error) {
	// Fast path: return cached value while still fresh.
	jellyfinCache.mu.Lock()
	if time.Since(jellyfinCache.fetchedAt) < jellyfinCacheTTL {
		items := jellyfinCache.items
		jellyfinCache.mu.Unlock()
		return items, nil
	}
	jellyfinCache.mu.Unlock()

	// Cache is stale — perform the HTTP fetch without holding the lock.
	userID, err := resolveJellyfinUserID(svc)
	if err != nil {
		return nil, err
	}

	jellyfinLibraryCap := 5000
	if v := os.Getenv("JELLYFIN_LIBRARY_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			jellyfinLibraryCap = n
		}
	}

	path := fmt.Sprintf("/Users/%s/Items?IncludeItemTypes=Movie,Series&Fields=Overview,ImageTags,ProviderIds&Recursive=true&Limit=%d", userID, jellyfinLibraryCap)
	body, err := jellyfinDo(svc, "GET", path, svc.JellyfinAPIKey, nil)
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

	// Re-acquire lock to store result. Double-check: if another goroutine beat
	// us and wrote a fresh value, keep theirs (it's equally good and avoids
	// a spurious cache reset).
	jellyfinCache.mu.Lock()
	if time.Since(jellyfinCache.fetchedAt) >= jellyfinCacheTTL {
		jellyfinCache.items = resp.Items
		jellyfinCache.fetchedAt = time.Now()
		slog.Info("jellyfin library cached", "component", "catalog_sync", "count", count)
	}
	items := jellyfinCache.items
	jellyfinCache.mu.Unlock()
	return items, nil
}

// UpsertFromHook creates or updates catalog records when a download completes.
// For episodes, it upserts the parent series and season records first,
// then the episode — linking it into the hierarchy.
// All items are set to tier "pipeline": they are on the filesystem but not yet
// confirmed in Jellyfin.
func UpsertFromHook(db *sql.DB, source ProculaJobSource) error {
	switch source.Type {
	case "movie":
		_, err := UpsertCatalogItem(db, CatalogItem{
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
		// 1. Upsert the parent series
		seriesID, err := UpsertCatalogItem(db, CatalogItem{
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

		// 2. Upsert the season
		seasonTitle := fmt.Sprintf("%s Season %d", source.Title, source.SeasonNumber)
		seasonID, err := UpsertCatalogItem(db, CatalogItem{
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

		// 3. Upsert the episode
		_, err = UpsertCatalogItem(db, CatalogItem{
			Type:          "episode",
			ParentID:      seasonID,
			EpisodeID:     source.EpisodeID,
			SeasonNumber:  source.SeasonNumber,
			EpisodeNumber: source.EpisodeNumber,
			ArrType:       source.ArrType,
			// Title is the series title — episode-level titles are not in ProculaJobSource
			Title:    source.Title,
			Year:     source.Year,
			Tier:     "pipeline",
			FilePath: source.Path,
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
// Movies with a file get tier "library"; without get tier "queue".
// Series tier is derived from episodeFileCount statistics.
// Items already in the catalog are updated; tier is never downgraded.
func BackfillFromArr(db *sql.DB, svc *ServiceClients) error {
	sonarrKey, radarrKey, _ := svc.Keys()

	if radarrKey != "" {
		if err := backfillRadarr(db, svc, radarrKey); err != nil {
			slog.Error("backfill radarr failed", "component", "catalog_sync", "error", err)
			// Continue to Sonarr even if Radarr fails
		}
	}
	if sonarrKey != "" {
		if err := backfillSonarr(db, svc, sonarrKey); err != nil {
			slog.Error("backfill sonarr failed", "component", "catalog_sync", "error", err)
		}
	}
	slog.Info("catalog backfill complete", "component", "catalog_sync")
	return nil
}

func backfillRadarr(db *sql.DB, svc *ServiceClients, _ string) error {
	ctx := context.Background()
	data, err := svc.Radarr.Get(ctx, "/api/v3/movie")
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
		if _, err := UpsertCatalogItem(db, CatalogItem{
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

// episodeConcurrency is the maximum number of concurrent Sonarr episode fetches
// during catalog backfill.
const episodeConcurrency = 10

func backfillSonarr(db *sql.DB, svc *ServiceClients, _ string) error {
	ctx := context.Background()
	data, err := svc.Sonarr.Get(ctx, "/api/v3/series")
	if err != nil {
		return fmt.Errorf("sonarr list: %w", err)
	}
	var seriesList []map[string]any
	if err := json.Unmarshal(data, &seriesList); err != nil {
		return fmt.Errorf("sonarr parse: %w", err)
	}

	// seriesByArrID maps Sonarr series ID → parsed series fields needed for
	// season upserts. Built in the first pass so the second pass (episode fetch)
	// can look up series context without re-iterating seriesList.
	type seriesMeta struct {
		catalogID string
		title     string
		year      int
		tier      string
	}
	seriesByArrID := make(map[int]seriesMeta, len(seriesList))

	// First pass: upsert all series records and collect their arr IDs.
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

		catalogID, err := UpsertCatalogItem(db, CatalogItem{
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

	// Collect the arr IDs we successfully upserted.
	arrIDs := make([]int, 0, len(seriesByArrID))
	for id := range seriesByArrID {
		arrIDs = append(arrIDs, id)
	}

	// Second pass: fetch episodes per series with up to episodeConcurrency
	// concurrent requests. Individual fetches are used because Sonarr v3's
	// seriesId batch parameter is not reliably supported — when repeated query
	// params are given (e.g. ?seriesId=1&seriesId=2), ASP.NET may take only
	// the last value, silently returning episodes for a single series.
	var (
		allEpisodesMu sync.Mutex
		allEpisodes   []map[string]any
		fetchErr      error
		fetchErrMu    sync.Mutex
		wg            sync.WaitGroup
		sem           = make(chan struct{}, episodeConcurrency)
	)

	for _, id := range arrIDs {
		// Stop launching new goroutines if a fetch error was recorded.
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
			epData, err := svc.Sonarr.Get(ctx, path)
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

	// Third pass: upsert unique seasons from the episode list.
	// seasonsSeen key: (arrID, seasonNumber)
	type seasonKey struct {
		arrID     int
		seasonNum int
	}
	seasonsSeen := map[seasonKey]bool{}
	for _, ep := range allEpisodes {
		arrID := int(floatVal(ep, "seriesId"))
		seasonNum := int(floatVal(ep, "seasonNumber"))
		if seasonNum == 0 {
			continue // skip specials
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
		if _, err := UpsertCatalogItem(db, CatalogItem{
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

// maybeSyncJellyfinMetadata syncs Jellyfin metadata for an item if stale (>24h) or never synced.
// Only syncs movies and series (they carry the artwork/synopsis for their subtree).
// Safe to call in a goroutine — logs errors, never panics.
func maybeSyncJellyfinMetadata(item *CatalogItem) {
	if item == nil {
		return
	}
	// Only root-level items carry metadata
	if item.Type != "movie" && item.Type != "series" {
		return
	}
	if item.MetadataSyncedAt != "" {
		t, err := time.Parse(time.RFC3339, item.MetadataSyncedAt)
		if err == nil && time.Since(t) < 24*time.Hour {
			return // still fresh
		}
	}
	if err := SyncJellyfinMetadata(catalogDB, services, item); err != nil {
		slog.Error("jellyfin metadata sync", "component", "catalog_sync", "id", item.ID, "error", err)
	}
}

// SyncJellyfinMetadata fetches artwork and synopsis from Jellyfin and persists them.
// If the item is not yet in Jellyfin, it records the attempt (MetadataSyncedAt is set)
// so we don't hammer Jellyfin on every request.
func SyncJellyfinMetadata(db *sql.DB, svc *ServiceClients, item *CatalogItem) error {
	jellyfinID, artworkURL, synopsis, err := fetchJellyfinItemMeta(svc, item)
	if err != nil {
		return err
	}
	syncedAt := time.Now().UTC().Format(time.RFC3339)
	if err := UpdateCatalogMetadata(db, item.ID, jellyfinID, artworkURL, synopsis, syncedAt); err != nil {
		return fmt.Errorf("persist metadata: %w", err)
	}
	if jellyfinID != "" {
		slog.Info("jellyfin metadata synced", "component", "catalog_sync", "id", item.ID, "jellyfin_id", jellyfinID)
	}
	return nil
}

// resolveJellyfinUserID fetches and caches the pelicula-internal user ID.
// Jellyfin's /Items endpoint only returns folder items without a user context;
// /Users/{id}/Items?Recursive=true is required to query the actual library.
func resolveJellyfinUserID(svc *ServiceClients) (string, error) {
	svc.mu.RLock()
	uid := svc.JellyfinUserID
	svc.mu.RUnlock()
	if uid != "" {
		return uid, nil
	}

	body, err := jellyfinDo(svc, "GET", "/Users", svc.JellyfinAPIKey, nil)
	if err != nil {
		return "", fmt.Errorf("jellyfin users list: %w", err)
	}
	var users []struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	}
	if err := json.Unmarshal(body, &users); err != nil {
		return "", fmt.Errorf("jellyfin users parse: %w", err)
	}
	// Prefer pelicula-internal; fall back to first user found.
	for _, u := range users {
		if u.Name == jellyfinServiceUser {
			uid = u.ID
			break
		}
	}
	if uid == "" && len(users) > 0 {
		uid = users[0].ID
	}
	if uid == "" {
		return "", fmt.Errorf("no Jellyfin users found")
	}

	svc.mu.Lock()
	svc.JellyfinUserID = uid
	svc.mu.Unlock()
	return uid, nil
}

// fetchJellyfinItemMeta looks up a catalog item in the cached Jellyfin library
// by TMDB or TVDB provider ID. Returns (jellyfinID, artworkURL, synopsis, error).
// Returns empty strings without error if the item is not yet in Jellyfin.
//
// Note: Jellyfin 10.11+ ignores AnyProviderIdEquals on user-scoped queries,
// so we fetch the full library once (cached 5 min) and match client-side.
func fetchJellyfinItemMeta(svc *ServiceClients, item *CatalogItem) (string, string, string, error) {
	items, err := fetchJellyfinLibrary(svc)
	if err != nil {
		return "", "", "", err
	}

	// Build target provider ID strings to match against.
	var tmdbKey, tvdbKey string
	if item.TmdbID != 0 {
		tmdbKey = fmt.Sprintf("%d", item.TmdbID)
	}
	if item.TvdbID != 0 {
		tvdbKey = fmt.Sprintf("%d", item.TvdbID)
	}
	if tmdbKey == "" && tvdbKey == "" {
		return "", "", "", nil // nothing to match on
	}

	for _, jf := range items {
		matched := false
		if tmdbKey != "" && jf.ProviderIDs["Tmdb"] == tmdbKey {
			matched = true
		}
		if !matched && tvdbKey != "" && jf.ProviderIDs["Tvdb"] == tvdbKey {
			matched = true
		}
		if !matched {
			continue
		}

		artworkURL := ""
		if _, ok := jf.ImageTags["Primary"]; ok {
			// Root-relative so the browser loads it via nginx (/jellyfin → jellyfin:8096).
			artworkURL = fmt.Sprintf("/jellyfin/Items/%s/Images/Primary", jf.ID)
		}
		return jf.ID, artworkURL, jf.Overview, nil
	}
	return "", "", "", nil // not yet in Jellyfin
}
