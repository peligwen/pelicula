// Package downloads manages qBittorrent torrent interactions and *arr queue operations.
package downloads

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"pelicula-api/httputil"
	arr "pelicula-api/internal/clients/arr"
	qbt "pelicula-api/internal/clients/qbt"
)

// Svc is the subset of ServiceClients that the downloads package needs.
type Svc interface {
	Keys() (sonarr, radarr, prowlarr string)
	SonarrClient() *arr.Client
	RadarrClient() *arr.Client
	QbtClient() *qbt.Client
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
	Svc       Svc
	SonarrURL string
	RadarrURL string
}

func (h *Handler) HandleDownloads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	torrents, err := h.Svc.QbtClient().ListTorrents(r.Context())
	if err != nil {
		httputil.WriteError(w, "failed to reach qBittorrent: "+err.Error(), http.StatusBadGateway)
		return
	}

	out := make([]Torrent, 0, len(torrents))
	active := 0
	queued := 0

	for _, t := range torrents {
		out = append(out, Torrent{
			Hash:     t.Hash,
			Name:     t.Name,
			Progress: t.Progress,
			DLSpeed:  t.DLSpeed,
			UPSpeed:  t.UPSpeed,
			ETA:      t.ETA,
			State:    t.State,
			Size:     t.Size,
			Category: t.Category,
		})

		switch t.State {
		case "downloading", "uploading", "stalledDL", "stalledUP", "forcedDL", "forcedUP":
			active++
		case "queuedDL", "queuedUP":
			queued++
		}
	}

	stats := DownloadStats{Active: active, Queued: queued}
	if info, err := h.Svc.QbtClient().GetTransferInfo(r.Context()); err == nil {
		stats.DLSpeed = info.DLSpeed
		stats.UPSpeed = info.UPSpeed
	}

	httputil.WriteJSON(w, Response{
		Torrents: out,
		Stats:    stats,
	})
}

func (h *Handler) HandleDownloadStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	info, err := h.Svc.QbtClient().GetTransferInfo(r.Context())
	if err != nil {
		httputil.WriteError(w, "failed to reach qBittorrent", http.StatusBadGateway)
		return
	}

	httputil.WriteJSON(w, DownloadStats{
		DLSpeed: info.DLSpeed,
		UPSpeed: info.UPSpeed,
	})
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

	var err error
	if req.Paused {
		err = h.Svc.QbtClient().StopTorrent(r.Context(), req.Hash)
	} else {
		err = h.Svc.QbtClient().StartTorrent(r.Context(), req.Hash)
	}
	if err != nil {
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
	var arrClient *arr.Client
	var apiKey, apiVer string
	switch req.Category {
	case "radarr":
		arrClient, apiKey, apiVer = h.Svc.RadarrClient(), radarrKey, "/api/v3"
	case "sonarr":
		arrClient, apiKey, apiVer = h.Svc.SonarrClient(), sonarrKey, "/api/v3"
	default:
		httputil.WriteError(w, "unknown download category", http.StatusUnprocessableEntity)
		return
	}

	if apiKey != "" {
		if !req.Blocklist {
			h.unmonitorArrItem(r.Context(), arrClient, apiVer, req.Category, req.Hash)
		}
		h.removeFromArrQueue(r.Context(), arrClient, apiVer, req.Category, req.Hash, req.Blocklist)
	}

	if err := h.Svc.QbtClient().DeleteTorrent(r.Context(), req.Hash); err != nil {
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

func (h *Handler) removeFromArrQueue(ctx context.Context, arrClient *arr.Client, apiVer, category, hash string, blocklist bool) {
	records, err := arrClient.GetAllQueueRecords(ctx, apiVer, "&includeUnknownMovieItems=true&includeUnknownSeriesItems=true")
	if err != nil {
		slog.Error("failed to fetch arr queue", "component", "downloads", "service", category, "error", err)
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
		if err := arrClient.Delete(ctx, path); err != nil {
			slog.Error("failed to remove arr queue item", "component", "downloads", "queue_id", queueID, "error", err)
		} else {
			slog.Info("removed arr queue item", "component", "downloads", "queue_id", queueID, "service", category, "blocklist", blockParam)
		}
		return
	}
	slog.Warn("hash not found in arr queue", "component", "downloads", "hash", shortHash(hash), "service", category)
}

func (h *Handler) unmonitorArrItem(ctx context.Context, arrClient *arr.Client, apiVer, category, hash string) {
	records, err := arrClient.GetAllQueueRecords(ctx, apiVer, "")
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
				h.unmonitorMovie(ctx, arrClient, apiVer, movieID)
			}
		case "sonarr":
			episodeID := int(floatVal(rec, "episodeId"))
			if episodeID > 0 {
				h.unmonitorEpisode(ctx, arrClient, apiVer, episodeID)
			}
		}
		return
	}
}

func (h *Handler) unmonitorMovie(ctx context.Context, arrClient *arr.Client, apiVer string, movieID int) {
	movie, err := arrClient.GetMovie(ctx, apiVer, movieID)
	if err != nil {
		return
	}
	movie["monitored"] = false
	if err := arrClient.UpdateMovie(ctx, apiVer, movieID, movie); err != nil {
		slog.Error("failed to unmonitor movie", "component", "downloads", "movie_id", movieID, "error", err)
	} else {
		slog.Info("unmonitored movie", "component", "downloads", "movie_id", movieID)
	}
}

func (h *Handler) unmonitorEpisode(ctx context.Context, arrClient *arr.Client, apiVer string, episodeID int) {
	episode, err := arrClient.GetEpisode(ctx, apiVer, episodeID)
	if err != nil {
		return
	}
	episode["monitored"] = false
	if err := arrClient.UpdateEpisode(ctx, apiVer, episodeID, episode); err != nil {
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

// shortHash returns the first 8 chars of a hash string, for logging.
func shortHash(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}
