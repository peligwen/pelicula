package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// ── Library registry ──────────────────────────────────────────────────────────

// Library describes a media library root managed by Pelicula.
type Library struct {
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	Path       string `json:"path,omitempty"` // host path; empty = LIBRARY_DIR/slug
	Type       string `json:"type"`           // movies | tvshows | mixed | other
	Arr        string `json:"arr"`            // radarr | sonarr | none
	Processing string `json:"processing"`     // audit | full | off
	BuiltIn    bool   `json:"builtin,omitempty"`
}

// ContainerPath returns the path at which this library's media is mounted
// inside the middleware container (always /media/<slug>).
func (l Library) ContainerPath() string {
	return "/media/" + l.Slug
}

// LibraryConfig is the on-disk schema for libraries.json.
type LibraryConfig struct {
	Libraries []Library `json:"libraries"`
}

// defaultLibraryConfig returns the built-in two-library config used when
// libraries.json is absent.
func defaultLibraryConfig() LibraryConfig {
	return LibraryConfig{
		Libraries: []Library{
			{Name: "Movies", Slug: "movies", Type: "movies", Arr: "radarr", Processing: "full", BuiltIn: true},
			{Name: "TV Shows", Slug: "tv", Type: "tvshows", Arr: "sonarr", Processing: "full", BuiltIn: true},
		},
	}
}

// libraryRegistryMu serialises reads and writes to libraries.json.
var libraryRegistryMu sync.RWMutex

// libraryRegistry is the in-memory library registry, loaded at startup.
var libraryRegistry LibraryConfig

// loadLibraries reads libraries.json from configPeliculaDir. If the file does
// not exist it writes the default config and returns it. Any other I/O error is
// returned to the caller.
func loadLibraries(configPeliculaDir string) (LibraryConfig, error) {
	path := configPeliculaDir + "/libraries.json"

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		cfg := defaultLibraryConfig()
		if writeErr := writeLibraries(configPeliculaDir, cfg); writeErr != nil {
			// Log but don't fail — in-memory default is still usable.
			return cfg, fmt.Errorf("libraries: create default config: %w", writeErr)
		}
		return cfg, nil
	}
	if err != nil {
		return LibraryConfig{}, fmt.Errorf("libraries: read %s: %w", path, err)
	}

	var cfg LibraryConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return LibraryConfig{}, fmt.Errorf("libraries: parse %s: %w", path, err)
	}
	return cfg, nil
}

// writeLibraries atomically writes cfg to configPeliculaDir/libraries.json.
func writeLibraries(configPeliculaDir string, cfg LibraryConfig) error {
	path := configPeliculaDir + "/libraries.json"

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("libraries: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("libraries: write %s: %w", path, err)
	}
	return nil
}

// GetLibraries returns a snapshot of the current library registry.
func GetLibraries() []Library {
	libraryRegistryMu.RLock()
	defer libraryRegistryMu.RUnlock()
	out := make([]Library, len(libraryRegistry.Libraries))
	copy(out, libraryRegistry.Libraries)
	return out
}

// GetLibraryBySlug returns the Library with the given slug, or an error if not
// found.
func GetLibraryBySlug(slug string) (Library, error) {
	libraryRegistryMu.RLock()
	defer libraryRegistryMu.RUnlock()
	for _, lib := range libraryRegistry.Libraries {
		if lib.Slug == slug {
			return lib, nil
		}
	}
	return Library{}, fmt.Errorf("library %q not found", slug)
}

// SaveLibrary creates or replaces a library by slug and persists the registry.
// configPeliculaDir must be the path to the pelicula config directory (e.g.
// /config/pelicula inside the container).
func SaveLibrary(configPeliculaDir string, lib Library) error {
	libraryRegistryMu.Lock()
	defer libraryRegistryMu.Unlock()

	libs := libraryRegistry.Libraries
	for i, existing := range libs {
		if existing.Slug == lib.Slug {
			libs[i] = lib
			libraryRegistry.Libraries = libs
			return writeLibraries(configPeliculaDir, libraryRegistry)
		}
	}
	// Not found — append.
	libraryRegistry.Libraries = append(libs, lib)
	return writeLibraries(configPeliculaDir, libraryRegistry)
}

// DeleteLibrary removes the library with the given slug and persists the
// registry. Returns an error if the slug is not found or the library is
// built-in.
func DeleteLibrary(configPeliculaDir string, slug string) error {
	libraryRegistryMu.Lock()
	defer libraryRegistryMu.Unlock()

	libs := libraryRegistry.Libraries
	idx := -1
	for i, lib := range libs {
		if lib.Slug == slug {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("library %q not found", slug)
	}
	if libs[idx].BuiltIn {
		return fmt.Errorf("library %q is built-in and cannot be deleted", slug)
	}

	libraryRegistry.Libraries = append(libs[:idx], libs[idx+1:]...)
	return writeLibraries(configPeliculaDir, libraryRegistry)
}
