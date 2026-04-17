package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"pelicula-api/httputil"
)

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

type DownloadStats struct {
	DLSpeed int64 `json:"dlspeed"`
	UPSpeed int64 `json:"upspeed"`
	Active  int   `json:"active"`
	Queued  int   `json:"queued"`
}

type DownloadsResponse struct {
	Torrents []Torrent     `json:"torrents"`
	Stats    DownloadStats `json:"stats"`
}

func handleDownloads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	qbtTorrents, err := services.Qbt.ListTorrents(r.Context())
	if err != nil {
		httputil.WriteError(w, "failed to reach qBittorrent: "+err.Error(), http.StatusBadGateway)
		return
	}

	torrents := make([]Torrent, 0, len(qbtTorrents))
	active := 0
	queued := 0

	for _, qt := range qbtTorrents {
		t := Torrent{
			Hash:     qt.Hash,
			Name:     qt.Name,
			Progress: qt.Progress,
			DLSpeed:  qt.DLSpeed,
			UPSpeed:  qt.UPSpeed,
			ETA:      qt.ETA,
			State:    qt.State,
			Size:     qt.Size,
			Category: qt.Category,
		}
		torrents = append(torrents, t)

		switch t.State {
		case "downloading", "uploading", "stalledDL", "stalledUP", "forcedDL", "forcedUP":
			active++
		case "queuedDL", "queuedUP":
			queued++
		}
	}

	var stats DownloadStats
	stats.Active = active
	stats.Queued = queued

	if ti, err := services.Qbt.GetTransferInfo(r.Context()); err == nil {
		stats.DLSpeed = ti.DLSpeed
		stats.UPSpeed = ti.UPSpeed
	}

	httputil.WriteJSON(w, DownloadsResponse{
		Torrents: torrents,
		Stats:    stats,
	})
}

func handleDownloadStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ti, err := services.Qbt.GetTransferInfo(r.Context())
	if err != nil {
		httputil.WriteError(w, "failed to reach qBittorrent", http.StatusBadGateway)
		return
	}

	httputil.WriteJSON(w, DownloadStats{
		DLSpeed: ti.DLSpeed,
		UPSpeed: ti.UPSpeed,
	})
}

// handleDownloadPause pauses or resumes a torrent in qBittorrent.
func handleDownloadPause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10) // 64 KB
	var req struct {
		Hash   string `json:"hash"`
		Paused bool   `json:"paused"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Hash == "" {
		httputil.WriteError(w, "invalid request", http.StatusBadRequest)
		return
	}

	// qBittorrent v5+ renamed pause/resume to stop/start
	var qbtErr error
	if req.Paused {
		qbtErr = services.Qbt.StopTorrent(r.Context(), req.Hash)
	} else {
		qbtErr = services.Qbt.StartTorrent(r.Context(), req.Hash)
	}
	if err := qbtErr; err != nil {
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

// handleDownloadCancel removes a torrent and unmonitors the item in Radarr/Sonarr.
func handleDownloadCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10) // 64 KB
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

	// Determine which *arr app owns this download
	sonarrKey, radarrKey, _ := services.Keys()
	var baseURL, apiKey, apiVer string
	switch req.Category {
	case "radarr":
		baseURL, apiKey, apiVer = radarrURL, radarrKey, "/api/v3"
	case "sonarr":
		baseURL, apiKey, apiVer = sonarrURL, sonarrKey, "/api/v3"
	default:
		httputil.WriteError(w, "unknown download category", http.StatusUnprocessableEntity)
		return
	}

	// Unmonitor before removing from queue (unmonitor needs the queue entry to find the item ID)
	if baseURL != "" && apiKey != "" {
		if !req.Blocklist {
			unmonitorArrItem(baseURL, apiKey, apiVer, req.Category, req.Hash)
		}
		removeFromArrQueue(baseURL, apiKey, apiVer, req.Hash, req.Blocklist)
	}

	// Delete torrent + files from qBittorrent
	if err := services.Qbt.DeleteTorrent(r.Context(), req.Hash); err != nil {
		slog.Error("failed to delete torrent from qBittorrent", "component", "downloads", "error", err)
	}

	action := "cancelled"
	if req.Blocklist {
		action = "cancelled+blocklisted"
	}
	slog.Info("torrent cancelled", "component", "downloads", "action", action, "hash", shortHash(req.Hash), "category", req.Category, "blocklist", req.Blocklist, "reason", req.Reason)
	httputil.WriteJSON(w, map[string]string{"status": "removed"})
}
