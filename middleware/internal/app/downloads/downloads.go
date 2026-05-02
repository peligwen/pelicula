// Package downloads manages qBittorrent torrent interactions and *arr queue operations.
package downloads

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"pelicula-api/httputil"
)

// QbtClient is the subset of ServiceClients that the downloads package needs.
type QbtClient interface {
	QbtGet(ctx context.Context, path string) ([]byte, error)
	QbtPost(ctx context.Context, path, form string) error
	Keys() (sonarr, radarr, prowlarr string)
	ArrGet(ctx context.Context, baseURL, apiKey, path string) ([]byte, error)
	ArrPost(ctx context.Context, baseURL, apiKey, path string, payload any) ([]byte, error)
	ArrPut(ctx context.Context, baseURL, apiKey, path string, payload any) ([]byte, error)
	ArrDelete(ctx context.Context, baseURL, apiKey, path string) ([]byte, error)
	ArrGetAllQueueRecords(ctx context.Context, baseURL, apiKey, apiVer, extraParams string) ([]map[string]any, error)
}

// Torrent represents a qBittorrent torrent.
type Torrent struct {
	Hash     string  `json:"hash"`
	Name     string  `json:"name"`
	Progress float64 `json:"progress"`
	DLSpeed  int64   `json:"dlspeed"`
	UPSpeed  int64   `json:"upspeed"`
	ETA      int64   `json:"eta"`
	State    string  `json:"state"`
	Size     int64   `json:"size"`
	Category string  `json:"category"`
}

// DownloadStats holds aggregate download statistics.
type DownloadStats struct {
	DLSpeed int64 `json:"dlspeed"`
	UPSpeed int64 `json:"upspeed"`
	Active  int   `json:"active"`
	Queued  int   `json:"queued"`
}

// Response is the full downloads API response.
type Response struct {
	Torrents []Torrent     `json:"torrents"`
	Stats    DownloadStats `json:"stats"`
}

// Handler holds injected dependencies for the downloads handlers.
type Handler struct {
	Svc       QbtClient
	SonarrURL string
	RadarrURL string
}

func (h *Handler) HandleDownloads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	data, err := h.Svc.QbtGet(r.Context(), "/api/v2/torrents/info")
	if err != nil {
		httputil.WriteError(w, "failed to reach qBittorrent: "+err.Error(), http.StatusBadGateway)
		return
	}

	var rawTorrents []map[string]any
	if err := json.Unmarshal(data, &rawTorrents); err != nil {
		httputil.WriteError(w, "failed to parse torrent data", http.StatusInternalServerError)
		return
	}

	torrents := make([]Torrent, 0, len(rawTorrents))
	active := 0
	queued := 0

	for _, rt := range rawTorrents {
		t := Torrent{
			Hash:     strVal(rt, "hash"),
			Name:     strVal(rt, "name"),
			Progress: floatVal(rt, "progress"),
			DLSpeed:  intVal(rt, "dlspeed"),
			UPSpeed:  intVal(rt, "upspeed"),
			ETA:      intVal(rt, "eta"),
			State:    strVal(rt, "state"),
			Size:     intVal(rt, "size"),
			Category: strVal(rt, "category"),
		}
		torrents = append(torrents, t)

		switch t.State {
		case "downloading", "uploading", "stalledDL", "stalledUP", "forcedDL", "forcedUP":
			active++
		case "queuedDL", "queuedUP":
			queued++
		}
	}

	statsData, err := h.Svc.QbtGet(r.Context(), "/api/v2/transfer/info")
	var stats DownloadStats
	stats.Active = active
	stats.Queued = queued

	if err == nil {
		var rawStats map[string]any
		if json.Unmarshal(statsData, &rawStats) == nil {
			stats.DLSpeed = intVal(rawStats, "dl_info_speed")
			stats.UPSpeed = intVal(rawStats, "up_info_speed")
		}
	}

	httputil.WriteJSON(w, Response{
		Torrents: torrents,
		Stats:    stats,
	})
}

func (h *Handler) HandleDownloadStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	statsData, err := h.Svc.QbtGet(r.Context(), "/api/v2/transfer/info")
	if err != nil {
		httputil.WriteError(w, "failed to reach qBittorrent", http.StatusBadGateway)
		return
	}

	var rawStats map[string]any
	if err := json.Unmarshal(statsData, &rawStats); err != nil {
		httputil.WriteError(w, "failed to parse stats", http.StatusInternalServerError)
		return
	}

	stats := DownloadStats{
		DLSpeed: intVal(rawStats, "dl_info_speed"),
		UPSpeed: intVal(rawStats, "up_info_speed"),
	}
	httputil.WriteJSON(w, stats)
}

