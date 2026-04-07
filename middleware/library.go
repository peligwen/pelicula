package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ── Scan types ────────────────────────────────────────────────────────────────

type ScanRequest struct {
	Files []ScanFile `json:"files"`
}

type ScanFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type MatchItem struct {
	File          string      `json:"file"`
	Size          int64       `json:"size"`
	Match         *MediaMatch `json:"match,omitempty"`
	Status        string      `json:"status"` // new / exists / unmatched
	SuggestedPath string      `json:"suggestedPath,omitempty"`
}

type MediaMatch struct {
	Type       string `json:"type"` // movie / series
	Title      string `json:"title"`
	Year       int    `json:"year"`
	TmdbID     int    `json:"tmdbId,omitempty"`
	TvdbID     int    `json:"tvdbId,omitempty"`
	Confidence string `json:"confidence"` // high / medium / low
}

// ── Apply types ───────────────────────────────────────────────────────────────

type ApplyRequest struct {
	Items    []ApplyItem `json:"items"`
	Strategy string      `json:"strategy"` // migrate / symlink / keep
	Validate bool        `json:"validate"` // forward to Procula for validation after apply
}

type ApplyItem struct {
	Type           string `json:"type"` // movie / series
	TmdbID         int    `json:"tmdbId,omitempty"`
	TvdbID         int    `json:"tvdbId,omitempty"`
	Title          string `json:"title"`
	Year           int    `json:"year"`
	RootFolderPath string `json:"rootFolderPath"`
	Monitored      bool   `json:"monitored"`
	SourcePath     string `json:"sourcePath,omitempty"` // original file path, used for Procula validation
}

