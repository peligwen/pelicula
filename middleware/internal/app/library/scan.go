package library

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"pelicula-api/httputil"
)

// ── Scan types ────────────────────────────────────────────────────────────────

// ScanRequest is the body accepted by HandleLibraryScan.
type ScanRequest struct {
	Files   []ScanFile `json:"files"`
	Folders []string   `json:"folders,omitempty"` // directories to walk recursively
}

// ScanFile is a single media file in a scan request.
type ScanFile struct {
	Path    string   `json:"path"`
	Size    int64    `json:"size"`
	Aliases []string `json:"-"` // populated server-side during hardlink collapse; not part of client input
}

// MatchItem is the per-file result of a library scan.
type MatchItem struct {
	File          string      `json:"file"`
	Size          int64       `json:"size"`
	Match         *MediaMatch `json:"match,omitempty"`
	Status        string      `json:"status"` // new / exists / unmatched
	SuggestedPath string      `json:"suggestedPath,omitempty"`
	GroupKey      string      `json:"groupKey,omitempty"`
	Aliases       []string    `json:"aliases,omitempty"` // other paths that are hardlinks to the same inode
}

// MediaMatch describes the *arr match found for a file.
type MediaMatch struct {
	Type       string `json:"type"` // movie / series
	Title      string `json:"title"`
	Year       int    `json:"year"`
	TmdbID     int    `json:"tmdbId,omitempty"`
	TvdbID     int    `json:"tvdbId,omitempty"`
	Season     int    `json:"season,omitempty"`
	Episode    int    `json:"episode,omitempty"`
	Confidence string `json:"confidence"` // high / medium / low
}

// ── Path guards ───────────────────────────────────────────────────────────────

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true,
	".ts": true, ".wmv": true, ".mov": true, ".flv": true,
}

var skipDirs = map[string]bool{
	"extras": true, "featurettes": true, "behind the scenes": true,
	"interviews": true, "deleted scenes": true, "trailers": true,
	"shorts": true, "samples": true,
}

// browseRoots returns the allowed top-level browse directories.
func browseRoots() []string {
	roots := []string{"/downloads", "/media"}
	if src := strings.TrimSpace(os.Getenv("IMPORT_SOURCE_DIR")); src != "" {
		roots = append(roots, "/import-source")
	}
	return roots
}

// isAllowedBrowsePath validates that a path is under one of the allowed roots.
func isAllowedBrowsePath(p string) bool {
	return IsUnderPrefixes(p, browseRoots())
}

