package library

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"pelicula-api/httputil"
)

// ── Apply types ───────────────────────────────────────────────────────────────

// ApplyRequest is the body accepted by HandleLibraryApply.
type ApplyRequest struct {
	Items    []ApplyItem `json:"items"`
	Strategy string      `json:"strategy"` // import / link / register (also accepts legacy: migrate / symlink / hardlink / keep)
	Validate bool        `json:"validate"` // forward to Procula for validation after apply
}

// ApplyItem describes one media item to apply.
type ApplyItem struct {
	Type           string `json:"type"` // movie / series
	TmdbID         int    `json:"tmdbId,omitempty"`
	TvdbID         int    `json:"tvdbId,omitempty"`
	Title          string `json:"title"`
	Year           int    `json:"year"`
	Season         int    `json:"season,omitempty"`
	Episode        int    `json:"episode,omitempty"`
	RootFolderPath string `json:"rootFolderPath"`
	Monitored      bool   `json:"monitored"`
	SourcePath     string `json:"sourcePath,omitempty"` // original file path, used for FS ops and Procula
	DestPath       string `json:"destPath,omitempty"`   // pre-computed destination (client-supplied for confirmation)
}

// LibraryApplyResult is the response shape for HandleLibraryApply.
type LibraryApplyResult struct {
	Added   int               `json:"added"`
	Skipped int               `json:"skipped"`
	Failed  int               `json:"failed"`
	Errors  []string          `json:"errors,omitempty"`
	Items   []ApplyItemResult `json:"items,omitempty"` // per-item detail for display
}

// ApplyItemResult is the per-item result within LibraryApplyResult.
type ApplyItemResult struct {
	Title string `json:"title"`
	Src   string `json:"src,omitempty"`
	Dest  string `json:"dest,omitempty"`
	FSOp  string `json:"fsOp,omitempty"` // "moved", "symlinked", "kept", "skipped", "failed"
	Error string `json:"error,omitempty"`
}

// ── HandleLibraryApply ───────────────────────────────────────────────────────

// HandleLibraryApply receives a list of matched items and registers them in
// Radarr/Sonarr with search disabled. The CLI has already performed any
// necessary filesystem operations (move / symlink) before calling this.
func (h *Handler) HandleLibraryApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 5<<20) // 5 MB
	var req ApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// ── Duplicate guard ──────────────────────────────────────────────────────
	{
		gkCount := make(map[string]int, len(req.Items))
		for _, item := range req.Items {
			gkCount[applyGroupKey(item)]++
		}
		var dups []string
		for k, n := range gkCount {
			if n > 1 {
				dups = append(dups, k)
			}
		}
		if len(dups) > 0 {
			sort.Strings(dups)
			httputil.WriteError(w,
				"duplicate group keys in apply request (resolve before applying): "+strings.Join(dups, ", "),
				http.StatusBadRequest)
			return
		}
	}

	sonarrKey, radarrKey, _ := h.Svc.Keys()
	if radarrKey == "" || sonarrKey == "" {
		httputil.WriteError(w, "API keys not loaded — is the stack wired?", http.StatusServiceUnavailable)
		return
	}

	if warns := h.CheckLibraryAccess(); len(warns) > 0 {
		httputil.WriteError(w, warns[0], http.StatusServiceUnavailable)
		return
	}

	existingMovies := h.loadExistingMovieIDs(radarrKey)
	existingSeries := h.loadExistingSeriesIDs(sonarrKey)

	movieProfiles, _ := h.loadProfileNameMap(h.RadarrURL, radarrKey)
	seriesProfiles, _ := h.loadProfileNameMap(h.SonarrURL, sonarrKey)

	// ── Filesystem operations (import / link) ────────────────────────────────
	libs := h.GetLibraries()
	dstRoots := make([]string, 0, len(libs))
	for _, lib := range libs {
		dstRoots = append(dstRoots, lib.ContainerPath())
	}
	movieRoot := h.FirstLibraryPath("radarr", "/media/movies")
	tvRoot := h.FirstLibraryPath("sonarr", "/media/tv")
	fsResults := applyFSOps(req.Items, req.Strategy, nil, dstRoots, movieRoot, tvRoot)

	type dedupeKey struct {
		kind string
		id   int
	}
	seen := make(map[dedupeKey]bool)

	result := &LibraryApplyResult{}
	var addedItems []ApplyItem
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)

	for idx, item := range req.Items {
		fsResult := fsResults[idx]

		if fsResult.op == "skipped" {
			mu.Lock()
			result.Skipped++
			result.Items = append(result.Items, ApplyItemResult{
				Title: item.Title, Src: item.SourcePath, FSOp: "skipped", Error: fsResult.err,
			})
			mu.Unlock()
			continue
		}

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
		go func(it ApplyItem, fsRes fsOpResult) {
			defer wg.Done()
			defer func() { <-sem }()
			var err error
			if it.Type == "movie" {
				err = h.applyMovie(radarrKey, it, movieProfiles)
			} else {
				err = h.applySeries(sonarrKey, it, seriesProfiles)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				result.Failed++
				result.Errors = append(result.Errors,
					fmt.Sprintf("%s %q: %v", it.Type, it.Title, err))
				result.Items = append(result.Items, ApplyItemResult{
					Title: it.Title, Src: it.SourcePath, FSOp: "failed", Error: err.Error(),
				})
			} else {
				result.Added++
				addedItems = append(addedItems, it)
				reportedOp := fsRes.op
				if reportedOp == "" {
					reportedOp = "kept"
				}
				result.Items = append(result.Items, ApplyItemResult{
					Title: it.Title, Src: it.SourcePath, Dest: it.DestPath, FSOp: reportedOp,
				})
			}
		}(item, fsResult)
	}
	wg.Wait()

	slog.Info("library apply complete", "component", "library",
		"added", result.Added, "skipped", result.Skipped, "failed", result.Failed)

	// Optionally forward successfully added items to Procula for validation.
	if req.Validate && len(addedItems) > 0 && h.ForwardToProc != nil {
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
			if err := h.ForwardToProc(source); err != nil {
				slog.Warn("failed to forward import to Procula",
					"component", "library", "title", item.Title, "error", err)
			}
		}
	}

	httputil.WriteJSON(w, result)
}

