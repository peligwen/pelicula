package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// configSubdirs are the per-service directories under CONFIG_DIR. `up`
// creates them; `reset-config` deletes them.
var configSubdirs = []string{"sonarr", "radarr", "prowlarr", "qbittorrent", "jellyfin", "pelicula", "gluetun"}

// setupDirs creates the directory tree the stack expects:
//
//	CONFIG_DIR/{sonarr,radarr,prowlarr,qbittorrent,jellyfin,pelicula,gluetun}
//	LIBRARY_DIR/{movies,tv}
//	WORK_DIR/downloads/{radarr,tv-sonarr}
func setupDirs(configDir, libraryDir, workDir string) error {
	var dirs []string
	for _, s := range configSubdirs {
		dirs = append(dirs, filepath.Join(configDir, s))
	}
	dirs = append(dirs,
		filepath.Join(libraryDir, "movies"),
		filepath.Join(libraryDir, "tv"),
		filepath.Join(workDir, "downloads", "radarr"),
		filepath.Join(workDir, "downloads", "tv-sonarr"),
	)
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return &dirCreateError{path: d, err: err}
		}
	}
	return nil
}

// dirCreateError wraps a directory creation failure with its path.
type dirCreateError struct {
	path string
	err  error
}

func (e *dirCreateError) Error() string { return fmt.Sprintf("mkdir %s: %s", e.path, e.err) }
func (e *dirCreateError) Unwrap() error { return e.err }

// firstExistingAncestor returns the deepest existing ancestor of path, or ""
// when none exists. Used to explain permission errors.
func firstExistingAncestor(path string) string {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if fileExists(p) {
			return p
		}
		if filepath.Dir(p) == p {
			return ""
		}
	}
}
