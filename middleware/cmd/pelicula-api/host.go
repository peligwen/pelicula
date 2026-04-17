package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"pelicula-api/httputil"
	"strconv"
	"strings"
	"syscall"
)

type hostDisk struct {
	Free    uint64  `json:"free"`
	Total   uint64  `json:"total"`
	UsedPct float64 `json:"used_pct"`
}

type hostLibrary struct {
	Movies int `json:"movies"`
	Series int `json:"series"`
}

type hostResponse struct {
	UptimeSeconds float64     `json:"uptime_seconds"`
	Disk          hostDisk    `json:"disk"`
	Library       hostLibrary `json:"library"`
}

// handleHost returns container uptime, media disk usage, and library counts.
func handleHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	resp := hostResponse{
		UptimeSeconds: readUptime(),
		Disk:          diskStats(),
		Library:       libraryCounts(),
	}
	httputil.WriteJSON(w, resp)
}

// readUptime reads /proc/uptime and returns the system uptime in seconds.
func readUptime() float64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	return v
}

// diskStats returns free/total bytes and used% for the library mount point.
// Falls back to the working directory if LIBRARY_DIR is not set.
func diskStats() hostDisk {
	path := os.Getenv("LIBRARY_DIR")
	if path == "" {
		path = "/media" // always mounted in the container; reflects real library disk
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return hostDisk{}
	}
	total := st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	used := total - free
	usedPct := 0.0
	if total > 0 {
		usedPct = float64(used) / float64(total) * 100
	}
	return hostDisk{Free: free, Total: total, UsedPct: usedPct}
}

// libraryCounts queries Radarr and Sonarr for movie/series counts.
func libraryCounts() hostLibrary {
	sonarrKey, radarrKey, _ := services.Keys()
	lib := hostLibrary{}

	if radarrKey != "" {
		if body, err := services.ArrGet(radarrURL, radarrKey, "/api/v3/movie"); err == nil {
			lib.Movies = jsonArrayLen(body)
		}
	}
	if sonarrKey != "" {
		if body, err := services.ArrGet(sonarrURL, sonarrKey, "/api/v3/series"); err == nil {
			lib.Series = jsonArrayLen(body)
		}
	}
	return lib
}

// jsonArrayLen decodes a JSON array and returns its length without
// fully materialising the items.
func jsonArrayLen(data []byte) int {
	var arr []json.RawMessage
	if err := json.Unmarshal(data, &arr); err != nil {
		return 0
	}
	return len(arr)
}

// formatUptime formats uptime seconds for display (e.g. "3d 4h", "2h 15m").
func formatUptime(secs float64) string {
	s := int(secs)
	d := s / 86400
	h := (s % 86400) / 3600
	m := (s % 3600) / 60
	if d > 0 {
		return fmt.Sprintf("%dd %dh", d, h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}
