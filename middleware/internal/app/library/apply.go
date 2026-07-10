package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

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
	Edition        string `json:"edition,omitempty"`    // cut label for multi-version movies (e.g. "Theatrical Cut", "Redux")
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

// HandleLibraryApply receives a list of matched items, performs the requested
// filesystem operation (move / symlink / hardlink, or none for strategy
// "register") for each item, and registers it in Radarr/Sonarr with search
// disabled. The filesystem operation and the *arr registration are
// interleaved one item at a time — not run as two separate batch passes —
// so that a crash or a registration failure only ever leaves the single
// in-flight item moved-but-unregistered instead of the whole request (see
// MWA-6).
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

	ctx := r.Context()
	existingMovies := h.loadExistingMovieIDs(ctx)
	existingSeries := h.loadExistingSeriesIDs(ctx)

	movieProfiles, _ := loadProfileNameMap(ctx, h.Svc.RadarrClient())
	seriesProfiles, _ := loadProfileNameMap(ctx, h.Svc.SonarrClient())

	// ── Filesystem operation + *arr registration, interleaved per item ───────
	// fsSess computes the filesystem operation (import / link / hardlink, or
	// "kept" for strategy "register") for one item at a time; each item is
	// moved immediately before its own *arr registration is attempted, not as
	// two separate batch passes over the whole request. That bounds the blast
	// radius of a crash or a registration failure to the single item in
	// flight — every earlier item in the request either completed both steps
	// or (if its own registration failed) is reported with the destination it
	// was already moved to; every later item hasn't been touched on disk yet.
	allowedDstRoots := h.applyAllowedDstRoots
	if allowedDstRoots == nil {
		libs := h.GetLibraries()
		allowedDstRoots = make([]string, 0, len(libs))
		for _, lib := range libs {
			allowedDstRoots = append(allowedDstRoots, lib.ContainerPath())
		}
	}
	movieRoot := h.FirstLibraryPath("radarr", "/media/movies")
	tvRoot := h.FirstLibraryPath("sonarr", "/media/tv")
	fsSess := newFSOpSession(req.Strategy, h.applyAllowedSrcRoots, allowedDstRoots, movieRoot, tvRoot)

	type dedupeKey struct {
		kind string
		id   int
	}
	seen := make(map[dedupeKey]bool)

	result := &LibraryApplyResult{}
	var addedItems []ApplyItem

	for idx := range req.Items {
		item := &req.Items[idx]
		fsResult := fsSess.apply(item)

		if fsResult.op == "skipped" {
			result.Skipped++
			result.Items = append(result.Items, ApplyItemResult{
				Title: item.Title, Src: item.SourcePath, FSOp: "skipped", Error: fsResult.err,
			})
			continue
		}
		if fsResult.op == "failed" {
			// The filesystem operation itself failed (permission denied, disk
			// full, MkdirAll error, ...) — the file never reached its
			// destination, so there is nothing to register with *arr yet.
			result.Failed++
			result.Errors = append(result.Errors,
				fmt.Sprintf("%s %q: filesystem operation failed: %s", item.Type, item.Title, fsResult.err))
			result.Items = append(result.Items, ApplyItemResult{
				Title: item.Title, Src: item.SourcePath, FSOp: "failed", Error: fsResult.err,
			})
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
			// Non-primary edition: file placed on disk by fsSess, but we skip
			// Radarr/Sonarr registration (1:1 per tmdbId/tvdbId). Report it as
			// placed and include in Procula forwarding.
			reportedOp := fsResult.op
			if reportedOp == "" {
				reportedOp = "kept"
			}
			result.Added++
			addedItems = append(addedItems, *item)
			result.Items = append(result.Items, ApplyItemResult{
				Title: item.Title, Src: item.SourcePath, Dest: item.DestPath, FSOp: reportedOp,
			})
			continue
		}
		seen[k] = true

		if (item.Type == "movie" && existingMovies[item.TmdbID]) ||
			(item.Type == "series" && existingSeries[item.TvdbID]) {
			result.Skipped++
			continue
		}

		var err error
		if item.Type == "movie" {
			err = h.applyMovie(ctx, radarrKey, *item, movieProfiles)
		} else {
			err = h.applySeries(ctx, sonarrKey, *item, seriesProfiles)
		}
		if err != nil {
			result.Failed++
			errMsg := err.Error()
			if fsMoved(fsResult.op) {
				// The item's file already landed at its destination before
				// registration was attempted — say so, and name the path, so
				// an admin can find and manually register it instead of it
				// silently rotting in the library root (MWA-6).
				errMsg = fmt.Sprintf("file already %s to %s, but registration failed: %s",
					fsResult.op, item.DestPath, errMsg)
			}
			result.Errors = append(result.Errors,
				fmt.Sprintf("%s %q: %s", item.Type, item.Title, errMsg))
			result.Items = append(result.Items, ApplyItemResult{
				Title: item.Title, Src: item.SourcePath, Dest: item.DestPath, FSOp: "failed", Error: errMsg,
			})
			continue
		}

		result.Added++
		addedItems = append(addedItems, *item)
		reportedOp := fsResult.op
		if reportedOp == "" {
			reportedOp = "kept"
		}
		result.Items = append(result.Items, ApplyItemResult{
			Title: item.Title, Src: item.SourcePath, Dest: item.DestPath, FSOp: reportedOp,
		})
	}

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

