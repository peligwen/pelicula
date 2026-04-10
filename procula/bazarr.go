package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

var bazarrURL = env("BAZARR_URL", "http://bazarr:6767/bazarr")

// readBazarrAPIKey reads the Bazarr API key from config.ini.
// Bazarr generates this key on first startup.
// configDir is /config inside the container; the file is at /config/bazarr/config/config.ini.
func readBazarrAPIKey(configDir string) (string, error) {
	path := configDir + "/bazarr/config/config.ini"
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read bazarr config.ini: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "apikey") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				if key := strings.TrimSpace(parts[1]); key != "" {
					return key, nil
				}
			}
		}
	}
	return "", fmt.Errorf("no apikey found in bazarr config.ini")
}

// bazarrSearchSubtitles asks Bazarr to search for subtitles for the given job immediately.
// This is fire-and-forget: errors are logged but do not block the pipeline.
// - Movies: POST /bazarr/api/movies/subtitles with {"radarrId": job.Source.ArrID}
// - Episodes: POST /bazarr/api/episodes/subtitles with {"sonarrEpisodeId": job.Source.EpisodeID}
//
// If the episode ID is missing (0) for a Sonarr job, falls back to logging a warning and returning.
func bazarrSearchSubtitles(ctx context.Context, configDir string, job *Job) {
	apiKey, err := readBazarrAPIKey(configDir)
	if err != nil {
		slog.Warn("bazarr: cannot read API key, skipping subtitle search", "component", "bazarr", "error", err)
		return
	}

	var path string
	var payload map[string]any

	switch job.Source.ArrType {
	case "radarr":
		path = "/api/movies/subtitles"
		payload = map[string]any{"radarrId": job.Source.ArrID}
	case "sonarr":
		if job.Source.EpisodeID == 0 {
			slog.Warn("bazarr: episode ID not available, skipping subtitle search", "component", "bazarr", "job_id", job.ID)
			return
		}
		path = "/api/episodes/subtitles"
		payload = map[string]any{"sonarrEpisodeId": job.Source.EpisodeID}
	default:
		slog.Warn("bazarr: unknown arr_type, skipping subtitle search", "component", "bazarr", "arr_type", job.Source.ArrType)
		return
	}

	body, _ := json.Marshal(payload)
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, bazarrURL+path, bytes.NewReader(body))
	if err != nil {
		slog.Warn("bazarr: failed to build request", "component", "bazarr", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("bazarr: subtitle search request failed", "component", "bazarr", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		slog.Warn("bazarr: subtitle search returned error status", "component", "bazarr", "status", resp.StatusCode)
		return
	}

	slog.Info("bazarr: subtitle search triggered", "component", "bazarr", "arr_type", job.Source.ArrType, "job_id", job.ID)
}
