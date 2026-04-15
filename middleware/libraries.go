package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"pelicula-api/httputil"
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

// slugRe is the allowed pattern for library slugs: lowercase alphanumeric,
// hyphens allowed but not as the first character.
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// validLibraryTypes is the set of accepted Library.Type values.
var validLibraryTypes = map[string]bool{
	"movies":  true,
	"tvshows": true,
	"mixed":   true,
	"other":   true,
}

// validLibraryArrs is the set of accepted Library.Arr values.
var validLibraryArrs = map[string]bool{
	"radarr": true,
	"sonarr": true,
	"none":   true,
}

// validateLibrary returns an error if lib contains invalid field values.
func validateLibrary(lib Library) error {
	if lib.Slug == "" {
		return fmt.Errorf("library slug must not be empty")
	}
	if !slugRe.MatchString(lib.Slug) {
		return fmt.Errorf("library slug %q is invalid: must match ^[a-z0-9][a-z0-9-]*$", lib.Slug)
	}
	if !validLibraryTypes[lib.Type] {
		return fmt.Errorf("library type %q is invalid: must be one of movies, tvshows, mixed, other", lib.Type)
	}
	if !validLibraryArrs[lib.Arr] {
		return fmt.Errorf("library arr %q is invalid: must be one of radarr, sonarr, none", lib.Arr)
	}
	return nil
}

// loadLibraries reads libraries.json from configPeliculaDir. If the file does
// not exist it writes the default config and returns it. Any other I/O error is
// returned to the caller.
func loadLibraries(configPeliculaDir string) (LibraryConfig, error) {
	path := filepath.Join(configPeliculaDir, "libraries.json")

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

// writeLibraries atomically writes cfg to configPeliculaDir/libraries.json by
// writing to a temp file in the same directory then renaming it into place.
func writeLibraries(configPeliculaDir string, cfg LibraryConfig) error {
	dest := filepath.Join(configPeliculaDir, "libraries.json")

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("libraries: marshal: %w", err)
	}

	tmp, err := os.CreateTemp(configPeliculaDir, "libraries-*.json.tmp")
	if err != nil {
		return fmt.Errorf("libraries: create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("libraries: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("libraries: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("libraries: rename %s → %s: %w", tmpName, dest, err)
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

// SaveLibrary validates, then creates or replaces a library by slug and
// persists the registry. configPeliculaDir must be the path to the pelicula
// config directory (e.g. /config/pelicula inside the container).
func SaveLibrary(configPeliculaDir string, lib Library) error {
	if err := validateLibrary(lib); err != nil {
		return err
	}

	libraryRegistryMu.Lock()
	defer libraryRegistryMu.Unlock()

	// Build the new slice without touching the global yet.
	existing := libraryRegistry.Libraries
	newLibs := make([]Library, len(existing))
	copy(newLibs, existing)

	found := false
	for i, e := range newLibs {
		if e.Slug == lib.Slug {
			newLibs[i] = lib
			found = true
			break
		}
	}
	if !found {
		newLibs = append(newLibs, lib)
	}

	newCfg := LibraryConfig{Libraries: newLibs}
	if err := writeLibraries(configPeliculaDir, newCfg); err != nil {
		return err
	}
	// Write succeeded — update global.
	libraryRegistry = newCfg
	return nil
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

// peliculaConfigDir is the runtime path to the pelicula config directory
// inside the container, used by the library HTTP handlers.
const peliculaConfigDir = "/config/pelicula"

// handleListLibraries handles GET /api/pelicula/libraries.
// No auth required — same convention as other read-only dashboard endpoints.
func handleListLibraries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	httputil.WriteJSON(w, GetLibraries())
}

// handleAddLibrary handles POST /api/pelicula/libraries.
// Admin auth required. Decodes the request body, validates, saves, and (when
// no external Path is provided) creates the media directory on disk.
func handleAddLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var lib Library
	if err := json.NewDecoder(r.Body).Decode(&lib); err != nil {
		httputil.WriteError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Prevent callers from injecting the built-in flag.
	lib.BuiltIn = false

	if err := SaveLibrary(peliculaConfigDir, lib); err != nil {
		httputil.WriteError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// If no external path is configured, create the media directory so it is
	// ready to receive files when the volume mount is wired up (Task 5).
	if lib.Path == "" {
		if err := os.MkdirAll("/media/"+lib.Slug, 0755); err != nil {
			// Non-fatal: log and continue — the directory may already exist or
			// the volume may not be mounted in this environment.
			slog.Warn("libraries: create media dir", "component", "libraries", "slug", lib.Slug, "error", err)
		}
	}

	w.WriteHeader(http.StatusCreated)
	httputil.WriteJSON(w, lib)
}

// handleUpdateLibrary handles PUT /api/pelicula/libraries/{slug}.
// Admin auth required. Merges the request body fields onto the existing
// library (preserving BuiltIn), then saves.
func handleUpdateLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slug := r.PathValue("slug")
	if slug == "" {
		httputil.WriteError(w, "slug required", http.StatusBadRequest)
		return
	}

	existing, err := GetLibraryBySlug(slug)
	if err != nil {
		httputil.WriteError(w, "library not found", http.StatusNotFound)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var updates Library
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		httputil.WriteError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Merge: apply non-zero update fields onto the existing library.
	// BuiltIn and Slug are immutable.
	merged := existing
	if updates.Name != "" {
		merged.Name = updates.Name
	}
	if updates.Type != "" {
		merged.Type = updates.Type
	}
	if updates.Arr != "" {
		merged.Arr = updates.Arr
	}
	if updates.Processing != "" {
		merged.Processing = updates.Processing
	}
	// Path may be cleared (set to "") to switch from external to managed;
	// always apply it from the update.
	merged.Path = updates.Path

	if err := SaveLibrary(peliculaConfigDir, merged); err != nil {
		httputil.WriteError(w, err.Error(), http.StatusBadRequest)
		return
	}
	httputil.WriteJSON(w, merged)
}

// handleDeleteLibrary handles DELETE /api/pelicula/libraries/{slug}.
// Admin auth required. Rejects built-in libraries.
func handleDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slug := r.PathValue("slug")
	if slug == "" {
		httputil.WriteError(w, "slug required", http.StatusBadRequest)
		return
	}

	if err := DeleteLibrary(peliculaConfigDir, slug); err != nil {
		// Distinguish not-found from built-in-rejection for proper status codes.
		if _, lookupErr := GetLibraryBySlug(slug); lookupErr != nil {
			httputil.WriteError(w, "library not found", http.StatusNotFound)
		} else {
			httputil.WriteError(w, err.Error(), http.StatusConflict)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteLibrary removes the library with the given slug and persists the
// registry. Returns an error if the slug is not found or the library is
// built-in.
func DeleteLibrary(configPeliculaDir string, slug string) error {
	libraryRegistryMu.Lock()
	defer libraryRegistryMu.Unlock()

	existing := libraryRegistry.Libraries
	idx := -1
	for i, lib := range existing {
		if lib.Slug == slug {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("library %q not found", slug)
	}
	if existing[idx].BuiltIn {
		return fmt.Errorf("library %q is built-in and cannot be deleted", slug)
	}

	// Build the new slice without touching the global yet.
	newLibs := make([]Library, 0, len(existing)-1)
	newLibs = append(newLibs, existing[:idx]...)
	newLibs = append(newLibs, existing[idx+1:]...)

	newCfg := LibraryConfig{Libraries: newLibs}
	if err := writeLibraries(configPeliculaDir, newCfg); err != nil {
		return err
	}
	// Write succeeded — update global.
	libraryRegistry = newCfg
	return nil
}
