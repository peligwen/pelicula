package main

import (
	"encoding/json"
	"fmt"
	"log"
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
	log.Printf("[downloads] %s torrent %s", action, req.Hash[:8])
	writeJSON(w, map[string]string{"status": action})
}

// handleDownloadCancel removes a torrent and unmonitors the item in Radarr/Sonarr.
func handleDownloadCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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
	var baseURL, apiKey, apiVer string
	switch req.Category {
	case "radarr":
		baseURL, apiKey, apiVer = radarrURL, services.RadarrKey, "/api/v3"
	case "sonarr":
		baseURL, apiKey, apiVer = sonarrURL, services.SonarrKey, "/api/v3"
	}

	// Remove from *arr queue and optionally blocklist
	if baseURL != "" && apiKey != "" {
		removeFromArrQueue(baseURL, apiKey, apiVer, req.Hash, req.Blocklist)
		if !req.Blocklist {
			unmonitorArrItem(baseURL, apiKey, apiVer, req.Category, req.Hash)
		}
	}

	// Delete torrent + files from qBittorrent
	if err := services.QbtPost("/api/v2/torrents/delete", "hashes="+url.QueryEscape(req.Hash)+"&deleteFiles=true"); err != nil {
		log.Printf("[downloads] failed to delete torrent from qBittorrent: %v", err)
	}

	action := "cancelled"
	if req.Blocklist {
		action = "cancelled+blocklisted"
		if req.Reason != "" {
			action += " (" + req.Reason + ")"
		}
	}
	log.Printf("[downloads] %s torrent %s (%s)", action, req.Hash[:8], req.Category)
	writeJSON(w, map[string]string{"status": "removed"})
}

// removeFromArrQueue finds the download in the *arr queue and removes it.
func removeFromArrQueue(baseURL, apiKey, apiVer, hash string, blocklist bool) {
	data, err := services.ArrGet(baseURL, apiKey, apiVer+"/queue?pageSize=100&includeUnknownMovieItems=true&includeUnknownSeriesItems=true")
	if err != nil {
		log.Printf("[downloads] failed to fetch %s queue: %v", baseURL, err)
		return
	}

	var queue struct {
		Records []map[string]any `json:"records"`
	}
	if json.Unmarshal(data, &queue) != nil {
		return
	}

	for _, rec := range queue.Records {
		dlHash := strVal(rec, "downloadId")
		if !strEqualFold(dlHash, hash) {
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
			log.Printf("[downloads] failed to remove queue item %d: %v", queueID, err)
		} else {
			log.Printf("[downloads] removed queue item %d from %s (blocklist=%s)", queueID, baseURL, blockParam)
		}
		return
	}
	log.Printf("[downloads] hash %s not found in %s queue", hash[:8], baseURL)
}

// unmonitorArrItem finds the movie/series associated with a download hash and unmonitors it.
func unmonitorArrItem(baseURL, apiKey, apiVer, category, hash string) {
	data, err := services.ArrGet(baseURL, apiKey, apiVer+"/queue?pageSize=100")
	if err != nil {
		return
	}

	var queue struct {
		Records []map[string]any `json:"records"`
	}
	if json.Unmarshal(data, &queue) != nil {
		return
	}

	for _, rec := range queue.Records {
		if !strEqualFold(strVal(rec, "downloadId"), hash) {
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
		log.Printf("[downloads] failed to unmonitor movie %d: %v", movieID, err)
	} else {
		log.Printf("[downloads] unmonitored movie %d", movieID)
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
		log.Printf("[downloads] failed to unmonitor episode %d: %v", episodeID, err)
	} else {
		log.Printf("[downloads] unmonitored episode %d", episodeID)
	}
}

func strEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func strVal(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func floatVal(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

func intVal(m map[string]any, key string) int64 {
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	return 0
}