// applyGroupKey computes the group key from an ApplyItem.
func applyGroupKey(item ApplyItem) string {
	switch item.Type {
	case "movie":
		return fmt.Sprintf("movie:%d", item.TmdbID)
	case "series":
		return fmt.Sprintf("series:%d:s%de%d", item.TvdbID, item.Season, item.Episode)
	default:
		return "unknown:" + item.SourcePath
	}
}

// ── *arr apply helpers ────────────────────────────────────────────────────────

func (h *Handler) applyMovie(apiKey string, item ApplyItem, profMap map[string]int) error {
	data, err := h.Svc.ArrGet(h.RadarrURL, apiKey,
		"/api/v3/movie/lookup/tmdb?tmdbId="+itoa(item.TmdbID))
	if err != nil {
		return fmt.Errorf("lookup: %w", err)
	}
	var movie map[string]any
	if err := json.Unmarshal(data, &movie); err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	profileID := resolveProfileID("", profMap)
	root := item.RootFolderPath
	if root == "" {
		root = h.FirstLibraryPath("radarr", "/media/movies")
	}

	movie["tmdbId"] = item.TmdbID
	movie["qualityProfileId"] = profileID
	movie["rootFolderPath"] = root
	movie["monitored"] = item.Monitored
	movie["addOptions"] = map[string]any{
		"searchForMovie": false,
	}
	body, err := h.Svc.ArrPost(h.RadarrURL, apiKey, "/api/v3/movie", movie)
	if err != nil {
		if len(body) > 0 {
			return fmt.Errorf("%w: %s", err, bytes.TrimSpace(body))
		}
		return err
	}
	return nil
}

func (h *Handler) applySeries(apiKey string, item ApplyItem, profMap map[string]int) error {
	data, err := h.Svc.ArrGet(h.SonarrURL, apiKey,
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
		root = h.FirstLibraryPath("sonarr", "/media/tv")
	}

	show["tvdbId"] = item.TvdbID
	show["qualityProfileId"] = profileID
	show["rootFolderPath"] = root
	show["monitored"] = item.Monitored
	show["seasonFolder"] = true
	show["addOptions"] = map[string]any{
		"searchForMissingEpisodes": false,
	}
	body, err := h.Svc.ArrPost(h.SonarrURL, apiKey, "/api/v3/series", show)
	if err != nil {
		if len(body) > 0 {
			return fmt.Errorf("%w: %s", err, bytes.TrimSpace(body))
		}
		return err
	}
	return nil
}

