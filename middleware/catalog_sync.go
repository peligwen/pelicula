package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

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

func backfillRadarr(db *sql.DB, svc *ServiceClients, apiKey string) error {
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

func backfillSonarr(db *sql.DB, svc *ServiceClients, apiKey string) error {
	data, err := svc.ArrGet(sonarrURL, apiKey, "/api/v3/series")
	if err != nil {
		return fmt.Errorf("sonarr list: %w", err)
	}
	var seriesList []map[string]any
	if err := json.Unmarshal(data, &seriesList); err != nil {
		return fmt.Errorf("sonarr parse: %w", err)
	}

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

		seriesID, err := UpsertCatalogItem(db, CatalogItem{
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

		// Fetch episodes to upsert seasons
		epData, err := svc.ArrGet(sonarrURL, apiKey, fmt.Sprintf("/api/v3/episode?seriesId=%d", arrID))
		if err != nil {
			slog.Error("backfill: fetch episodes", "component", "catalog_sync", "series", title, "error", err)
			continue
		}
		var episodes []map[string]any
		if err := json.Unmarshal(epData, &episodes); err != nil {
			continue
		}

		// Upsert unique seasons seen in episode list
		seasonsSeen := map[int]bool{}
		for _, ep := range episodes {
			seasonNum := int(floatVal(ep, "seasonNumber"))
			if seasonNum == 0 {
				continue // skip specials
			}
			if seasonsSeen[seasonNum] {
				continue
			}
			seasonsSeen[seasonNum] = true
			if _, err := UpsertCatalogItem(db, CatalogItem{
				Type:         "season",
				ParentID:     seriesID,
				SeasonNumber: seasonNum,
				Title:        fmt.Sprintf("%s Season %d", title, seasonNum),
				Year:         year,
				Tier:         tier,
			}); err != nil {
				slog.Error("backfill: upsert season", "component", "catalog_sync",
					"title", fmt.Sprintf("%s Season %d", title, seasonNum), "error", err)
			}
		}
	}
	slog.Info("backfill sonarr complete", "component", "catalog_sync", "count", len(seriesList))
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

// fetchJellyfinItemMeta queries Jellyfin for an item by TMDB/TVDB provider ID.
// Returns (jellyfinID, artworkURL, synopsis, error). Returns empty strings if not found.
func fetchJellyfinItemMeta(svc *ServiceClients, item *CatalogItem) (string, string, string, error) {
	var providerParam string
	switch item.Type {
	case "movie":
		if item.TmdbID != 0 {
			providerParam = fmt.Sprintf("Tmdb.%d", item.TmdbID)
		}
	case "series":
		if item.TvdbID != 0 {
			providerParam = fmt.Sprintf("Tvdb.%d", item.TvdbID)
		} else if item.TmdbID != 0 {
			providerParam = fmt.Sprintf("Tmdb.%d", item.TmdbID)
		}
	}
	if providerParam == "" {
		return "", "", "", nil // no provider ID to search by
	}

	path := fmt.Sprintf("/Items?AnyProviderIdEquals=%s&Fields=Overview,ImageTags&Limit=1", providerParam)
	body, err := jellyfinDo(svc, "GET", path, svc.JellyfinAPIKey, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("jellyfin items query: %w", err)
	}

	var resp struct {
		Items []struct {
			ID        string            `json:"Id"`
			Overview  string            `json:"Overview"`
			ImageTags map[string]string `json:"ImageTags"`
		} `json:"Items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", "", fmt.Errorf("jellyfin items parse: %w", err)
	}
	if len(resp.Items) == 0 {
		return "", "", "", nil // not yet in Jellyfin
	}

	jf := resp.Items[0]
	artworkURL := ""
	if _, ok := jf.ImageTags["Primary"]; ok {
		artworkURL = fmt.Sprintf("%s/Items/%s/Images/Primary", jellyfinURL, jf.ID)
	}
	return jf.ID, artworkURL, jf.Overview, nil
}
