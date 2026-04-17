// hooks_normalize.go — *arr webhook payload normalization.
package main

import (
	"fmt"
	"time"
)

// ProculaJobSource mirrors procula's JobSource for the HTTP call.
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

// normalizeHookPayload converts a Radarr or Sonarr webhook body into a JobSource.
func normalizeHookPayload(raw map[string]any) (source ProculaJobSource, err error) {
	downloadHash, _ := raw["downloadId"].(string)

	// Detect *arr type by payload shape
	if movie, ok := raw["movie"].(map[string]any); ok {
		// Radarr
		source.ArrType = "radarr"
		source.Type = "movie"
		source.Title, _ = movie["title"].(string)
		source.Year = int(floatVal(movie, "year"))
		source.ArrID = int(floatVal(movie, "id"))
		source.TmdbID = int(floatVal(movie, "tmdbId"))

		if mf, ok := raw["movieFile"].(map[string]any); ok {
			source.Path, _ = mf["path"].(string)
			source.Size = int64(floatVal(mf, "size"))
			if mi, ok := mf["mediaInfo"].(map[string]any); ok {
				secs := floatVal(mi, "runTimeSeconds")
				source.ExpectedRuntimeMinutes = int(secs / 60)
			}
		}
	} else if series, ok := raw["series"].(map[string]any); ok {
		// Sonarr
		source.ArrType = "sonarr"
		source.Type = "episode"
		source.Title, _ = series["title"].(string)
		source.Year = int(floatVal(series, "year"))
		source.ArrID = int(floatVal(series, "id"))
		source.TvdbID = int(floatVal(series, "tvdbId"))
		source.TmdbID = int(floatVal(series, "tmdbId"))

		if eps, ok := raw["episodes"].([]any); ok && len(eps) > 0 {
			if ep, ok := eps[0].(map[string]any); ok {
				source.EpisodeID = int(floatVal(ep, "id"))
				source.SeasonNumber = int(floatVal(ep, "seasonNumber"))
				source.EpisodeNumber = int(floatVal(ep, "episodeNumber"))
			}
		}

		if ef, ok := raw["episodeFile"].(map[string]any); ok {
			source.Path, _ = ef["path"].(string)
			source.Size = int64(floatVal(ef, "size"))
			if mi, ok := ef["mediaInfo"].(map[string]any); ok {
				secs := floatVal(mi, "runTimeSeconds")
				source.ExpectedRuntimeMinutes = int(secs / 60)
			}
		}
	} else {
		return source, fmt.Errorf("unrecognized payload: no 'movie' or 'series' key")
	}

	if source.Path == "" {
		return source, fmt.Errorf("no file path in webhook payload")
	}
	if !isAllowedWebhookPath(source.Path) {
		return source, fmt.Errorf("path not under an allowed media directory: %s", source.Path)
	}

	source.DownloadHash = downloadHash
	return source, nil
}

func parseArrDate(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, _ = time.Parse("2006-01-02T15:04:05Z", s)
	}
	return t
}
