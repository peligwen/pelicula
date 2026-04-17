package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"pelicula-api/httputil"
	"sort"
	"strings"
	"sync"
)

// ── Apply types ───────────────────────────────────────────────────────────────

type ApplyRequest struct {
	Items    []ApplyItem `json:"items"`
	Strategy string      `json:"strategy"` // import / link / register (also accepts legacy: migrate / symlink / hardlink / keep)
	Validate bool        `json:"validate"` // forward to Procula for validation after apply
}

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

type LibraryApplyResult struct {
	Added   int               `json:"added"`
	Skipped int               `json:"skipped"`
	Failed  int               `json:"failed"`
	Errors  []string          `json:"errors,omitempty"`
	Items   []ApplyItemResult `json:"items,omitempty"` // per-item detail for display
}

type ApplyItemResult struct {
	Title string `json:"title"`
	Src   string `json:"src,omitempty"`
	Dest  string `json:"dest,omitempty"`
	FSOp  string `json:"fsOp,omitempty"` // "moved", "symlinked", "kept", "skipped", "failed"
	Error string `json:"error,omitempty"`
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
		httputil.WriteError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// ── Duplicate guard ──────────────────────────────────────────────────────
	// Reject the request if any group key (movie:tmdbId or
	// series:tvdbId:s_e_) appears more than once.  The UI should have
	// resolved all duplicate groups before calling apply; this check
	// prevents stale UIs or direct API callers from clobbering files.
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

	sonarrKey, radarrKey, _ := services.Keys()
	if radarrKey == "" || sonarrKey == "" {
		httputil.WriteError(w, "API keys not loaded — is the stack wired?", http.StatusServiceUnavailable)
		return
	}

	if warns := CheckLibraryAccess(); len(warns) > 0 {
		httputil.WriteError(w, warns[0], http.StatusServiceUnavailable)
		return
	}

	existingMovies := loadExistingMovieIDs(radarrKey)
	existingSeries := loadExistingSeriesIDs(sonarrKey)

	movieProfiles, _ := loadProfileNameMap(radarrURL, radarrKey)
	seriesProfiles, _ := loadProfileNameMap(sonarrURL, sonarrKey)

	// ── Filesystem operations (import / link) ───────────────────────────────
	// Perform FS ops before *arr registration so that if a move fails we don't
	// register a path that doesn't exist yet.  "register" skips this section.
	// fsResults is parallel to req.Items and records the actual per-item outcome.
	fsResults := applyFSOps(req.Items, req.Strategy, nil, nil)

	// Deduplicate *arr registration by (type, id) — when multiple episodes of
	// the same series are imported, applySeries only needs to be called once.
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

	for idx, item := range req.Items {
		fsResult := fsResults[idx]

		// Items skipped by applyFSOps (path not under allowed prefix) are
		// propagated directly to the response without *arr registration.
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
			// FS op already ran; skip *arr registration for this duplicate series episode.
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
				result.Items = append(result.Items, ApplyItemResult{
					Title: it.Title, Src: it.SourcePath, FSOp: "failed", Error: err.Error(),
				})
			} else {
				result.Added++
				addedItems = append(addedItems, it)
				// Use the actual FS op result so callers see "failed"/"skipped"
				// rather than a pre-computed guess when the FS step did not succeed.
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
	if req.Validate && len(addedItems) > 0 {
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
			if err := forwardToProcula(r.Context(), source); err != nil {
				slog.Warn("failed to forward import to Procula",
					"component", "library", "title", item.Title, "error", err)
			}
		}
	}

	httputil.WriteJSON(w, result)
}

// applyGroupKey computes the group key from an ApplyItem. Used in the
// duplicate guard inside handleLibraryApply.
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

func suggestedMoviePath(title string, year int, filename string) string {
	folder := title
	if year > 0 {
		folder = fmt.Sprintf("%s (%d)", title, year)
	}
	root := firstLibraryPath("radarr", "/media/movies")
	return root + "/" + folder + "/" + filepath.Base(filename)
}

func suggestedTVPath(title string, season int, filename string) string {
	root := firstLibraryPath("sonarr", "/media/tv")
	if season > 0 {
		return fmt.Sprintf("%s/%s/Season %02d/%s", root, title, season, filepath.Base(filename))
	}
	return fmt.Sprintf("%s/%s/%s", root, title, filepath.Base(filename))
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
		root = firstLibraryPath("radarr", "/media/movies")
	}

	// Start from the full lookup object so Radarr gets all required fields
	// (images, ratings, etc.), then overlay our config values.
	movie["tmdbId"] = item.TmdbID
	movie["qualityProfileId"] = profileID
	movie["rootFolderPath"] = root
	movie["monitored"] = item.Monitored
	movie["addOptions"] = map[string]any{
		"searchForMovie": false,
	}
	body, err := services.ArrPost(radarrURL, apiKey, "/api/v3/movie", movie)
	if err != nil {
		if len(body) > 0 {
			return fmt.Errorf("%w: %s", err, bytes.TrimSpace(body))
		}
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
		root = firstLibraryPath("sonarr", "/media/tv")
	}

	// Start from the full lookup object so Sonarr gets all required fields,
	// then overlay our config values.
	show["tvdbId"] = item.TvdbID
	show["qualityProfileId"] = profileID
	show["rootFolderPath"] = root
	show["monitored"] = item.Monitored
	show["seasonFolder"] = true
	show["addOptions"] = map[string]any{
		"searchForMissingEpisodes": false,
	}
	body, err := services.ArrPost(sonarrURL, apiKey, "/api/v3/series", show)
	if err != nil {
		if len(body) > 0 {
			return fmt.Errorf("%w: %s", err, bytes.TrimSpace(body))
		}
		return err
	}
	return nil
}
