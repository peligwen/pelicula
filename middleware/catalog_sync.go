package main

import (
	"database/sql"
	"fmt"
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
