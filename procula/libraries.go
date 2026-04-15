package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

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
func defaultLibraries() []ProculaLibrary {
	return []ProculaLibrary{
		{Name: "Movies", Slug: "movies", Type: "movies", Arr: "radarr", Processing: "full"},
		{Name: "TV Shows", Slug: "tv", Type: "tvshows", Arr: "sonarr", Processing: "full"},
	}
}

// loadLibraries fetches the library list from pelicula-api and caches it.
// Falls back to the two built-in defaults if the API is unreachable.
// Should be called once at startup after the API is expected to be reachable.
func loadLibraries(peliculaAPI string) {
	client := &http.Client{Timeout: 5 * time.Second}
	url := peliculaAPI + "/api/pelicula/libraries"
	resp, err := client.Get(url)
	if err != nil {
		slog.Warn("could not fetch libraries from pelicula-api, using defaults", "component", "libraries", "error", err)
		setLibraries(defaultLibraries())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("pelicula-api returned non-200 for libraries, using defaults", "component", "libraries", "status", resp.StatusCode)
		setLibraries(defaultLibraries())
		return
	}

	var libs []ProculaLibrary
	if err := json.NewDecoder(resp.Body).Decode(&libs); err != nil {
		slog.Warn("could not decode libraries response, using defaults", "component", "libraries", "error", err)
		setLibraries(defaultLibraries())
		return
	}

	if len(libs) == 0 {
		slog.Warn("pelicula-api returned empty library list, using defaults", "component", "libraries")
		setLibraries(defaultLibraries())
		return
	}

	slog.Info("loaded libraries from pelicula-api", "component", "libraries", "count", len(libs))
	setLibraries(libs)

	// Start a background goroutine to refresh the library cache every 5 minutes
	// so that library changes made via the dashboard are picked up without a restart.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			refreshLibraries(peliculaAPI)
		}
	}()
}

// refreshLibraries fetches the library list from pelicula-api and updates the
// cache on success. On failure or empty response it logs a warning and keeps
// the existing cache unchanged (never falls back to defaults during a refresh).
func refreshLibraries(peliculaAPI string) {
	client := &http.Client{Timeout: 5 * time.Second}
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

// processingModeForPath returns the processing mode ("full", "audit", "off")
// for the library that owns the given path. Falls back to "full".
func processingModeForPath(path string) string {
	if lib, ok := libraryForPath(path); ok && lib.Processing != "" {
		return lib.Processing
	}
	return "full"
}
