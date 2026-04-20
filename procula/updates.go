package procula

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// UpdateInfo holds the result of the latest update check.
type UpdateInfo struct {
	CurrentVersion string    `json:"current_version"`
	LatestVersion  string    `json:"latest_version,omitempty"`
	UpdateAvail    bool      `json:"update_available"`
	CheckedAt      time.Time `json:"checked_at"`
	Error          string    `json:"error,omitempty"`
}

var (
	updateMu     sync.RWMutex
	cachedUpdate *UpdateInfo
)

// getCachedUpdate returns the most recent update check result, or a zero value
// if no check has been performed yet.
func getCachedUpdate() UpdateInfo {
	updateMu.RLock()
	defer updateMu.RUnlock()
	if cachedUpdate != nil {
		return *cachedUpdate
	}
	return UpdateInfo{CurrentVersion: Version}
}

// checkForUpdates queries the GitHub releases API and returns an UpdateInfo.
func checkForUpdates() UpdateInfo {
	info := UpdateInfo{
		CurrentVersion: Version,
		CheckedAt:      time.Now().UTC(),
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet,
		"https://api.github.com/repos/peligwen/pelicula/releases/latest", nil)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Pelicula/"+Version+" (+https://github.com/peligwen/pelicula)")

	resp, err := client.Do(req)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// No release published yet — not an error.
		return info
	}
	if resp.StatusCode != http.StatusOK {
		info.Error = "GitHub API returned HTTP " + resp.Status
		return info
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		info.Error = err.Error()
		return info
	}

	info.LatestVersion = release.TagName
	info.UpdateAvail = isNewerVersion(release.TagName, Version)
	return info
}

// isNewerVersion returns true when latest is a non-empty tag that is strictly
// greater than current using numeric semver comparison (MAJOR.MINOR.PATCH).
// Dev builds never show an update notice.
func isNewerVersion(latest, current string) bool {
	if latest == "" || current == "dev" {
		return false
	}
	lv := parseSemver(strings.TrimPrefix(latest, "v"))
	cv := parseSemver(strings.TrimPrefix(current, "v"))
	for i := range lv {
		if lv[i] > cv[i] {
			return true
		}
		if lv[i] < cv[i] {
			return false
		}
	}
	return false
}

// parseSemver splits a "MAJOR.MINOR.PATCH" string into a [3]int.
// Missing or non-numeric components default to 0.
func parseSemver(s string) [3]int {
	var v [3]int
	parts := strings.SplitN(s, ".", 3)
	for i, p := range parts {
		if i >= 3 {
			break
		}
		n, _ := strconv.Atoi(p)
		v[i] = n
	}
	return v
}

// RunUpdateChecker starts a background loop that checks for updates on startup
// (after a short delay) and then every 24 hours. Results are cached in memory
// and persisted to disk so they survive Procula restarts.
func RunUpdateChecker(configDir string) {
	cachePath := filepath.Join(configDir, "procula", "update_check.json")

	// Load cached result from disk so we can surface it immediately.
	if data, err := os.ReadFile(cachePath); err == nil {
		var cached UpdateInfo
		if json.Unmarshal(data, &cached) == nil {
			updateMu.Lock()
			cachedUpdate = &cached
			updateMu.Unlock()
		}
	}

	doCheck := func() {
		info := checkForUpdates()
		updateMu.Lock()
		cachedUpdate = &info
		updateMu.Unlock()

		if data, err := json.MarshalIndent(info, "", "  "); err == nil {
			if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err == nil {
				os.WriteFile(cachePath, data, 0644) //nolint:errcheck
			}
		}

		if info.Error != "" {
			slog.Warn("update check failed", "component", "updates", "error", info.Error)
		} else if info.UpdateAvail {
			slog.Info("update available",
				"component", "updates",
				"current", info.CurrentVersion,
				"latest", info.LatestVersion,
			)
		} else {
			slog.Info("up to date", "component", "updates", "version", info.CurrentVersion)
		}
	}

	// Skip the HTTP check if we have a cached result less than 24h old.
	updateMu.RLock()
	cached := cachedUpdate
	updateMu.RUnlock()

	if cached == nil || time.Since(cached.CheckedAt) >= 24*time.Hour {
		// Delay slightly so Procula is fully booted before hitting the network.
		time.Sleep(30 * time.Second)
		doCheck()
	}

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		doCheck()
	}
}
