package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// cliLibrary is a minimal representation of a library entry used by the CLI
// to create directories and generate compose overrides.
// It mirrors the on-disk schema in libraries.json (written by middleware).
type cliLibrary struct {
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	Path    string `json:"path,omitempty"` // host path; empty = LIBRARY_DIR/slug
	BuiltIn bool   `json:"builtin,omitempty"`
}

// cliLibraryConfig is the top-level on-disk schema for libraries.json.
type cliLibraryConfig struct {
	Libraries []cliLibrary `json:"libraries"`
}

// defaultLibraries returns the built-in two-library default config.
func defaultLibraries() cliLibraryConfig {
	return cliLibraryConfig{
		Libraries: []cliLibrary{
			{Name: "Movies", Slug: "movies", BuiltIn: true},
			{Name: "TV Shows", Slug: "tv", BuiltIn: true},
		},
	}
}

// readOrCreateLibraries reads libraries.json from configPeliculaDir.
// If the file does not exist, it writes the default (movies + tv) and returns those.
// If the file exists, it parses and returns the libraries.
func readOrCreateLibraries(configPeliculaDir string) ([]cliLibrary, error) {
	librariesPath := filepath.Join(configPeliculaDir, "libraries.json")

	data, err := os.ReadFile(librariesPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read libraries.json: %w", err)
		}

		// File absent — write default and return built-ins.
		defaults := defaultLibraries()
		out, err := json.MarshalIndent(defaults, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal default libraries.json: %w", err)
		}
		if err := os.MkdirAll(configPeliculaDir, 0755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", configPeliculaDir, err)
		}
		if err := os.WriteFile(librariesPath, append(out, '\n'), 0644); err != nil {
			return nil, fmt.Errorf("write default libraries.json: %w", err)
		}
		return defaults.Libraries, nil
	}

	var cfg cliLibraryConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse libraries.json: %w", err)
	}
	return cfg.Libraries, nil
}

// setupDirs creates the required directory tree for configDir, libraryDir, and workDir.
// For each library in libs, it creates filepath.Join(libraryDir, lib.Slug) unless the
// library has an explicit external path (lib.Path != ""), in which case the user manages
// that directory.
func setupDirs(configDir, libraryDir, workDir string, libs []cliLibrary) error {
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
		filepath.Join(workDir, "downloads"),
		filepath.Join(workDir, "downloads", "incomplete"),
		filepath.Join(workDir, "downloads", "radarr"),
		filepath.Join(workDir, "downloads", "tv-sonarr"),
		filepath.Join(workDir, "processing"),
	}

	// Create a directory under libraryDir for each managed library (no external path).
	for _, lib := range libs {
		if lib.Slug != "" && lib.Path == "" {
			dirs = append(dirs, filepath.Join(libraryDir, lib.Slug))
		}
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return nil
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
