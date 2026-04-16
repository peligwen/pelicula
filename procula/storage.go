package main

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// videoExtensions is the set of file extensions treated as video files for the
// has_media check on unregistered folders.
var videoExtensions = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true,
	".ts": true, ".wmv": true, ".mov": true, ".flv": true,
}

// FolderSize holds the computed size of a single monitored folder.
type FolderSize struct {
	Path       string `json:"path"`
	Label      string `json:"label"`
	Size       int64  `json:"size"` // bytes; -1 means not yet computed
	Registered bool   `json:"registered"`
	Slug       string `json:"slug,omitempty"`
	HasMedia   bool   `json:"has_media,omitempty"`
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

type monitoredVolume struct {
	label      string
	path       string
	registered bool   // true = known library or system dir; false = discovered but unclaimed
	slug       string // library slug; empty for non-library dirs
}

// monitoredVolumesOverride, when non-nil, replaces the dynamic discovery in
// getMonitoredVolumes. Set only in tests to inject controlled paths.
var monitoredVolumesOverride []monitoredVolume

// getMonitoredVolumes dynamically builds the list of paths to watch.
// It always includes the system dirs /downloads and /processing, adds one
// entry per registered library, then discovers any unregistered subdirectories
// under /media.
func getMonitoredVolumes() []monitoredVolume {
	if monitoredVolumesOverride != nil {
		return monitoredVolumesOverride
	}
	vols := []monitoredVolume{
		{label: "Downloads", path: "/downloads", registered: true},
		{label: "Processing", path: "/processing", registered: true},
	}

	// Add one entry per registered library.
	libs := getProculaLibraries()
	libPaths := make(map[string]bool, len(libs))
	for _, lib := range libs {
		p := lib.ContainerPath() // /media/<slug>
		vols = append(vols, monitoredVolume{
			label:      lib.Name,
			path:       p,
			registered: true,
			slug:       lib.Slug,
		})
		libPaths[p] = true
	}

	// Discover unregistered subdirectories under /media.
	entries, err := os.ReadDir("/media")
	if err == nil {
		for _, de := range entries {
			if !de.IsDir() || strings.HasPrefix(de.Name(), ".") {
				continue
			}
			p := "/media/" + de.Name()
			if !libPaths[p] {
				vols = append(vols, monitoredVolume{
					label:      de.Name(),
					path:       p,
					registered: false,
				})
			}
		}
	}
	return vols
}

// folderSizes caches the most recent per-folder byte totals from computeFolderSizes.
// folderHasMedia caches whether unregistered folders contain any video files.
var (
	folderSizeMu   sync.RWMutex
	folderSizes    = map[string]int64{} // path → bytes
	folderHasMedia = map[string]bool{}  // path → has video file
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

// getCachedFolderHasMedia returns a copy of the current has-media cache.
func getCachedFolderHasMedia() map[string]bool {
	folderSizeMu.RLock()
	defer folderSizeMu.RUnlock()
	cp := make(map[string]bool, len(folderHasMedia))
	for k, v := range folderHasMedia {
		cp[k] = v
	}
	return cp
}

// computeFolderSizes walks each monitored volume and sums file sizes.
// For unregistered volumes it also sets a flag if any video file is found.
// Results are stored in the package-level cache. This is called from the
// background monitor goroutine — never from the API handler.
func computeFolderSizes() {
	vols := getMonitoredVolumes()
	result := make(map[string]int64, len(vols))
	hasMedia := make(map[string]bool, len(vols))
	for _, v := range vols {
		var total int64
		found := false
		_ = filepath.WalkDir(v.path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip inaccessible entries
			}
			if !d.IsDir() {
				if info, err := d.Info(); err == nil {
					total += info.Size()
				}
				if !v.registered && !found {
					ext := strings.ToLower(filepath.Ext(d.Name()))
					if videoExtensions[ext] {
						found = true
					}
				}
			}
			return nil
		})
		result[v.path] = total
		if !v.registered {
			hasMedia[v.path] = found
		}
	}
	folderSizeMu.Lock()
	folderSizes = result
	folderHasMedia = hasMedia
	folderSizeMu.Unlock()
}

// buildStorageReport groups monitored volumes by their underlying filesystem
// and returns a report. Volumes that cannot be stat'd are skipped silently.
func buildStorageReport() StorageReport {
	report := StorageReport{Timestamp: time.Now().UTC()}

	type fsGroupPath struct {
		path       string
		label      string
		registered bool
		slug       string
	}
	type fsGroup struct {
		info  FilesystemInfo
		paths []fsGroupPath
	}
	groups := make(map[string]*fsGroup)
	order := []string{} // preserve first-seen order

	for _, v := range getMonitoredVolumes() {
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
		g.paths = append(g.paths, fsGroupPath{path: v.path, label: v.label, registered: v.registered, slug: v.slug})
	}

	// Attach cached folder sizes and has-media flags.
	sizes := getCachedFolderSizes()
	hasMedia := getCachedFolderHasMedia()
	for _, id := range order {
		g := groups[id]
		for _, p := range g.paths {
			size, ok := sizes[p.path]
			if !ok {
				size = -1 // not yet computed
			}
			g.info.Folders = append(g.info.Folders, FolderSize{
				Path:       p.path,
				Label:      p.label,
				Size:       size,
				Registered: p.registered,
				Slug:       p.slug,
				HasMedia:   hasMedia[p.path],
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