type LibraryApplyResult struct {
	Added   int      `json:"added"`
	Skipped int      `json:"skipped"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
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
	roots := []string{"/movies", "/tv", "/downloads"}
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

// ── Handlers ─────────────────────────────────────────────────────────────────

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
		writeJSON(w, BrowseResponse{Entries: entries})
		return
	}

	if !isAllowedBrowsePath(dir) {
		writeError(w, "path not under an allowed directory", http.StatusForbidden)
		return
	}

	// Resolve symlinks so a symlink inside /downloads pointing to /etc can't
	// escape the allowed root. Re-check after resolution.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, "directory not found", http.StatusNotFound)
		} else {
			writeError(w, "path not under an allowed directory", http.StatusForbidden)
		}
		return
	}
	if !isAllowedBrowsePath(resolved) {
		writeError(w, "path not under an allowed directory", http.StatusForbidden)
		return
	}
	dir = resolved

	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, "directory not found", http.StatusNotFound)
		} else {
			writeError(w, "failed to read directory", http.StatusInternalServerError)
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

	writeJSON(w, BrowseResponse{Entries: entries, Truncated: truncated})
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
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Files) == 0 {
		writeJSON(w, []MatchItem{})
		return
	}

	sonarrKey, radarrKey, _ := services.Keys()
	if radarrKey == "" || sonarrKey == "" {
		writeError(w, "API keys not loaded — is the stack wired?", http.StatusServiceUnavailable)
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

	writeJSON(w, results)
}

// handleLibraryApply receives a list of matched items and registers them in
// Radarr/Sonarr with search disabled. The CLI has already performed any
// necessary filesystem operations (move / symlink) before calling this.
func handleLibraryApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 5<<20) // 5 MB
	var req ApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	sonarrKey, radarrKey, _ := services.Keys()
	if radarrKey == "" || sonarrKey == "" {
		writeError(w, "API keys not loaded — is the stack wired?", http.StatusServiceUnavailable)
		return
	}

	existingMovies := loadExistingMovieIDs(radarrKey)
	existingSeries := loadExistingSeriesIDs(sonarrKey)

	movieProfiles, _ := loadProfileNameMap(radarrURL, radarrKey)
	seriesProfiles, _ := loadProfileNameMap(sonarrURL, sonarrKey)

	// Deduplicate by (type, id) — multiple files from the same title map to
	// one Radarr/Sonarr entry.
	type dedupeKey struct {
		kind string
		id   int
	}
	seen := make(map[dedupeKey]bool)

	result := &LibraryApplyResult{}
	var addedItems []ApplyItem // tracks successfully added items for Procula forwarding
	var mu sync.Mutex
	var wg sync.WaitGroup
	// Semaphore: max 5 concurrent Radarr/Sonarr add calls.
	sem := make(chan struct{}, 5)

	for _, item := range req.Items {
		var k dedupeKey
		switch item.Type {
		case "movie":
			k = dedupeKey{"movie", item.TmdbID}
		case "series":
			k = dedupeKey{"series", item.TvdbID}
		default:
			continue
		}

		if seen[k] {
			mu.Lock()
			result.Skipped++
			mu.Unlock()
			continue
		}
		seen[k] = true

		if (item.Type == "movie" && existingMovies[item.TmdbID]) ||
			(item.Type == "series" && existingSeries[item.TvdbID]) {
			mu.Lock()
			result.Skipped++
			mu.Unlock()
			continue
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(it ApplyItem) {
			defer wg.Done()
			defer func() { <-sem }()
			var err error
			if it.Type == "movie" {
				err = applyMovie(radarrKey, it, movieProfiles)
			} else {
				err = applySeries(sonarrKey, it, seriesProfiles)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				result.Failed++
				result.Errors = append(result.Errors,
					fmt.Sprintf("%s %q: %v", it.Type, it.Title, err))
			} else {
				result.Added++
				addedItems = append(addedItems, it)
			}
		}(item)
	}
	wg.Wait()

	slog.Info("library apply complete", "component", "library",
		"added", result.Added, "skipped", result.Skipped, "failed", result.Failed)

	// Optionally forward successfully added items to Procula for validation.
	if req.Validate && len(addedItems) > 0 {
		proculaURL := proculaBaseURL() + "/api/procula/jobs"
		for _, item := range addedItems {
			if item.SourcePath == "" {
				continue
			}
			arrType := "radarr"
			if item.Type == "series" {
				arrType = "sonarr"
			}
			source := ProculaJobSource{
				Type:    item.Type,
				Title:   item.Title,
				Year:    item.Year,
				Path:    item.SourcePath,
				ArrType: arrType,
			}
			if err := forwardToProcula(proculaURL, source); err != nil {
				slog.Warn("failed to forward import to Procula",
					"component", "library", "title", item.Title, "error", err)
			}
		}
	}

	writeJSON(w, result)
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
			item.Match = m
			season := extractSeason(filename)
			item.SuggestedPath = suggestedTVPath(m.Title, season, filename)
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
			item.Match = m
			if m.Type == "movie" {
				item.SuggestedPath = suggestedMoviePath(m.Title, m.Year, filename)
				if existingMovies[m.TmdbID] {
					item.Status = "exists"
				} else {
					item.Status = "new"
				}
			} else {
				season := extractSeason(filename)
				item.SuggestedPath = suggestedTVPath(m.Title, season, filename)
				if existingSeries[m.TvdbID] {
					item.Status = "exists"
				} else {
					item.Status = "new"
				}
			}
		}
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

func suggestedMoviePath(title string, year int, filename string) string {
	folder := title
	if year > 0 {
		folder = fmt.Sprintf("%s (%d)", title, year)
	}
	return "/movies/" + folder + "/" + filepath.Base(filename)
}

func suggestedTVPath(title string, season int, filename string) string {
	if season > 0 {
		return fmt.Sprintf("/tv/%s/Season %02d/%s", title, season, filepath.Base(filename))
	}
	return fmt.Sprintf("/tv/%s/%s", title, filepath.Base(filename))
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

// ── Apply helpers ─────────────────────────────────────────────────────────────

func applyMovie(apiKey string, item ApplyItem, profMap map[string]int) error {
	// Look up full movie details for the add payload.
	data, err := services.ArrGet(radarrURL, apiKey,
		"/api/v3/movie/lookup/tmdb?tmdbId="+itoa(item.TmdbID))
	if err != nil {
		return fmt.Errorf("lookup: %w", err)
	}
	var movie map[string]any
	if err := json.Unmarshal(data, &movie); err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	profileID := resolveProfileID("", profMap) // use first available
	// rootFolderPath from the item (set by the CLI based on strategy)
	root := item.RootFolderPath
	if root == "" {
		root = "/movies"
	}

	payload := map[string]any{
		"tmdbId":           item.TmdbID,
		"title":            movie["title"],
		"qualityProfileId": profileID,
		"rootFolderPath":   root,
		"monitored":        item.Monitored,
		"addOptions": map[string]any{
			"searchForMovie": false,
		},
	}
	if _, err := services.ArrPost(radarrURL, apiKey, "/api/v3/movie", payload); err != nil {
		return err
	}
	return nil
}

func applySeries(apiKey string, item ApplyItem, profMap map[string]int) error {
	// Look up full series details.
	data, err := services.ArrGet(sonarrURL, apiKey,
		"/api/v3/series/lookup?term=tvdb:"+itoa(item.TvdbID))
	if err != nil {
		return fmt.Errorf("lookup: %w", err)
	}
	var shows []map[string]any
	if err := json.Unmarshal(data, &shows); err != nil || len(shows) == 0 {
		return fmt.Errorf("series not found")
	}
	show := shows[0]

	profileID := resolveProfileID("", profMap)
	root := item.RootFolderPath
	if root == "" {
		root = "/tv"
	}

	payload := map[string]any{
		"tvdbId":           item.TvdbID,
		"title":            show["title"],
		"qualityProfileId": profileID,
		"rootFolderPath":   root,
		"monitored":        item.Monitored,
		"seasonFolder":     true,
		"seasons":          show["seasons"],
		"addOptions": map[string]any{
			"searchForMissingEpisodes": false,
		},
	}
	if _, err := services.ArrPost(sonarrURL, apiKey, "/api/v3/series", payload); err != nil {
		return err
	}
	return nil
}