// resolveProfileID looks up profile ID by name, falling back to first available.
func resolveProfileID(name string, nameMap map[string]int) int {
	if id, ok := nameMap[name]; ok {
		return id
	}
	for _, id := range nameMap {
		slog.Warn("quality profile not found, using fallback",
			"component", "library", "requested", name, "fallback_id", id)
		return id
	}
	slog.Warn("quality profile not found and no profiles available, using id=1",
		"component", "library", "requested", name)
	return 1
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}

// ── Filesystem helpers ────────────────────────────────────────────────────────

// fsOpResult records the outcome of a single filesystem operation.
type fsOpResult struct {
	op  string // "moved", "hardlinked", "symlinked", "kept", "skipped", "failed"
	err string // non-empty only when op == "failed" or "skipped"
}

// copyFile copies src to dst using a buffered io.Copy, removing dst on error.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		in.Close()
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		in.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		in.Close()
		os.Remove(dst)
		return err
	}
	in.Close()
	return nil
}

// moveFile moves src to dst, falling back to copy+remove when os.Rename fails
// across different filesystems (common on Synology bind mounts).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// applyFSOps iterates items and performs the filesystem operation dictated by
// strategy for each item that has a SourcePath. allowedSrcRoots defaults to
// the production browse roots when nil; allowedDstRoots defaults to empty.
// movieRoot and tvRoot are used to compute suggested destination paths when
// item.DestPath is empty; pass empty strings to use the hardcoded fallbacks.
func applyFSOps(items []ApplyItem, strategy string, allowedSrcRoots, allowedDstRoots []string, movieRoot, tvRoot string) []fsOpResult {
	results := make([]fsOpResult, len(items))
	for i := range results {
		results[i] = fsOpResult{op: "kept"}
	}

	// Normalise legacy strategy names to canonical ones.
	switch strategy {
	case "migrate":
		strategy = "import"
	case "symlink":
		strategy = "link"
	case "keep":
		strategy = "register"
	}
	if strategy == "register" {
		return results
	}
	if allowedSrcRoots == nil {
		allowedSrcRoots = browseRoots()
	}
	if allowedDstRoots == nil {
		allowedDstRoots = []string{}
	}

	for i := range items {
		item := &items[i]
		if item.SourcePath == "" {
			continue
		}
		src := filepath.Clean(item.SourcePath)
		if !IsUnderPrefixes(src, allowedSrcRoots) {
			results[i] = fsOpResult{op: "skipped", err: "path not allowed"}
			continue
		}

		dst := item.DestPath
		if dst == "" {
			if item.Type == "movie" {
				dst = suggestedMoviePath(movieRoot, item.Title, item.Year, filepath.Base(src))
			} else {
				dst = suggestedTVPath(tvRoot, item.Title, 0, filepath.Base(src))
			}
		}
		dst = filepath.Clean(dst)
		if !IsUnderPrefixes(dst, allowedDstRoots) {
			results[i] = fsOpResult{op: "skipped", err: "destination path not allowed"}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			slog.Warn("import: mkdir failed", "component", "library", "dst", dst, "error", err)
			results[i] = fsOpResult{op: "failed", err: err.Error()}
			continue
		}

		switch strategy {
		case "import":
			if err := moveFile(src, dst); err != nil {
				slog.Warn("import: move failed", "component", "library",
					"src", src, "dst", dst, "error", err)
				results[i] = fsOpResult{op: "failed", err: err.Error()}
			} else {
				item.SourcePath = dst
				item.DestPath = dst
				results[i] = fsOpResult{op: "moved"}
			}
		case "hardlink":
			if _, err := os.Lstat(dst); os.IsNotExist(err) {
				if err := os.Link(src, dst); err != nil {
					slog.Warn("import: hardlink failed", "component", "library",
						"src", src, "dst", dst, "error", err)
					results[i] = fsOpResult{op: "failed", err: err.Error()}
				} else {
					item.DestPath = dst
					results[i] = fsOpResult{op: "hardlinked"}
				}
			} else {
				item.DestPath = dst
				results[i] = fsOpResult{op: "hardlinked"}
			}
		case "link":
			if _, err := os.Lstat(dst); os.IsNotExist(err) {
				if err := os.Symlink(src, dst); err != nil {
					slog.Warn("import: symlink failed", "component", "library",
						"src", src, "dst", dst, "error", err)
					results[i] = fsOpResult{op: "failed", err: err.Error()}
				} else {
					item.DestPath = dst
					results[i] = fsOpResult{op: "symlinked"}
				}
			} else {
				item.DestPath = dst
				results[i] = fsOpResult{op: "symlinked"}
			}
		}
	}
	return results
}
