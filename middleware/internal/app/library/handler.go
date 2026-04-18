// Peligrosa: trust boundary layer (HandleBrowse).
// The folder browser resolves symlinks and re-checks the resolved path against
// browse roots before listing — prevents path-traversal escape via symlinks.
// Library scan and browse are admin-only and do not touch untrusted user input;
// they are not part of the Peligrosa surface.
// See ../docs/PELIGROSA.md.
package library

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pelicula-api/httputil"
	proculaclient "pelicula-api/internal/clients/procula"
)

// ── Handler ───────────────────────────────────────────────────────────────────

// ArrClient is the subset of ServiceClients that the library package needs.
type ArrClient interface {
	Keys() (sonarr, radarr, prowlarr string)
	ArrGet(baseURL, apiKey, path string) ([]byte, error)
	ArrPost(baseURL, apiKey, path string, payload any) ([]byte, error)
}

// ForwardToProculaFunc is a callback that creates a Procula pipeline job.
// Matches the signature of the forwardToProcula shim in cmd/.
type ForwardToProculaFunc func(source ProculaJobSource) error

// ProculaJobSource is the payload sent to Procula when forwarding an imported
// item. Mirrors catalog.ProculaJobSource; re-declared here to avoid an import
// cycle with internal/app/catalog.
type ProculaJobSource struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	Year    int    `json:"year,omitempty"`
	Path    string `json:"path"`
	ArrType string `json:"arr_type,omitempty"`
	ArrID   int    `json:"arr_id,omitempty"`
}

// Handler holds injected dependencies for all library HTTP handlers.
type Handler struct {
	Svc           ArrClient
	Procula       *proculaclient.Client
	RadarrURL     string
	SonarrURL     string
	ConfigDir     string // e.g. /config/pelicula
	ForwardToProc ForwardToProculaFunc

	registryMu sync.RWMutex
	registry   LibraryConfig
}

// ── Browse types ──────────────────────────────────────────────────────────────

// BrowseEntry is a single directory listing entry.
type BrowseEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	IsDir   bool      `json:"isDir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

// BrowseResponse is the response shape for the browser endpoint.
type BrowseResponse struct {
	Entries   []BrowseEntry `json:"entries"`
	Truncated bool          `json:"truncated"`
}

// ── Browse handler ────────────────────────────────────────────────────────────

// HandleBrowse returns a directory listing for the server-side folder browser.
// When called without a path, returns the allowed root directories.
func (h *Handler) HandleBrowse(w http.ResponseWriter, r *http.Request) {
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
