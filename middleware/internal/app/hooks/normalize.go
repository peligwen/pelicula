// normalize.go — *arr webhook payload normalization.
package hooks

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"pelicula-api/internal/app/catalog"
)

// NormalizeHookPayload converts a Radarr or Sonarr webhook body into a
// catalog.ProculaJobSource. It validates that the file path is under a known
// media directory to prevent path traversal.
func NormalizeHookPayload(raw map[string]any) (source catalog.ProculaJobSource, err error) {
	downloadHash, _ := raw["downloadId"].(string)

	// Detect *arr type by payload shape.
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
	if !IsAllowedWebhookPath(source.Path) {
		return source, fmt.Errorf("path not under an allowed media directory: %s", source.Path)
	}

	source.DownloadHash = downloadHash
	return source, nil
}

// IsAllowedWebhookPath checks that the path from a webhook payload is under a
// known media directory, preventing path traversal to arbitrary filesystem locations.
func IsAllowedWebhookPath(p string) bool {
	if isUnderPrefixes(p, []string{"/downloads", "/processing"}) {
		return true
	}
	clean := filepath.Clean(p)
	return clean == "/media" || strings.HasPrefix(clean, "/media/")
}

// isUnderPrefixes reports whether the cleaned path equals or is nested under
// one of the given prefixes.
func isUnderPrefixes(p string, prefixes []string) bool {
	clean := filepath.Clean(p)
	for _, prefix := range prefixes {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

func parseArrDate(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, _ = time.Parse("2006-01-02T15:04:05Z", s)
	}
	return t
}

// floatVal extracts a float64 from a map[string]any by key.
func floatVal(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

// strVal extracts a string from a map[string]any by key.
func strVal(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
