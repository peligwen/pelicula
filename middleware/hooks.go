package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

// handleImportHook receives *arr import webhooks, normalizes the payload,
// and forwards a job to Procula.
func handleImportHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	eventType, _ := raw["eventType"].(string)
	// Only process Download (import) events; silently accept test pings
	if strings.EqualFold(eventType, "test") {
		writeJSON(w, map[string]string{"status": "ok"})
		return
	}
	if !strings.EqualFold(eventType, "download") {
		log.Printf("[hooks] ignoring %s event", eventType)
		writeJSON(w, map[string]string{"status": "ignored"})
		return
	}

	source, err := normalizeHookPayload(raw)
	if err != nil {
		log.Printf("[hooks] failed to normalize webhook: %v", err)
		writeError(w, "invalid webhook payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("[hooks] import webhook: %s %q (%s) path=%s", source.ArrType, source.Title, source.Type, source.Path)

	// Forward to Procula
	proculaURL := proculaBaseURL() + "/api/procula/jobs"
	if err := forwardToProcula(proculaURL, source); err != nil {
		log.Printf("[hooks] failed to forward to Procula: %v", err)
		// Don't fail the webhook — *arr doesn't retry sensibly on 5xx
		writeJSON(w, map[string]string{"status": "queued", "warning": err.Error()})
		return
	}

	writeJSON(w, map[string]string{"status": "queued"})
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

	source.DownloadHash = downloadHash
	return source, nil
}

// ProculaJobSource mirrors procula's JobSource for the HTTP call.
type ProculaJobSource struct {
	Type                   string `json:"type"`
	Title                  string `json:"title"`
	Year                   int    `json:"year"`
	Path                   string `json:"path"`
	Size                   int64  `json:"size"`
	ArrID                  int    `json:"arr_id"`
	ArrType                string `json:"arr_type"`
	DownloadHash           string `json:"download_hash"`
	ExpectedRuntimeMinutes int    `json:"expected_runtime_minutes"`
}

func forwardToProcula(url string, source ProculaJobSource) error {
	data, err := json.Marshal(source)
	if err != nil {
		return err
	}
	resp, err := services.client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("reach procula: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("procula HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// handleProcessingProxy proxies Procula's status endpoint for the dashboard.
func handleProcessingProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	resp, err := services.client.Get(proculaBaseURL() + "/api/procula/status")
	if err != nil {
		writeError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, "failed to read procula response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func proculaBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("PROCULA_URL")); v != "" {
		return v
	}
	return "http://procula:8282"
}
