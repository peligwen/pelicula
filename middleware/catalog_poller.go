package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// RunQueuePoller polls Radarr and Sonarr's download queues every 60 seconds
// and upserts queue-tier catalog records for items actively downloading.
// Items already at pipeline or library tier are not downgraded (enforced by UpsertCatalogItem).
func RunQueuePoller(ctx context.Context, db *sql.DB, svc *ServiceClients) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	// Run immediately on startup so the catalog is populated without waiting 60s
	pollDownloadQueue(db, svc)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollDownloadQueue(db, svc)
		}
	}
}

func pollDownloadQueue(db *sql.DB, svc *ServiceClients) {
	sonarrKey, radarrKey, _ := svc.Keys()

	if radarrKey != "" {
		records, err := svc.ArrGetAllQueueRecords(radarrURL, radarrKey, "/api/v3", "&includeUnknownMovieItems=false")
		if err != nil {
			slog.Error("catalog poller: radarr queue fetch", "component", "catalog_poller", "error", err)
		} else {
			for _, rec := range records {
				upsertQueueMovie(db, rec)
			}
		}
	}

	if sonarrKey != "" {
		records, err := svc.ArrGetAllQueueRecords(sonarrURL, sonarrKey, "/api/v3", "&includeUnknownSeriesItems=false")
		if err != nil {
			slog.Error("catalog poller: sonarr queue fetch", "component", "catalog_poller", "error", err)
		} else {
			for _, rec := range records {
				upsertQueueEpisode(db, rec)
			}
		}
	}
}

func upsertQueueMovie(db *sql.DB, rec map[string]any) {
	movie, ok := rec["movie"].(map[string]any)
	if !ok {
		return
	}
	title, _ := movie["title"].(string)
	if title == "" {
		return
	}
	if _, err := UpsertCatalogItem(db, CatalogItem{
		Type:    "movie",
		TmdbID:  int(floatVal(movie, "tmdbId")),
		ArrID:   int(floatVal(movie, "id")),
		ArrType: "radarr",
		Title:   title,
		Year:    int(floatVal(movie, "year")),
		Tier:    "queue",
	}); err != nil {
		slog.Error("catalog poller: upsert queue movie", "component", "catalog_poller", "title", title, "error", err)
	}
}

func upsertQueueEpisode(db *sql.DB, rec map[string]any) {
	series, ok := rec["series"].(map[string]any)
	if !ok {
		return
	}
	title, _ := series["title"].(string)
	if title == "" {
		return
	}
	tvdbID := int(floatVal(series, "tvdbId"))
	arrID := int(floatVal(series, "id"))
	year := int(floatVal(series, "year"))
	episodeID := int(floatVal(rec, "episodeId"))
	seasonNum := 0
	epNum := 0
	if episode, ok := rec["episode"].(map[string]any); ok {
		seasonNum = int(floatVal(episode, "seasonNumber"))
		epNum = int(floatVal(episode, "episodeNumber"))
	}

	// Upsert series
	seriesID, err := UpsertCatalogItem(db, CatalogItem{
		Type:    "series",
		TvdbID:  tvdbID,
		ArrID:   arrID,
		ArrType: "sonarr",
		Title:   title,
		Year:    year,
		Tier:    "queue",
	})
	if err != nil {
		slog.Error("catalog poller: upsert queue series", "component", "catalog_poller", "title", title, "error", err)
		return
	}

	// Upsert season
	seasonID, err := UpsertCatalogItem(db, CatalogItem{
		Type:         "season",
		ParentID:     seriesID,
		SeasonNumber: seasonNum,
		Title:        fmt.Sprintf("%s Season %d", title, seasonNum),
		Year:         year,
		Tier:         "queue",
	})
	if err != nil {
		slog.Error("catalog poller: upsert queue season", "component", "catalog_poller", "title", title, "error", err)
		return
	}

	// Upsert episode (no file path yet — still downloading)
	if _, err := UpsertCatalogItem(db, CatalogItem{
		Type:          "episode",
		ParentID:      seasonID,
		EpisodeID:     episodeID,
		SeasonNumber:  seasonNum,
		EpisodeNumber: epNum,
		ArrType:       "sonarr",
		Title:         title,
		Year:          year,
		Tier:          "queue",
	}); err != nil {
		slog.Error("catalog poller: upsert queue episode", "component", "catalog_poller", "title", title, "error", err)
	}
}
