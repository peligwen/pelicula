package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// setupDirs creates the required directory tree for configDir, libraryDir, and workDir.
func setupDirs(configDir, libraryDir, workDir string) error {
	dirs := []string{
		filepath.Join(configDir, "gluetun"),
		filepath.Join(configDir, "qbittorrent"),
		filepath.Join(configDir, "prowlarr"),
		filepath.Join(configDir, "sonarr"),
		filepath.Join(configDir, "radarr"),
		filepath.Join(configDir, "jellyfin"),
		filepath.Join(configDir, "bazarr"),
		filepath.Join(configDir, "procula", "jobs"),
		filepath.Join(configDir, "procula", "profiles"),
		filepath.Join(configDir, "pelicula"),
		filepath.Join(configDir, "certs"),
		filepath.Join(libraryDir, "movies"),
		filepath.Join(libraryDir, "tv"),
		filepath.Join(workDir, "downloads"),
		filepath.Join(workDir, "downloads", "incomplete"),
		filepath.Join(workDir, "downloads", "radarr"),
		filepath.Join(workDir, "downloads", "tv-sonarr"),
		filepath.Join(workDir, "processing"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return &dirCreateError{path: d, err: err}
		}
	}
	return nil
}

// dirCreateError wraps a directory creation failure with actionable guidance.
type dirCreateError struct {
	path string
	err  error
}

func (e *dirCreateError) Error() string {
	return fmt.Sprintf("mkdir %s: %s", e.path, e.err)
}

func (e *dirCreateError) Unwrap() error { return e.err }

// firstExistingAncestor walks up from path and returns the deepest ancestor
// that already exists on the filesystem, or "" if none found.
func firstExistingAncestor(path string) string {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			break // reached filesystem root
		}
	}
	return ""
}

// writeEnvFile writes a fresh .env file with the given parameters.
func writeEnvFile(envPath, configDir, libraryDir, workDir, puid, pgid, tz,
	wgKey, countries, port, adminUser, proculaKey, jfPass string) error {

	// Back up if exists
	if _, err := os.Stat(envPath); err == nil {
		bak := fmt.Sprintf("%s.bak.%d", envPath, time.Now().Unix())
		_ = copyFile(envPath, bak)
	}

	m := EnvMap{
		"CONFIG_DIR":            configDir,
		"LIBRARY_DIR":           libraryDir,
		"WORK_DIR":              workDir,
		"PUID":                  puid,
		"PGID":                  pgid,
		"TZ":                    tz,
		"WIREGUARD_PRIVATE_KEY": wgKey,
		"SERVER_COUNTRIES":      countries,
		"GLUETUN_HTTP_USER":     "pelicula",
		"GLUETUN_HTTP_PASS":     generateAPIKey(),
		"PELICULA_PORT":         port,
		"JELLYFIN_ADMIN_USER":   adminUser,
		"JELLYFIN_PASSWORD":     jfPass,
		"PROCULA_API_KEY":       proculaKey,
		"TRANSCODING_ENABLED":   "false",
		"NOTIFICATIONS_ENABLED": "false",
		"NOTIFICATIONS_MODE":    "internal",
	}
	return WriteEnv(envPath, m)
}