func (h *Handler) HandleDownloadPause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Hash   string `json:"hash"`
		Paused bool   `json:"paused"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Hash == "" {
		httputil.WriteError(w, "invalid request", http.StatusBadRequest)
		return
	}

	// qBittorrent v5+ renamed pause/resume to stop/start
	var endpoint string
	if req.Paused {
		endpoint = "/api/v2/torrents/stop"
	} else {
		endpoint = "/api/v2/torrents/start"
	}

	if err := h.Svc.QbtPost(r.Context(), endpoint, "hashes="+url.QueryEscape(req.Hash)); err != nil {
		httputil.WriteError(w, "qBittorrent error: "+err.Error(), http.StatusBadGateway)
		return
	}

	action := "resumed"
	if req.Paused {
		action = "paused"
	}
	slog.Info("torrent state changed", "component", "downloads", "action", action, "hash", shortHash(req.Hash))
	httputil.WriteJSON(w, map[string]string{"status": action})
}

func (h *Handler) HandleDownloadCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Hash      string `json:"hash"`
		Category  string `json:"category"`
		Blocklist bool   `json:"blocklist"`
		Reason    string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Hash == "" {
		httputil.WriteError(w, "invalid request", http.StatusBadRequest)
		return
	}

	sonarrKey, radarrKey, _ := h.Svc.Keys()
	var baseURL, apiKey, apiVer string
	switch req.Category {
	case "radarr":
		baseURL, apiKey, apiVer = h.RadarrURL, radarrKey, "/api/v3"
	case "sonarr":
		baseURL, apiKey, apiVer = h.SonarrURL, sonarrKey, "/api/v3"
	default:
		httputil.WriteError(w, "unknown download category", http.StatusUnprocessableEntity)
		return
	}

	if baseURL != "" && apiKey != "" {
		if !req.Blocklist {
			h.unmonitorArrItem(r.Context(), baseURL, apiKey, apiVer, req.Category, req.Hash)
		}
		h.removeFromArrQueue(r.Context(), baseURL, apiKey, apiVer, req.Hash, req.Blocklist)
	}

	if err := h.Svc.QbtPost(r.Context(), "/api/v2/torrents/delete", "hashes="+url.QueryEscape(req.Hash)+"&deleteFiles=true"); err != nil {
		slog.Error("failed to delete torrent from qBittorrent", "component", "downloads", "error", err)
	}

	action := "cancelled"
	if req.Blocklist {
		action = "cancelled+blocklisted"
	}
	slog.Info("torrent cancelled", "component", "downloads",
		"action", action, "hash", shortHash(req.Hash),
		"category", req.Category, "blocklist", req.Blocklist, "reason", req.Reason)
	httputil.WriteJSON(w, map[string]string{"status": "removed"})
}

func (h *Handler) removeFromArrQueue(ctx context.Context, baseURL, apiKey, apiVer, hash string, blocklist bool) {
	records, err := h.Svc.ArrGetAllQueueRecords(ctx, baseURL, apiKey, apiVer, "&includeUnknownMovieItems=true&includeUnknownSeriesItems=true")
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
		_, err := h.Svc.ArrDelete(ctx, baseURL, apiKey, path)
		if err != nil {
			slog.Error("failed to remove arr queue item", "component", "downloads", "queue_id", queueID, "error", err)
		} else {
			slog.Info("removed arr queue item", "component", "downloads", "queue_id", queueID, "url", baseURL, "blocklist", blockParam)
		}
		return
	}
	slog.Warn("hash not found in arr queue", "component", "downloads", "hash", shortHash(hash), "url", baseURL)
}

func (h *Handler) unmonitorArrItem(ctx context.Context, baseURL, apiKey, apiVer, category, hash string) {
	records, err := h.Svc.ArrGetAllQueueRecords(ctx, baseURL, apiKey, apiVer, "")
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
				h.unmonitorMovie(ctx, baseURL, apiKey, apiVer, movieID)
			}
		case "sonarr":
			episodeID := int(floatVal(rec, "episodeId"))
			if episodeID > 0 {
				h.unmonitorEpisode(ctx, baseURL, apiKey, apiVer, episodeID)
			}
		}
		return
	}
}

func (h *Handler) unmonitorMovie(ctx context.Context, baseURL, apiKey, apiVer string, movieID int) {
	data, err := h.Svc.ArrGet(ctx, baseURL, apiKey, fmt.Sprintf("%s/movie/%d", apiVer, movieID))
	if err != nil {
		return
	}
	var movie map[string]any
	if json.Unmarshal(data, &movie) != nil {
		return
	}
	movie["monitored"] = false
	if _, err := h.Svc.ArrPut(ctx, baseURL, apiKey, fmt.Sprintf("%s/movie/%d", apiVer, movieID), movie); err != nil {
		slog.Error("failed to unmonitor movie", "component", "downloads", "movie_id", movieID, "error", err)
	} else {
		slog.Info("unmonitored movie", "component", "downloads", "movie_id", movieID)
	}
}

func (h *Handler) unmonitorEpisode(ctx context.Context, baseURL, apiKey, apiVer string, episodeID int) {
	data, err := h.Svc.ArrGet(ctx, baseURL, apiKey, fmt.Sprintf("%s/episode/%d", apiVer, episodeID))
	if err != nil {
		return
	}
	var episode map[string]any
	if json.Unmarshal(data, &episode) != nil {
		return
	}
	episode["monitored"] = false
	if _, err := h.Svc.ArrPut(ctx, baseURL, apiKey, fmt.Sprintf("%s/episode/%d", apiVer, episodeID), episode); err != nil {
		slog.Error("failed to unmonitor episode", "component", "downloads", "episode_id", episodeID, "error", err)
	} else {
		slog.Info("unmonitored episode", "component", "downloads", "episode_id", episodeID)
	}
}

// strVal extracts a string from map[string]any.
func strVal(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// floatVal extracts a float64 from map[string]any.
func floatVal(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

// intVal extracts an int64 from map[string]any (where JSON numbers are float64).
func intVal(m map[string]any, key string) int64 {
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	return 0
}

// shortHash returns the first 8 chars of a hash string, for logging.
func shortHash(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}
