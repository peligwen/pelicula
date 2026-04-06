package main

import (
	"fmt"
	"log/slog"
	"syscall"
	"time"
)

// VolumeInfo holds disk usage statistics for a single mount point.
type VolumeInfo struct {
	Path      string  `json:"path"`
	Label     string  `json:"label"`     // human-readable name
	Total     int64   `json:"total"`     // bytes
	Used      int64   `json:"used"`      // bytes
	Available int64   `json:"available"` // bytes
	UsedPct   float64 `json:"used_pct"`  // 0–100
	Status    string  `json:"status"`    // "ok", "warning", "critical"
}

// StorageReport is the response from GET /api/procula/storage.
type StorageReport struct {
	Volumes   []VolumeInfo `json:"volumes"`
	Timestamp time.Time    `json:"timestamp"`
}

// volumes to monitor: label → mount path inside the container.
var monitoredVolumes = []struct {
	label string
	path  string
}{
	{"Downloads", "/downloads"},
	{"Movies", "/movies"},
	{"TV", "/tv"},
	{"Processing", "/processing"},
}

// getVolumeInfo uses syscall.Statfs to read disk usage for the given path.
func getVolumeInfo(path, label string) (VolumeInfo, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return VolumeInfo{}, fmt.Errorf("statfs %s: %w", path, err)
	}

	total := int64(stat.Blocks) * int64(stat.Bsize)
	avail := int64(stat.Bavail) * int64(stat.Bsize)
	used := total - int64(stat.Bfree)*int64(stat.Bsize)

	var usedPct float64
	if total > 0 {
		usedPct = float64(used) / float64(total) * 100
	}

	s := GetSettings()
	status := "ok"
	switch {
	case usedPct >= s.StorageCriticalPct:
		status = "critical"
	case usedPct >= s.StorageWarningPct:
		status = "warning"
	}

	return VolumeInfo{
		Path:      path,
		Label:     label,
		Total:     total,
		Used:      used,
		Available: avail,
		UsedPct:   usedPct,
		Status:    status,
	}, nil
}

// buildStorageReport scans all monitored volumes and returns a report.
// Volumes that cannot be stat'd (e.g. not mounted) are skipped silently.
func buildStorageReport() StorageReport {
	report := StorageReport{Timestamp: time.Now().UTC()}
	for _, v := range monitoredVolumes {
		info, err := getVolumeInfo(v.path, v.label)
		if err != nil {
			slog.Warn("skipping volume", "component", "storage", "path", v.path, "error", err)
			continue
		}
		report.Volumes = append(report.Volumes, info)
	}
	return report
}

// RunStorageMonitor checks disk usage every 5 minutes and emits notification
// events when volumes cross warning or critical thresholds. It only fires
// again when the status changes or an hour has elapsed since the last alert.
func RunStorageMonitor(configDir string) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Track last alerted status and time per volume path.
	lastStatus := make(map[string]string)
	lastAlertTime := make(map[string]time.Time)

	check := func() {
		report := buildStorageReport()
		for _, v := range report.Volumes {
			if v.Status == "ok" {
				lastStatus[v.Path] = "ok"
				continue
			}
			prev := lastStatus[v.Path]
			since := time.Since(lastAlertTime[v.Path])

			// Emit if status changed, or if same non-ok status and 1 hour elapsed.
			if v.Status != prev || (prev != "" && since >= time.Hour) {
				msg := fmt.Sprintf("Storage %s: %s is %.0f%% full",
					v.Status, v.Label, v.UsedPct)
				event := NotificationEvent{
					ID:        fmt.Sprintf("storage_%s_%d", v.Label, time.Now().UnixNano()),
					Timestamp: time.Now().UTC(),
					Type:      "storage_" + v.Status,
					Message:   msg,
				}
				appendToFeed(configDir, event)
				lastStatus[v.Path] = v.Status
				lastAlertTime[v.Path] = time.Now()
				slog.Warn("storage threshold crossed",
					"component", "storage",
					"volume", v.Label,
					"used_pct", v.UsedPct,
					"status", v.Status,
				)
			}
		}
	}

	// Run once immediately at startup, then on ticker.
	check()
	for range ticker.C {
		check()
	}
}