// IsUnderPrefixes reports whether the cleaned path equals or is nested under
// one of the given prefixes.
func IsUnderPrefixes(p string, prefixes []string) bool {
	clean := filepath.Clean(p)
	for _, prefix := range prefixes {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

// walkVideoFiles recursively walks dir and appends video files to out (up to
// the remaining capacity of cap). Returns the updated slice and updated cap.
func walkVideoFiles(dir string, out []ScanFile, cap int) ([]ScanFile, int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, cap
	}
	for _, de := range entries {
		if cap <= 0 {
			break
		}
		name := de.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		if de.IsDir() {
			if skipDirs[strings.ToLower(name)] {
				continue
			}
			out, cap = walkVideoFiles(full, out, cap)
		} else {
			ext := strings.ToLower(filepath.Ext(name))
			if !videoExts[ext] {
				continue
			}
			info, err := de.Info()
			if err != nil {
				continue
			}
			if strings.Contains(strings.ToLower(name), "sample") && info.Size() < 100<<20 {
				continue
			}
			out = append(out, ScanFile{Path: full, Size: info.Size()})
			cap--
		}
	}
	return out, cap
}

// collapseHardlinks deduplicates files that share an inode (hardlinks).
func collapseHardlinks(files []ScanFile) []ScanFile {
	type inodeKey struct{ dev, ino uint64 }
	seen := make(map[inodeKey]int) // value = index in result
	result := make([]ScanFile, 0, len(files))
	for _, f := range files {
		var st syscall.Stat_t
		if err := syscall.Stat(f.Path, &st); err != nil {
			result = append(result, f)
			continue
		}
		key := inodeKey{uint64(st.Dev), uint64(st.Ino)}
		if idx, ok := seen[key]; ok {
			result[idx].Aliases = append(result[idx].Aliases, f.Path)
		} else {
			seen[key] = len(result)
			result = append(result, f)
		}
	}
	return result
}

// ── HandleLibraryScan ────────────────────────────────────────────────────────

// HandleLibraryScan receives a list of local media files and returns a match
// plan — each file matched to a Radarr movie or Sonarr series with a
// confidence level and a suggested library path.
func (h *Handler) HandleLibraryScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 5<<20) // 5 MB
	var req ScanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Expand any selected folders into individual video files.
	const maxScanFiles = 2000
	remaining := maxScanFiles - len(req.Files)
	for _, dir := range req.Folders {
		clean := filepath.Clean(dir)
		if !isAllowedBrowsePath(clean) {
			continue
		}
		req.Files, remaining = walkVideoFiles(clean, req.Files, remaining)
		if remaining <= 0 {
			break
		}
	}

	// Collapse hardlinks — multiple paths to the same inode become one entry.
	req.Files = collapseHardlinks(req.Files)

	if len(req.Files) == 0 {
		httputil.WriteJSON(w, []MatchItem{})
		return
	}

	sonarrKey, radarrKey, _ := h.Svc.Keys()
	if radarrKey == "" || sonarrKey == "" {
		httputil.WriteError(w, "API keys not loaded — is the stack wired?", http.StatusServiceUnavailable)
		return
	}

	existingMovies := h.loadExistingMovieIDs(radarrKey)
	existingSeries := h.loadExistingSeriesIDs(sonarrKey)

	// Lookup cache to avoid hammering the *arr APIs with duplicate titles.
	cache := make(map[string]*MediaMatch)
	var cacheMu sync.Mutex

	// Worker pool — 5 concurrent lookups.
	const workers = 5
	type job struct {
		file ScanFile
		idx  int
	}

	results := make([]MatchItem, len(req.Files))
	jobs := make(chan job, len(req.Files))
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				results[j.idx] = h.matchFile(
					j.file, radarrKey, sonarrKey,
					existingMovies, existingSeries,
					cache, &cacheMu,
				)
			}
		}()
	}
	for i, f := range req.Files {
		jobs <- job{f, i}
	}
	close(jobs)
	wg.Wait()

	// Assign stable group keys so the UI can identify duplicate sets.
	assignGroupKeys(results)

	high, medium, low, unmatched, exists := 0, 0, 0, 0, 0
	for _, item := range results {
		switch item.Status {
		case "exists":
			exists++
		case "unmatched":
			unmatched++
		default:
			if item.Match != nil {
				switch item.Match.Confidence {
				case "high":
					high++
				case "medium":
					medium++
				case "low":
					low++
				}
			}
		}
	}
	slog.Info("library scan complete", "component", "library",
		"files", len(req.Files),
		"high", high, "medium", medium, "low", low,
		"unmatched", unmatched, "exists", exists)

	httputil.WriteJSON(w, results)
}

// ── *arr helpers ──────────────────────────────────────────────────────────────

// loadExistingMovieIDs returns a set of tmdbIds already in Radarr.
func (h *Handler) loadExistingMovieIDs(apiKey string) map[int]bool {
	data, err := h.Svc.ArrGet(h.RadarrURL, apiKey, "/api/v3/movie")
	if err != nil {
		return nil
	}
	var movies []map[string]any
	if err := json.Unmarshal(data, &movies); err != nil {
		return nil
	}
	m := make(map[int]bool, len(movies))
	for _, mv := range movies {
		m[int(floatVal(mv, "tmdbId"))] = true
	}
	return m
}

// loadExistingSeriesIDs returns a set of tvdbIds already in Sonarr.
func (h *Handler) loadExistingSeriesIDs(apiKey string) map[int]bool {
	data, err := h.Svc.ArrGet(h.SonarrURL, apiKey, "/api/v3/series")
	if err != nil {
		return nil
	}
	var series []map[string]any
	if err := json.Unmarshal(data, &series); err != nil {
		return nil
	}
	m := make(map[int]bool, len(series))
	for _, s := range series {
		m[int(floatVal(s, "tvdbId"))] = true
	}
	return m
}

// loadProfileNameMap returns name → id for quality profiles.
func (h *Handler) loadProfileNameMap(baseURL, apiKey string) (map[string]int, error) {
	data, err := h.Svc.ArrGet(baseURL, apiKey, "/api/v3/qualityprofile")
	if err != nil {
		return nil, err
	}
	var profiles []map[string]any
	if err := json.Unmarshal(data, &profiles); err != nil {
		return nil, err
	}
	m := make(map[string]int, len(profiles))
	for _, p := range profiles {
		m[strVal(p, "name")] = int(floatVal(p, "id"))
	}
	return m, nil
}

// ── JSON helpers (local copies to avoid import of cmd-level util) ────────────

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