// applyGroupKey computes the group key from an ApplyItem. When a movie item
// carries a non-empty Edition, the edition is included in the key so that
// multiple cuts of the same film (different editions) are treated as distinct
// entries rather than duplicates.
func applyGroupKey(item ApplyItem) string {
	switch item.Type {
	case "movie":
		if item.Edition != "" {
			return fmt.Sprintf("movie:%d:%s", item.TmdbID, strings.ToLower(item.Edition))
		}
		return fmt.Sprintf("movie:%d", item.TmdbID)
	case "series":
		return fmt.Sprintf("series:%d:s%de%d", item.TvdbID, item.Season, item.Episode)
	default:
		return "unknown:" + item.SourcePath
	}
}

// ── *arr apply helpers ────────────────────────────────────────────────────────

func (h *Handler) applyMovie(ctx context.Context, _ string, item ApplyItem, profMap map[string]int) error {
	data, err := h.Svc.RadarrClient().Get(ctx,
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
	body, err := h.Svc.RadarrClient().Post(ctx, "/api/v3/movie", movie)
	if err != nil {
		if len(body) > 0 {
			return fmt.Errorf("%w: %s", err, bytes.TrimSpace(body))
		}
		return err
	}
	return nil
}

func (h *Handler) applySeries(ctx context.Context, _ string, item ApplyItem, profMap map[string]int) error {
	shows, err := h.Svc.SonarrClient().LookupSeries(ctx, "/api/v3", "tvdb:"+itoa(item.TvdbID))
	if err != nil {
		return fmt.Errorf("lookup: %w", err)
	}
	if len(shows) == 0 {
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
	body, err := h.Svc.SonarrClient().Post(ctx, "/api/v3/series", show)
	if err != nil {
		if len(body) > 0 {
			return fmt.Errorf("%w: %s", err, bytes.TrimSpace(body))
		}
		return err
	}
	return nil
}

// resolveProfileID looks up profile ID by name. When name is empty (the wizard
// has no profile picker today), it returns the lowest profile ID for a
// deterministic default — Radarr/Sonarr's onboarding-default profile is
// typically the lowest-numbered one. When a non-empty name is requested but
// not found, we still fall back deterministically and log a warning so the
// mismatch is visible.
func resolveProfileID(name string, nameMap map[string]int) int {
	if id, ok := nameMap[name]; ok {
		return id
	}
	fallback := 0
	for _, id := range nameMap {
		if fallback == 0 || id < fallback {
			fallback = id
		}
	}
	if fallback == 0 {
		fallback = 1
	}
	if name != "" {
		slog.Warn("quality profile not found, using fallback",
			"component", "library", "requested", name, "fallback_id", fallback)
	}
	return fallback
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

// moveFile moves src to dst. Rename across filesystems returns EXDEV; only
// that case warrants a copy+delete fallback. Other errors should surface so
// callers see the real cause (permission denied, disk full, read-only dst,
// etc.) rather than a confusing copy failure or a partial dst file.
func moveFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) || linkErr.Err != syscall.EXDEV {
		return err
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// resolveRoots returns each input root with symlinks resolved. Non-existent or
// otherwise unresolvable roots are passed through unchanged so callers can
// keep using them for the textual prefix check on the unresolved source path.
func resolveRoots(roots []string) []string {
	out := make([]string, len(roots))
	for i, r := range roots {
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			out[i] = rr
		} else {
			out[i] = r
		}
	}
	return out
}

// fsMoved reports whether op represents a filesystem relocation that already
// happened (the item's file is physically sitting at its destination), as
// opposed to "kept" (strategy "register": no relocation) or a non-terminal
// state. Used to decide whether a subsequent *arr registration failure needs
// to tell the admin where the file already went (MWA-6).
func fsMoved(op string) bool {
	switch op {
	case "moved", "hardlinked", "symlinked":
		return true
	default:
		return false
	}
}

// fsOpSession holds the strategy and roots shared across every item in one
// apply request, computed once, so callers can apply the filesystem
// operation to items one at a time (interleaved with *arr registration)
// instead of only as a single batch pass.
type fsOpSession struct {
	strategy         string // normalised: "import" | "link" | "hardlink" | "register"
	allowedSrcRoots  []string
	allowedDstRoots  []string
	resolvedSrcRoots []string
	movieRoot        string
	tvRoot           string
}

// newFSOpSession normalises strategy and, unless it's "register" (no
// filesystem operation is ever performed), resolves allowedSrcRoots /
// allowedDstRoots. allowedSrcRoots defaults to the production browse roots
// when nil; allowedDstRoots defaults to empty (nothing passes) when nil.
// movieRoot and tvRoot are used to compute suggested destination paths when
// an item's DestPath is empty; pass empty strings to use the hardcoded
// fallbacks.
func newFSOpSession(strategy string, allowedSrcRoots, allowedDstRoots []string, movieRoot, tvRoot string) *fsOpSession {
	// Normalise legacy strategy names to canonical ones.
	switch strategy {
	case "migrate":
		strategy = "import"
	case "symlink":
		strategy = "link"
	case "keep":
		strategy = "register"
	}
	sess := &fsOpSession{strategy: strategy, movieRoot: movieRoot, tvRoot: tvRoot}
	if strategy == "register" {
		return sess
	}
	if allowedSrcRoots == nil {
		allowedSrcRoots = browseRoots()
	}
	if allowedDstRoots == nil {
		allowedDstRoots = []string{}
	}
	sess.allowedSrcRoots = allowedSrcRoots
	sess.allowedDstRoots = allowedDstRoots
	// Resolve symlinks in the allowed source roots once. The post-EvalSymlinks
	// comparison below would otherwise fail when a root itself contains symlink
	// components (e.g. macOS /var → /private/var, or LIBRARY_DIR being a
	// Synology shared-folder symlink).
	sess.resolvedSrcRoots = resolveRoots(allowedSrcRoots)
	return sess
}

// apply performs the filesystem operation for a single item, mutating
// item.SourcePath/DestPath in place when the strategy relocates the file (so
// later reads of item reflect where it actually ended up).
func (s *fsOpSession) apply(item *ApplyItem) fsOpResult {
	if s.strategy == "register" || item.SourcePath == "" {
		return fsOpResult{op: "kept"}
	}

	src := filepath.Clean(item.SourcePath)
	if !IsUnderPrefixes(src, s.allowedSrcRoots) {
		return fsOpResult{op: "skipped", err: "path not allowed"}
	}
	// Resolve symlinks and re-validate. A symlink planted under an allowed
	// root (e.g. /downloads/sneaky → /etc) would pass the textual prefix
	// check but redirect the rename/link to anywhere on disk. HandleBrowse
	// already resolves on listing; mirror that here so the apply path
	// can't be tricked by something the browse view would have hidden.
	resolved, err := filepath.EvalSymlinks(src)
	if err != nil {
		return fsOpResult{op: "skipped", err: "source not readable: " + err.Error()}
	}
	if !IsUnderPrefixes(resolved, s.resolvedSrcRoots) {
		return fsOpResult{op: "skipped", err: "path not allowed"}
	}
	src = resolved

	dst := item.DestPath
	if dst == "" {
		if item.Type == "movie" {
			dst = suggestedMoviePath(s.movieRoot, item.Title, item.Year, filepath.Base(src), item.Edition)
		} else {
			dst = suggestedTVPath(s.tvRoot, item.Title, 0, filepath.Base(src))
		}
	}
	dst = filepath.Clean(dst)
	if !IsUnderPrefixes(dst, s.allowedDstRoots) {
		return fsOpResult{op: "skipped", err: "destination path not allowed"}
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		slog.Warn("import: mkdir failed", "component", "library", "dst", dst, "error", err)
		return fsOpResult{op: "failed", err: err.Error()}
	}

	switch s.strategy {
	case "import":
		if err := moveFile(src, dst); err != nil {
			slog.Warn("import: move failed", "component", "library",
				"src", src, "dst", dst, "error", err)
			return fsOpResult{op: "failed", err: err.Error()}
		}
		item.SourcePath = dst
		item.DestPath = dst
		return fsOpResult{op: "moved"}
	case "hardlink":
		_, lstatErr := os.Lstat(dst)
		switch {
		case errors.Is(lstatErr, fs.ErrNotExist):
			if err := os.Link(src, dst); err != nil {
				slog.Warn("import: hardlink failed", "component", "library",
					"src", src, "dst", dst, "error", err)
				return fsOpResult{op: "failed", err: err.Error()}
			}
			item.DestPath = dst
			return fsOpResult{op: "hardlinked"}
		case lstatErr == nil:
			// dst already exists — idempotent success.
			item.DestPath = dst
			return fsOpResult{op: "hardlinked"}
		default:
			// Any other Lstat error (permission denied, I/O error, etc.)
			// must not silently succeed.
			slog.Warn("import: lstat failed before hardlink", "component", "library",
				"dst", dst, "error", lstatErr)
			return fsOpResult{op: "failed", err: lstatErr.Error()}
		}
	case "link":
		_, lstatErr := os.Lstat(dst)
		switch {
		case errors.Is(lstatErr, fs.ErrNotExist):
			if err := os.Symlink(src, dst); err != nil {
				slog.Warn("import: symlink failed", "component", "library",
					"src", src, "dst", dst, "error", err)
				return fsOpResult{op: "failed", err: err.Error()}
			}
			item.DestPath = dst
			return fsOpResult{op: "symlinked"}
		case lstatErr == nil:
			// dst already exists — idempotent success.
			item.DestPath = dst
			return fsOpResult{op: "symlinked"}
		default:
			// Any other Lstat error (permission denied, I/O error, etc.)
			// must not silently succeed.
			slog.Warn("import: lstat failed before symlink", "component", "library",
				"dst", dst, "error", lstatErr)
			return fsOpResult{op: "failed", err: lstatErr.Error()}
		}
	}
	return fsOpResult{op: "kept"}
}

// applyFSOps iterates items and performs the filesystem operation dictated by
// strategy for each item that has a SourcePath, computing every item's
// result up front. This batch form is kept for tests (and any future caller
// that wants the whole set resolved eagerly); HandleLibraryApply itself uses
// fsOpSession.apply directly, one item at a time, interleaved with that
// item's *arr registration (see MWA-6). allowedSrcRoots defaults to the
// production browse roots when nil; allowedDstRoots defaults to empty.
// movieRoot and tvRoot are used to compute suggested destination paths when
// item.DestPath is empty; pass empty strings to use the hardcoded fallbacks.
func applyFSOps(items []ApplyItem, strategy string, allowedSrcRoots, allowedDstRoots []string, movieRoot, tvRoot string) []fsOpResult {
	sess := newFSOpSession(strategy, allowedSrcRoots, allowedDstRoots, movieRoot, tvRoot)
	results := make([]fsOpResult, len(items))
	for i := range items {
		results[i] = sess.apply(&items[i])
	}
	return results
}
