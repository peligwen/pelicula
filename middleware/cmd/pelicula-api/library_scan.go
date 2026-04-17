// Peligrosa: trust boundary layer (handleBrowse).
// The folder browser resolves symlinks and re-checks the resolved path against
// browse roots before listing — prevents path-traversal escape via symlinks.
// Library scan and browse are admin-only and do not touch untrusted user input;
// they are not part of the Peligrosa surface.
// See ../docs/PELIGROSA.md.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"pelicula-api/httputil"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

// ── Scan types ────────────────────────────────────────────────────────────────

type ScanRequest struct {
	Files   []ScanFile `json:"files"`
	Folders []string   `json:"folders,omitempty"` // directories to walk recursively
}

type ScanFile struct {
	Path    string   `json:"path"`
	Size    int64    `json:"size"`
	Aliases []string `json:"-"` // populated server-side during hardlink collapse; not part of client input
}

type MatchItem struct {
	File          string      `json:"file"`
	Size          int64       `json:"size"`
	Match         *MediaMatch `json:"match,omitempty"`
	Status        string      `json:"status"` // new / exists / unmatched
	SuggestedPath string      `json:"suggestedPath,omitempty"`
	GroupKey      string      `json:"groupKey,omitempty"`
	Aliases       []string    `json:"aliases,omitempty"` // other paths that are hardlinks to the same inode
}

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

// ── Browse types ─────────────────────────────────────────────────────────────

type BrowseEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	IsDir   bool      `json:"isDir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

type BrowseResponse struct {
	Entries   []BrowseEntry `json:"entries"`
	Truncated bool          `json:"truncated"`
}

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

// isAllowedBrowsePath validates that a path is under one of the allowed roots,
// preventing directory traversal attacks.
func isAllowedBrowsePath(p string) bool {
	return isUnderPrefixes(p, browseRoots())
}

// walkVideoFiles recursively walks dir and appends video files to out (up to
// the remaining capacity of cap). Returns the updated slice and updated cap.
// Reuses the same skip rules as handleBrowse: hidden files, skipDirs, non-video
// extensions, and sample files under 100 MB.
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

// handleBrowse returns a directory listing for the server-side folder browser.
// When called without a path, returns the allowed root directories.
func handleBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dir := r.URL.Query().Get("path")

	// No path — return top-level roots.
	if dir == "" {
		roots := browseRoots()
		entries := make([]BrowseEntry, 0, len(roots))
		for _, root := range roots {
			info, err := os.Stat(root)
			if err != nil {
				continue // root doesn't exist on this host
			}
			entries = append(entries, BrowseEntry{
				Name:    filepath.Base(root),
				Path:    root,
				IsDir:   true,
				ModTime: info.ModTime(),
			})
		}
		httputil.WriteJSON(w, BrowseResponse{Entries: entries})
		return
	}

	if !isAllowedBrowsePath(dir) {
		httputil.WriteError(w, "path not under an allowed directory", http.StatusForbidden)
		return
	}

	// Resolve symlinks so a symlink inside /downloads pointing to /etc can't
	// escape the allowed root. Re-check after resolution.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if os.IsNotExist(err) {
			httputil.WriteError(w, "directory not found", http.StatusNotFound)
		} else {
			httputil.WriteError(w, "path not under an allowed directory", http.StatusForbidden)
		}
		return
	}
	if !isAllowedBrowsePath(resolved) {
		httputil.WriteError(w, "path not under an allowed directory", http.StatusForbidden)
		return
	}
	dir = resolved

	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			httputil.WriteError(w, "directory not found", http.StatusNotFound)
		} else {
			httputil.WriteError(w, "failed to read directory", http.StatusInternalServerError)
		}
		return
	}

	const maxEntries = 500
	entries := make([]BrowseEntry, 0, len(dirEntries))
	truncated := false

	for _, de := range dirEntries {
		name := de.Name()
		// Skip hidden files/dirs.
		if strings.HasPrefix(name, ".") {
			continue
		}

		if de.IsDir() {
			// Skip known extras/junk directories.
			if skipDirs[strings.ToLower(name)] {
				continue
			}
			info, err := de.Info()
			if err != nil {
				continue
			}
			entries = append(entries, BrowseEntry{
				Name:    name,
				Path:    filepath.Join(dir, name),
				IsDir:   true,
				ModTime: info.ModTime(),
			})
		} else {
			ext := strings.ToLower(filepath.Ext(name))
			if !videoExts[ext] {
				continue
			}
			info, err := de.Info()
			if err != nil {
				continue
			}
			// Skip sample files (name contains "sample" and size < 100 MB).
			if strings.Contains(strings.ToLower(name), "sample") && info.Size() < 100<<20 {
				continue
			}
			entries = append(entries, BrowseEntry{
				Name:    name,
				Path:    filepath.Join(dir, name),
				IsDir:   false,
				Size:    info.Size(),
				ModTime: info.ModTime(),
			})
		}

		if len(entries) >= maxEntries {
			truncated = true
			break
		}
	}

	httputil.WriteJSON(w, BrowseResponse{Entries: entries, Truncated: truncated})
}

// handleLibraryScan receives a list of local media files and returns a match
// plan — each file matched to a Radarr movie or Sonarr series with a
// confidence level and a suggested library path.
func handleLibraryScan(w http.ResponseWriter, r *http.Request) {
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

	// Collapse hardlinks — multiple paths to the same inode become one entry,
	// with the extra paths recorded in Aliases so the UI can display them.
	req.Files = collapseHardlinks(req.Files)

	if len(req.Files) == 0 {
		httputil.WriteJSON(w, []MatchItem{})
		return
	}

	sonarrKey, radarrKey, _ := services.Keys()
	if radarrKey == "" || sonarrKey == "" {
		httputil.WriteError(w, "API keys not loaded — is the stack wired?", http.StatusServiceUnavailable)
		return
	}

	existingMovies := loadExistingMovieIDs(radarrKey)
	existingSeries := loadExistingSeriesIDs(sonarrKey)

	// Lookup cache to avoid hammering the *arr APIs with duplicate titles.
	// Key format: "movie:<title>" or "series:<title>"
	cache := make(map[string]*MediaMatch)
	var cacheMu sync.Mutex

	// Worker pool — 5 concurrent lookups so we don't overwhelm Radarr/Sonarr.
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
				results[j.idx] = matchFile(
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
	for _, r := range results {
		switch r.Status {
		case "exists":
			exists++
		case "unmatched":
			unmatched++
		default:
			switch r.Match.Confidence {
			case "high":
				high++
			case "medium":
				medium++
			case "low":
				low++
			}
		}
	}
	slog.Info("library scan complete", "component", "library",
		"files", len(req.Files),
		"high", high, "medium", medium, "low", low,
		"unmatched", unmatched, "exists", exists)

	httputil.WriteJSON(w, results)
}

// ── Match helpers ─────────────────────────────────────────────────────────────

func matchFile(
	f ScanFile,
	radarrKey, sonarrKey string,
	existingMovies, existingSeries map[int]bool,
	cache map[cacheKeyT]*MediaMatch,
	cacheMu *sync.Mutex,
) MatchItem {
	item := MatchItem{File: f.Path, Size: f.Size, Status: "unmatched"}

	filename := filepath.Base(f.Path)
	title, year, isTV := cleanFilename(filename)
	if title == "" {
		return item
	}

	encoded := url.QueryEscape(title)

	if isTV {
		m := cachedLookup(cache, cacheMu, "series:"+title, func() *MediaMatch {
			return lookupSeries(sonarrKey, encoded, title, year)
		})
		if m != nil {
			season := extractSeason(filename)
			episode := extractEpisode(filename)
			mc := *m // copy — do not mutate the shared cache entry
			mc.Season = season
			mc.Episode = episode
			item.Match = &mc
			item.SuggestedPath = suggestedTVPath(m.Title, season, filename)
			item.Aliases = f.Aliases
			if existingSeries[m.TvdbID] {
				item.Status = "exists"
			} else {
				item.Status = "new"
			}
		}
	} else {
		// Try Radarr first, fall back to Sonarr.
		m := cachedLookup(cache, cacheMu, "movie:"+title, func() *MediaMatch {
			return lookupMovie(radarrKey, encoded, title, year)
		})
		if m == nil {
			m = cachedLookup(cache, cacheMu, "series:"+title, func() *MediaMatch {
				return lookupSeries(sonarrKey, encoded, title, year)
			})
		}
		if m != nil {
			item.Aliases = f.Aliases
			if m.Type == "movie" {
				item.Match = m // movie matches have no Season/Episode; safe to share
				item.SuggestedPath = suggestedMoviePath(m.Title, m.Year, filename)
				if existingMovies[m.TmdbID] {
					item.Status = "exists"
				} else {
					item.Status = "new"
				}
			} else {
				season := extractSeason(filename)
				episode := extractEpisode(filename)
				mc := *m
				mc.Season = season
				mc.Episode = episode
				item.Match = &mc
				item.SuggestedPath = suggestedTVPath(m.Title, season, filename)
				if existingSeries[m.TvdbID] {
					item.Status = "exists"
				} else {
					item.Status = "new"
				}
			}
		}
	}

	// If the file is already at its suggested destination path, mark it as
	// in-place so the UI can skip the strategy prompt.
	if item.Status == "new" && item.SuggestedPath != "" &&
		filepath.Clean(f.Path) == filepath.Clean(item.SuggestedPath) {
		item.Status = "in_place"
	}

	return item
}

type cacheKeyT = string

func cachedLookup(
	cache map[cacheKeyT]*MediaMatch,
	mu *sync.Mutex,
	key string,
	lookup func() *MediaMatch,
) *MediaMatch {
	mu.Lock()
	if m, ok := cache[key]; ok {
		mu.Unlock()
		return m
	}
	mu.Unlock()

	m := lookup()

	mu.Lock()
	cache[key] = m
	mu.Unlock()
	return m
}

func lookupMovie(apiKey, encoded, cleanTitle string, year int) *MediaMatch {
	data, err := services.ArrGet(radarrURL, apiKey, "/api/v3/movie/lookup?term="+encoded)
	if err != nil {
		return nil
	}
	var results []map[string]any
	if err := json.Unmarshal(data, &results); err != nil || len(results) == 0 {
		return nil
	}
	for i, r := range results {
		if i >= 5 {
			break
		}
		mt := strVal(r, "title")
		my := int(floatVal(r, "year"))
		confidence := scoreMatch(cleanTitle, year, mt, my)
		if confidence == "unmatched" {
			continue
		}
		return &MediaMatch{
			Type:       "movie",
			Title:      mt,
			Year:       my,
			TmdbID:     int(floatVal(r, "tmdbId")),
			Confidence: confidence,
		}
	}
	return nil
}

func lookupSeries(apiKey, encoded, cleanTitle string, year int) *MediaMatch {
	data, err := services.ArrGet(sonarrURL, apiKey, "/api/v3/series/lookup?term="+encoded)
	if err != nil {
		return nil
	}
	var results []map[string]any
	if err := json.Unmarshal(data, &results); err != nil || len(results) == 0 {
		return nil
	}
	for i, r := range results {
		if i >= 5 {
			break
		}
		mt := strVal(r, "title")
		my := int(floatVal(r, "year"))
		confidence := scoreMatch(cleanTitle, year, mt, my)
		if confidence == "unmatched" {
			continue
		}
		return &MediaMatch{
			Type:       "series",
			Title:      mt,
			Year:       my,
			TvdbID:     int(floatVal(r, "tvdbId")),
			TmdbID:     int(floatVal(r, "tmdbId")),
			Confidence: confidence,
		}
	}
	return nil
}

// ── Filename parsing ──────────────────────────────────────────────────────────

var (
	// yearRe matches a standalone 4-digit year (1900–2099).
	yearRe = regexp.MustCompile(`\b(19\d{2}|20[012]\d)\b`)
	// tvEpRe matches SxxExx / sxxexx episode patterns.
	tvEpRe = regexp.MustCompile(`(?i)\bS\d{1,2}E\d{1,2}\b`)
	// seasonRe captures the season number from a TV filename.
	seasonRe = regexp.MustCompile(`(?i)\bS(\d{1,2})E\d{1,2}\b`)
	// episodeRe captures the episode number from a TV filename.
	episodeRe = regexp.MustCompile(`(?i)\bS\d{1,2}E(\d{1,2})\b`)
)

// cleanFilename extracts a search-ready title, year, and TV flag from a
// media filename. Handles dot-delimited (`The.Dark.Knight.2008.mkv`),
// paren-year (`Alien (1979).mkv`), and TV episode (`Breaking.Bad.S01E01`) patterns.
func cleanFilename(filename string) (title string, year int, isTV bool) {
	// Drop extension.
	name := filename
	if ext := filepath.Ext(filename); ext != "" {
		name = name[:len(name)-len(ext)]
	}

	// Replace dot/underscore separators with spaces first so regexes
	// work on the cleaned string.
	name = strings.NewReplacer(".", " ", "_", " ").Replace(name)

	isTV = tvEpRe.MatchString(name)

	cutIdx := len(name)

	// Find year — cut title there.
	if loc := yearRe.FindStringIndex(name); loc != nil {
		digits := yearRe.FindString(name[loc[0]:])
		year, _ = strconv.Atoi(digits)
		if loc[0] < cutIdx {
			cutIdx = loc[0]
		}
	}

	// Find TV episode tag — cut title there too (whichever is earlier).
	if loc := tvEpRe.FindStringIndex(name); loc != nil {
		if loc[0] < cutIdx {
			cutIdx = loc[0]
		}
	}

	// Trim trailing separators and junk left after the cut.
	title = strings.TrimRight(name[:cutIdx], " -_([")
	title = strings.TrimSpace(strings.Join(strings.Fields(title), " "))
	return
}

// extractSeason returns the season number from a TV filename, or 0 if unknown.
func extractSeason(filename string) int {
	name := strings.NewReplacer(".", " ", "_", " ").Replace(filename)
	if m := seasonRe.FindStringSubmatch(name); m != nil {
		s, _ := strconv.Atoi(m[1])
		return s
	}
	return 0
}

// extractEpisode returns the episode number from a TV filename, or 0 if unknown.
func extractEpisode(filename string) int {
	name := strings.NewReplacer(".", " ", "_", " ").Replace(filename)
	if m := episodeRe.FindStringSubmatch(name); m != nil {
		e, _ := strconv.Atoi(m[1])
		return e
	}
	return 0
}

// collapseHardlinks deduplicates files that share an inode (hardlinks).
// The first path encountered for a given inode is kept as the canonical file;
// subsequent paths for the same inode are appended to its Aliases slice.
// Files that cannot be stat'd are included as-is.
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

// matchItemGroupKey returns a stable group key for a MatchItem.
// Movies: "movie:<tmdbId>". TV episodes: "series:<tvdbId>:s<season>e<episode>".
// Unmatched items each get a unique key derived from their file path.
func matchItemGroupKey(item MatchItem) string {
	if item.Match == nil {
		return "unmatched:" + item.File
	}
	switch item.Match.Type {
	case "movie":
		if item.Match.TmdbID > 0 {
			return fmt.Sprintf("movie:%d", item.Match.TmdbID)
		}
	case "series":
		if item.Match.TvdbID > 0 {
			return fmt.Sprintf("series:%d:s%de%d", item.Match.TvdbID, item.Match.Season, item.Match.Episode)
		}
	}
	return "unmatched:" + item.File
}

// assignGroupKeys sets GroupKey on each MatchItem in place.
func assignGroupKeys(items []MatchItem) {
	for i, item := range items {
		items[i].GroupKey = matchItemGroupKey(item)
	}
}

// ── Scoring ───────────────────────────────────────────────────────────────────

func scoreMatch(cleanedTitle string, year int, matchTitle string, matchYear int) string {
	ct := normalizeTitle(cleanedTitle)
	mt := normalizeTitle(matchTitle)
	if ct == "" || mt == "" {
		return "unmatched"
	}

	yearOK := year == 0 || matchYear == 0 || absInt(year-matchYear) <= 1

	if ct == mt {
		if yearOK {
			return "high"
		}
		return "medium"
	}
	if strings.Contains(mt, ct) || strings.Contains(ct, mt) {
		if yearOK {
			return "medium"
		}
		return "low"
	}

	// Word overlap.
	ctWords := strings.Fields(ct)
	mtSet := make(map[string]bool, len(mt))
	for _, w := range strings.Fields(mt) {
		mtSet[w] = true
	}
	matches := 0
	for _, w := range ctWords {
		if mtSet[w] {
			matches++
		}
	}
	if len(ctWords) == 0 {
		return "unmatched"
	}
	ratio := matches * 100 / len(ctWords)
	switch {
	case ratio >= 80 && yearOK:
		return "medium"
	case ratio >= 60:
		return "low"
	default:
		return "unmatched"
	}
}

func normalizeTitle(s string) string {
	s = strings.ToLower(s)
	for _, pfx := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, pfx) {
			s = s[len(pfx):]
		}
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(strings.Join(strings.Fields(b.String()), " "))
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
