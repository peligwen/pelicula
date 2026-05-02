package procula

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// runLibraryRefresh refreshes the library cache every 5 minutes until ctx is
// cancelled. It must be launched after loadLibraries returns successfully.
func runLibraryRefresh(ctx context.Context, peliculaAPI string) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshLibraries(peliculaAPI)
		}
	}
}

// ProculaLibrary is a minimal view of a pelicula-api Library used inside procula.
// Field names match the JSON returned by GET /api/pelicula/libraries.
type ProculaLibrary struct {
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	Type       string `json:"type"`       // movies | tvshows | mixed | other
	Arr        string `json:"arr"`        // radarr | sonarr | none
	Processing string `json:"processing"` // full | audit | off
}

// ContainerPath returns the mount path inside the procula container (/media/<slug>).
func (l ProculaLibrary) ContainerPath() string {
	return "/media/" + l.Slug
}

var (
	libraryMu    sync.RWMutex
	cachedLibs   []ProculaLibrary
	libraryReady bool
)

// defaultLibraries returns the two built-in libraries used as a fallback when
// the pelicula-api is unreachable at startup.
// SYNC: keep slug/name/type/arr/processing in sync with defaultLibraries() in cmd/pelicula/dirs.go.
func defaultLibraries() []ProculaLibrary {
	return []ProculaLibrary{
		{Name: "Movies", Slug: "movies", Type: "movies", Arr: "radarr", Processing: "full"},
		{Name: "TV Shows", Slug: "tv", Type: "tvshows", Arr: "sonarr", Processing: "full"},
	}
}

const (
	libraryMaxAttempts   = 10
	libraryRetryDelay    = 3 * time.Second
	libraryTotalDeadline = 60 * time.Second
)

// loadLibraries fetches the library list from pelicula-api and caches it.
// Falls back to the two built-in defaults if the API is unreachable after all
// retries. Retries up to libraryMaxAttempts times with a libraryRetryDelay
// linear delay, bounded by a 60-second total deadline.
// Should be called once at startup after the API is expected to be reachable.
func loadLibraries(peliculaAPI string) {
	ctx, cancel := context.WithTimeout(context.Background(), libraryTotalDeadline)
	defer cancel()

	libs, ok := fetchLibrariesWithRetry(ctx, peliculaAPI)
	if !ok {
		slog.Warn("could not fetch libraries from pelicula-api after all retries, using defaults",
			"component", "libraries", "max_attempts", libraryMaxAttempts)
		setLibraries(defaultLibraries())
		return
	}

	slog.Info("loaded libraries from pelicula-api", "component", "libraries", "count", len(libs))
	setLibraries(libs)
}

// fetchLibrariesWithRetry attempts to fetch the library list up to
// libraryMaxAttempts times. Returns the list and true on success, or nil and
// false after all retries (including context deadline).
func fetchLibrariesWithRetry(ctx context.Context, peliculaAPI string) ([]ProculaLibrary, bool) {
	client := newProculaClient(5 * time.Second)
	apiURL := peliculaAPI + "/api/pelicula/libraries"

	for attempt := 1; attempt <= libraryMaxAttempts; attempt++ {
		slog.Debug("fetching libraries from pelicula-api",
			"component", "libraries", "attempt", attempt)

		libs, err := fetchLibrariesOnce(client, apiURL)
		if err == nil && libs != nil {
			return libs, true
		}
		if err != nil {
			slog.Debug("library fetch attempt failed",
				"component", "libraries", "attempt", attempt, "error", err)
		}

		if attempt == libraryMaxAttempts {
			break
		}

		// Wait for the retry delay or context cancellation.
		select {
		case <-ctx.Done():
			slog.Debug("library fetch context cancelled", "component", "libraries")
			return nil, false
		case <-time.After(libraryRetryDelay):
		}
	}
	return nil, false
}

// fetchLibrariesOnce makes a single HTTP request for the library list.
// Returns (nil, nil) on a parseable-but-empty response (treated as retriable).
func fetchLibrariesOnce(client *http.Client, apiURL string) ([]ProculaLibrary, error) {
	resp, err := client.Get(apiURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var libs []ProculaLibrary
	if err := json.NewDecoder(resp.Body).Decode(&libs); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	if len(libs) == 0 {
		return nil, fmt.Errorf("empty library list")
	}
	return libs, nil
}

// refreshLibraries fetches the library list from pelicula-api and updates the
// cache on success. On failure or empty response it logs a warning and keeps
// the existing cache unchanged (never falls back to defaults during a refresh).
func refreshLibraries(peliculaAPI string) {
	client := newProculaClient(5 * time.Second)
	url := peliculaAPI + "/api/pelicula/libraries"
	resp, err := client.Get(url)
	if err != nil {
		slog.Warn("library refresh: could not reach pelicula-api, keeping existing cache", "component", "libraries", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("library refresh: non-200 response, keeping existing cache", "component", "libraries", "status", resp.StatusCode)
		return
	}

	var libs []ProculaLibrary
	if err := json.NewDecoder(resp.Body).Decode(&libs); err != nil {
		slog.Warn("library refresh: could not decode response, keeping existing cache", "component", "libraries", "error", err)
		return
	}

	if len(libs) == 0 {
		slog.Warn("library refresh: empty library list, keeping existing cache", "component", "libraries")
		return
	}

	slog.Info("library cache refreshed", "component", "libraries", "count", len(libs))
	setLibraries(libs)
}

func setLibraries(libs []ProculaLibrary) {
	libraryMu.Lock()
	defer libraryMu.Unlock()
	cachedLibs = libs
	libraryReady = true
}

// getProculaLibraries returns the cached library list.
// If not yet loaded, returns the built-in defaults.
func getProculaLibraries() []ProculaLibrary {
	libraryMu.RLock()
	defer libraryMu.RUnlock()
	if !libraryReady {
		return defaultLibraries()
	}
	cp := make([]ProculaLibrary, len(cachedLibs))
	copy(cp, cachedLibs)
	return cp
}

// libraryForPath returns the ProculaLibrary whose ContainerPath is a prefix of
// the given path, or the zero value and false if no library matches.
func libraryForPath(path string) (ProculaLibrary, bool) {
	for _, lib := range getProculaLibraries() {
		prefix := lib.ContainerPath()
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return lib, true
		}
	}
	return ProculaLibrary{}, false
}

// arrTypeFromPath returns "sonarr" or "radarr" by looking up the library
// registry. Falls back to "radarr" if no library matches.
func arrTypeFromPath(p string) string {
	if lib, ok := libraryForPath(p); ok {
		if lib.Arr != "" && lib.Arr != "none" {
			return lib.Arr
		}
	}
	return "radarr"
}

// mediaTypeFromPath returns "movie", "episode", or "other" by consulting the
// library registry's type field.
func mediaTypeFromPath(p string) string {
	if lib, ok := libraryForPath(p); ok {
		switch lib.Type {
		case "movies":
			return "movie"
		case "tvshows":
			return "episode"
		default:
			return "other"
		}
	}
	return "movie"
}

// isLibrarySlug reports whether slug matches any configured library's slug.
// Used to distinguish a library directory name from a title-bearing parent dir.
func isLibrarySlug(slug string) bool {
	for _, lib := range getProculaLibraries() {
		if lib.Slug == slug {
			return true
		}
	}
	return false
}

// processingModeForPath returns the processing mode ("full", "audit", "off")
// for the library that owns the given path. Falls back to "full".
func processingModeForPath(path string) string {
	if lib, ok := libraryForPath(path); ok && lib.Processing != "" {
		return lib.Processing
	}
	return "full"
}
