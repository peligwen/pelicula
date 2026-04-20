package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"pelicula-api/internal/app/util"
)

// QueueArrClient is the subset of ArrClient needed for queue polling.
type QueueArrClient interface {
	Keys() (sonarr, radarr, prowlarr string)
	ArrGetAllQueueRecords(baseURL, apiKey, apiVer, extraParams string) ([]map[string]any, error)
}

// RunQueuePoller polls Radarr and Sonarr's download queues every 60 seconds
// (±10% jitter) and upserts queue-tier catalog records for items actively
// downloading.
func RunQueuePoller(ctx context.Context, db *sql.DB, svc QueueArrClient, radarrURL, sonarrURL string) {
	tick := util.JitteredTicker(ctx, 60*time.Second, 0.1)

	pollDownloadQueue(ctx, db, svc, radarrURL, sonarrURL)

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			pollDownloadQueue(ctx, db, svc, radarrURL, sonarrURL)
		}
	}
}

func pollDownloadQueue(ctx context.Context, db *sql.DB, svc QueueArrClient, radarrURL, sonarrURL string) {
	sonarrKey, radarrKey, _ := svc.Keys()

	if radarrKey != "" {
		records, err := svc.ArrGetAllQueueRecords(radarrURL, radarrKey, "/api/v3", "&includeUnknownMovieItems=false")
		if err != nil {
			slog.Error("catalog poller: radarr queue fetch", "component", "catalog_poller", "error", err)
		} else {
			for _, rec := range records {
				upsertQueueMovie(ctx, db, rec)
			}
		}
	}

	if sonarrKey != "" {
		records, err := svc.ArrGetAllQueueRecords(sonarrURL, sonarrKey, "/api/v3", "&includeUnknownSeriesItems=false")
		if err != nil {
			slog.Error("catalog poller: sonarr queue fetch", "component", "catalog_poller", "error", err)
		} else {
			for _, rec := range records {
				upsertQueueEpisode(ctx, db, rec)
			}
		}
	}
}

func upsertQueueMovie(ctx context.Context, db *sql.DB, rec map[string]any) {
	movie, ok := rec["movie"].(map[string]any)
	if !ok {
		return
	}
	title, _ := movie["title"].(string)
	if title == "" {
		return
	}
	if _, err := UpsertCatalogItem(ctx, db, CatalogItem{
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

func upsertQueueEpisode(ctx context.Context, db *sql.DB, rec map[string]any) {
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
	epTitle := title
	if episode, ok := rec["episode"].(map[string]any); ok {
		seasonNum = int(floatVal(episode, "seasonNumber"))
		epNum = int(floatVal(episode, "episodeNumber"))
		if t, ok := episode["title"].(string); ok && t != "" {
			epTitle = t
		}
	}

	seriesID, err := UpsertCatalogItem(ctx, db, CatalogItem{
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

	seasonID, err := UpsertCatalogItem(ctx, db, CatalogItem{
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

	if _, err := UpsertCatalogItem(ctx, db, CatalogItem{
		Type:          "episode",
		ParentID:      seasonID,
		EpisodeID:     episodeID,
		SeasonNumber:  seasonNum,
		EpisodeNumber: epNum,
		ArrType:       "sonarr",
		Title:         epTitle,
		Year:          year,
		Tier:          "queue",
	}); err != nil {
		slog.Error("catalog poller: upsert queue episode", "component", "catalog_poller", "title", title, "error", err)
	}
}
