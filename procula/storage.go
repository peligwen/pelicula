package main

import (
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// FolderSize holds the computed size of a single monitored folder.
type FolderSize struct {
	Path  string `json:"path"`
	Label string `json:"label"`
	Size  int64  `json:"size"` // bytes; -1 means not yet computed
}

// FilesystemInfo represents one unique underlying filesystem.
// Multiple monitored folders may share a single filesystem (e.g. when all
// container paths are bind-mounted from the same host volume).
type FilesystemInfo struct {
	FsID      string       `json:"fs_id"`
	Total     int64        `json:"total"`
	Used      int64        `json:"used"`
	Available int64        `json:"available"`
	UsedPct   float64      `json:"used_pct"`
	Status    string       `json:"status"` // "ok", "warning", "critical"
	Folders   []FolderSize `json:"folders"`
}

// StorageReport is the response from GET /api/procula/storage.
type StorageReport struct {
	Filesystems []FilesystemInfo `json:"filesystems"`
	Timestamp   time.Time        `json:"timestamp"`
}

// monitoredVolumes is the set of paths to watch. Overridable in tests.
var monitoredVolumes = []struct {
	label string
	path  string
}{
	{"Downloads", "/downloads"},
	{"Movies", "/movies"},
	{"TV", "/tv"},
	{"Processing", "/processing"},
}

// folderSizes caches the most recent per-folder byte totals from computeFolderSizes.
var (
	folderSizeMu sync.RWMutex
	folderSizes  = map[string]int64{} // path → bytes
)

// fsIDForPath returns a stable string identifying the filesystem a path lives on.
// Uses the device number from syscall.Stat — the same device number means the
// same underlying filesystem. This works on Linux and macOS without build tags.
func fsIDForPath(path string) (string, error) {
	var s syscall.Stat_t
	if err := syscall.Stat(path, &s); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", s.Dev), nil
}

// getCachedFolderSizes returns a copy of the current folder size cache.
func getCachedFolderSizes() map[string]int64 {
	folderSizeMu.RLock()
	defer folderSizeMu.RUnlock()
	cp := make(map[string]int64, len(folderSizes))
	for k, v := range folderSizes {
		cp[k] = v
	}
	return cp
}

// computeFolderSizes walks each monitored volume and sums file sizes.
// Results are stored in the package-level cache. This is called from the
// background monitor goroutine — never from the API handler.
func computeFolderSizes() {
	result := make(map[string]int64, len(monitoredVolumes))
	for _, v := range monitoredVolumes {
		var total int64
		_ = filepath.WalkDir(v.path, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip inaccessible entries
			}
			if !d.IsDir() {
				if info, err := d.Info(); err == nil {
					total += info.Size()
				}
			}
			return nil
		})
		result[v.path] = total
	}
	folderSizeMu.Lock()
	folderSizes = result
	folderSizeMu.Unlock()
}

// buildStorageReport groups monitored volumes by their underlying filesystem
// and returns a report. Volumes that cannot be stat'd are skipped silently.
func buildStorageReport() StorageReport {
	report := StorageReport{Timestamp: time.Now().UTC()}

	type fsGroup struct {
		info  FilesystemInfo
		paths []struct{ path, label string }
	}
	groups := make(map[string]*fsGroup)
	order := []string{} // preserve first-seen order

	for _, v := range monitoredVolumes {
		id, err := fsIDForPath(v.path)
		if err != nil {
			slog.Warn("skipping volume", "component", "storage", "path", v.path, "error", err)
			continue
		}

		var stat syscall.Statfs_t
		if err := syscall.Statfs(v.path, &stat); err != nil {
			slog.Warn("skipping volume statfs", "component", "storage", "path", v.path, "error", err)
			continue
		}

		g, exists := groups[id]
		if !exists {
			total := int64(stat.Blocks) * int64(stat.Bsize)
			avail := int64(stat.Bavail) * int64(stat.Bsize)
			used := total - avail

			var usedPct float64
			if total > 0 {
				usedPct = float64(used) / float64(total) * 100
			}

			s := GetSettings(appDB)
			status := "ok"
			switch {
			case usedPct >= s.StorageCriticalPct:
				status = "critical"
			case usedPct >= s.StorageWarningPct:
				status = "warning"
			}

			g = &fsGroup{info: FilesystemInfo{
				FsID:      id,
				Total:     total,
				Used:      used,
				Available: avail,
				UsedPct:   usedPct,
				Status:    status,
			}}
			groups[id] = g
			order = append(order, id)
		}
		g.paths = append(g.paths, struct{ path, label string }{v.path, v.label})
	}

	// Attach cached folder sizes.
	sizes := getCachedFolderSizes()
	for _, id := range order {
		g := groups[id]
		for _, p := range g.paths {
			size, ok := sizes[p.path]
			if !ok {
				size = -1 // not yet computed
			}
			g.info.Folders = append(g.info.Folders, FolderSize{
				Path:  p.path,
				Label: p.label,
				Size:  size,
			})
		}
		report.Filesystems = append(report.Filesystems, g.info)
	}
	return report
}

// RunStorageMonitor computes folder sizes and checks disk thresholds every
// 5 minutes, emitting notification events when filesystems cross thresholds.
// It only re-alerts when the status changes or an hour has elapsed.
func RunStorageMonitor(configDir string) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	lastStatus := make(map[string]string)       // fsID → last alerted status
	lastAlertTime := make(map[string]time.Time) // fsID → last alert time

	check := func() {
		computeFolderSizes()
		report := buildStorageReport()
		for _, fsi := range report.Filesystems {
			if fsi.Status == "ok" {
				lastStatus[fsi.FsID] = "ok"
				continue
			}
			prev := lastStatus[fsi.FsID]
			since := time.Since(lastAlertTime[fsi.FsID])

			if fsi.Status != prev || (prev != "" && since >= time.Hour) {
				labels := make([]string, len(fsi.Folders))
				for i, f := range fsi.Folders {
					labels[i] = f.Label
				}
				msg := fmt.Sprintf("Storage %s: disk (%s) is %.0f%% full",
					fsi.Status, strings.Join(labels, ", "), fsi.UsedPct)
				event := NotificationEvent{
					ID:        fmt.Sprintf("storage_%s_%d", fsi.FsID, time.Now().UnixNano()),
					Timestamp: time.Now().UTC(),
					Type:      "storage_" + fsi.Status,
					Message:   msg,
				}
				appendToFeed(configDir, event)
				lastStatus[fsi.FsID] = fsi.Status
				lastAlertTime[fsi.FsID] = time.Now()
				slog.Warn("storage threshold crossed",
					"component", "storage",
					"fs_id", fsi.FsID,
					"folders", strings.Join(labels, ", "),
					"used_pct", fsi.UsedPct,
					"status", fsi.Status,
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
