package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// removeFromArrQueue finds the download in the *arr queue and removes it.
func removeFromArrQueue(baseURL, apiKey, apiVer, hash string, blocklist bool) {
	records, err := services.ArrGetAllQueueRecords(baseURL, apiKey, apiVer, "&includeUnknownMovieItems=true&includeUnknownSeriesItems=true")
	if err != nil {
		slog.Error("failed to fetch arr queue", "component", "downloads", "url", baseURL, "error", err)
		return
	}

	for _, rec := range records {
		dlHash := strVal(rec, "downloadId")
		if !strings.EqualFold(dlHash, hash) {
			continue
		}
		queueID := int(floatVal(rec, "id"))
		blockParam := "false"
		if blocklist {
			blockParam = "true"
		}
		path := fmt.Sprintf("%s/queue/%d?removeFromClient=true&blocklist=%s", apiVer, queueID, blockParam)
		_, err := services.ArrDelete(baseURL, apiKey, path)
		if err != nil {
			slog.Error("failed to remove arr queue item", "component", "downloads", "queue_id", queueID, "error", err)
		} else {
			slog.Info("removed arr queue item", "component", "downloads", "queue_id", queueID, "url", baseURL, "blocklist", blockParam)
		}
		return
	}
	slog.Warn("hash not found in arr queue", "component", "downloads", "hash", shortHash(hash), "url", baseURL)
}

// unmonitorArrItem finds the movie/series associated with a download hash and unmonitors it.
func unmonitorArrItem(baseURL, apiKey, apiVer, category, hash string) {
	records, err := services.ArrGetAllQueueRecords(baseURL, apiKey, apiVer, "")
	if err != nil {
		return
	}

	for _, rec := range records {
		if !strings.EqualFold(strVal(rec, "downloadId"), hash) {
			continue
		}

		switch category {
		case "radarr":
			movieID := int(floatVal(rec, "movieId"))
			if movieID > 0 {
				unmonitorMovie(baseURL, apiKey, apiVer, movieID)
			}
		case "sonarr":
			episodeID := int(floatVal(rec, "episodeId"))
			if episodeID > 0 {
				unmonitorEpisode(baseURL, apiKey, apiVer, episodeID)
			}
		}
		return
	}
}

func unmonitorMovie(baseURL, apiKey, apiVer string, movieID int) {
	data, err := services.ArrGet(baseURL, apiKey, fmt.Sprintf("%s/movie/%d", apiVer, movieID))
	if err != nil {
		return
	}
	var movie map[string]any
	if json.Unmarshal(data, &movie) != nil {
		return
	}
	movie["monitored"] = false
	_, err = services.ArrPut(baseURL, apiKey, fmt.Sprintf("%s/movie/%d", apiVer, movieID), movie)
	if err != nil {
		slog.Error("failed to unmonitor movie", "component", "downloads", "movie_id", movieID, "error", err)
	} else {
		slog.Info("unmonitored movie", "component", "downloads", "movie_id", movieID)
	}
}

func unmonitorEpisode(baseURL, apiKey, apiVer string, episodeID int) {
	data, err := services.ArrGet(baseURL, apiKey, fmt.Sprintf("%s/episode/%d", apiVer, episodeID))
	if err != nil {
		return
	}
	var episode map[string]any
	if json.Unmarshal(data, &episode) != nil {
		return
	}
	episode["monitored"] = false
	_, err = services.ArrPut(baseURL, apiKey, fmt.Sprintf("%s/episode/%d", apiVer, episodeID), episode)
	if err != nil {
		slog.Error("failed to unmonitor episode", "component", "downloads", "episode_id", episodeID, "error", err)
	} else {
		slog.Info("unmonitored episode", "component", "downloads", "episode_id", episodeID)
	}
}
