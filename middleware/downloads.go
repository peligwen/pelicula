package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
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

	data, err := services.QbtGet("/api/v2/torrents/info")
	if err != nil {
		writeError(w, "failed to reach qBittorrent: "+err.Error(), http.StatusBadGateway)
		return
	}

	var rawTorrents []map[string]any
	if err := json.Unmarshal(data, &rawTorrents); err != nil {
		writeError(w, "failed to parse torrent data", http.StatusInternalServerError)
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

	statsData, err := services.QbtGet("/api/v2/transfer/info")
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

	writeJSON(w, DownloadsResponse{
		Torrents: torrents,
		Stats:    stats,
	})
}

func handleDownloadStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	statsData, err := services.QbtGet("/api/v2/transfer/info")
	if err != nil {
		writeError(w, "failed to reach qBittorrent", http.StatusBadGateway)
		return
	}

	var rawStats map[string]any
	if err := json.Unmarshal(statsData, &rawStats); err != nil {
		writeError(w, "failed to parse stats", http.StatusInternalServerError)
		return
	}

	stats := DownloadStats{
		DLSpeed: intVal(rawStats, "dl_info_speed"),
		UPSpeed: intVal(rawStats, "up_info_speed"),
	}
	writeJSON(w, stats)
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
		writeError(w, "invalid request", http.StatusBadRequest)
		return
	}

	// qBittorrent v5+ renamed pause/resume to stop/start
	var endpoint string
	if req.Paused {
		endpoint = "/api/v2/torrents/stop"
	} else {
		endpoint = "/api/v2/torrents/start"
	}

	if err := services.QbtPost(endpoint, "hashes="+url.QueryEscape(req.Hash)); err != nil {
		writeError(w, "qBittorrent error: "+err.Error(), http.StatusBadGateway)
		return
	}

	action := "resumed"
	if req.Paused {
		action = "paused"
	}
	slog.Info("torrent state changed", "component", "downloads", "action", action, "hash", shortHash(req.Hash))
	writeJSON(w, map[string]string{"status": action})
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
		writeError(w, "invalid request", http.StatusBadRequest)
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
	}

	// Unmonitor before removing from queue (unmonitor needs the queue entry to find the item ID)
	if baseURL != "" && apiKey != "" {
		if !req.Blocklist {
			unmonitorArrItem(baseURL, apiKey, apiVer, req.Category, req.Hash)
		}
		removeFromArrQueue(baseURL, apiKey, apiVer, req.Hash, req.Blocklist)
	}

	// Delete torrent + files from qBittorrent
	if err := services.QbtPost("/api/v2/torrents/delete", "hashes="+url.QueryEscape(req.Hash)+"&deleteFiles=true"); err != nil {
		slog.Error("failed to delete torrent from qBittorrent", "component", "downloads", "error", err)
	}

	action := "cancelled"
	if req.Blocklist {
		action = "cancelled+blocklisted"
	}
	slog.Info("torrent cancelled", "component", "downloads", "action", action, "hash", shortHash(req.Hash), "category", req.Category, "blocklist", req.Blocklist, "reason", req.Reason)
	writeJSON(w, map[string]string{"status": "removed"})
}
