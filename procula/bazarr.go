package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var bazarrURL = env("BAZARR_URL", "http://bazarr:6767/bazarr")

// readBazarrAPIKey reads the Bazarr API key from config.yaml.
// Bazarr generates this key on first startup and stores it under auth.apikey.
// configDir is /config inside the container; the file is at /config/bazarr/config/config.yaml.
func readBazarrAPIKey(configDir string) (string, error) {
	path := configDir + "/bazarr/config/config.yaml"
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read bazarr config.yaml: %w", err)
	}
	inAuth := false
	for _, raw := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Top-level key (no leading whitespace) starts a new section.
		if raw[0] != ' ' && raw[0] != '\t' {
			inAuth = strings.HasPrefix(raw, "auth:")
			continue
		}
		if !inAuth {
			continue
		}
		if strings.HasPrefix(trimmed, "apikey:") {
			key := strings.TrimSpace(strings.TrimPrefix(trimmed, "apikey:"))
			key = strings.Trim(key, `"'`)
			if key == "" || key == "null" {
				return "", fmt.Errorf("auth.apikey empty in bazarr config.yaml")
			}
			return key, nil
		}
	}
	return "", fmt.Errorf("no auth.apikey found in bazarr config.yaml")
}

// bazarrSearchSubtitles asks Bazarr to search for missing subtitle languages
// for the given job, one PATCH per language. Fire-and-forget: errors are
// logged and do not block the pipeline.
//
// Bazarr's REST API is Flask-RESTx and reads request.form, so payloads must
// be form-encoded. PATCH /api/{movies,episodes}/subtitles is the per-item
// search trigger (POST on the same path is the subtitle-file upload
// endpoint). One call per language is required.
//
// If job.MissingSubs is empty (e.g. a synthetic job created by the library
// resub button), this falls back to every language in PELICULA_SUB_LANGS.
// Bazarr de-dupes any language that's already present on disk.
func bazarrSearchSubtitles(ctx context.Context, configDir string, job *Job) {
	apiKey, err := readBazarrAPIKey(configDir)
	if err != nil {
		slog.Warn("bazarr: cannot read API key, skipping subtitle search", "component", "bazarr", "error", err)
		return
	}

	langs := job.MissingSubs
	if len(langs) == 0 {
		for _, c := range strings.Split(os.Getenv("PELICULA_SUB_LANGS"), ",") {
			if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
				langs = append(langs, c)
			}
		}
	}
	if len(langs) == 0 {
		slog.Warn("bazarr: no languages to request, skipping", "component", "bazarr", "job_id", job.ID)
		return
	}

	var path string
	base := url.Values{}
	switch job.Source.ArrType {
	case "radarr":
		path = "/api/movies/subtitles"
		base.Set("radarrid", strconv.Itoa(job.Source.ArrID))
	case "sonarr":
		if job.Source.EpisodeID == 0 {
			slog.Warn("bazarr: episode ID not available, skipping subtitle search", "component", "bazarr", "job_id", job.ID)
			return
		}
		path = "/api/episodes/subtitles"
		base.Set("seriesid", strconv.Itoa(job.Source.ArrID))
		base.Set("episodeid", strconv.Itoa(job.Source.EpisodeID))
	default:
		slog.Warn("bazarr: unknown arr_type, skipping subtitle search", "component", "bazarr", "arr_type", job.Source.ArrType)
		return
	}

	for _, code := range langs {
		form := url.Values{}
		for k, v := range base {
			form[k] = v
		}
		form.Set("language", code)
		form.Set("hi", "False")
		form.Set("forced", "False")

		reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPatch, bazarrURL+path, strings.NewReader(form.Encode()))
		if err != nil {
			cancel()
			slog.Warn("bazarr: build request failed", "component", "bazarr", "lang", code, "error", err)
			continue
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-API-KEY", apiKey)

		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err != nil {
			slog.Warn("bazarr: request failed", "component", "bazarr", "lang", code, "error", err)
			continue
		}
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			slog.Warn("bazarr: search returned error", "component", "bazarr", "lang", code, "status", resp.StatusCode, "body", string(body))
			continue
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		slog.Info("bazarr: subtitle search triggered", "component", "bazarr", "arr_type", job.Source.ArrType, "job_id", job.ID, "lang", code)
	}
}
