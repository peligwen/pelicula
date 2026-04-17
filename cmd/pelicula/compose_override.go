package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

//go:embed templates/libraries.yml.tmpl
var librariesTmpl string

// generateLibrariesOverride reads libraries.json from configPeliculaDir, and if
// any libraries have an external host path set, writes a docker-compose.libraries.yml
// override at outputPath that mounts each one into every relevant service.
// If no external libraries exist the override file is removed (if present).
func generateLibrariesOverride(configPeliculaDir, outputPath string) error {
	librariesFile := filepath.Join(configPeliculaDir, "libraries.json")

	data, err := os.ReadFile(librariesFile)
	if err != nil {
		if os.IsNotExist(err) {
			// No libraries file yet — nothing to do.
			return removeIfExists(outputPath)
		}
		return fmt.Errorf("read libraries.json: %w", err)
	}

	var cfg cliLibraryConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse libraries.json: %w", err)
	}

	// Collect only external libraries (those with an explicit host path).
	var external []cliLibrary
	for _, lib := range cfg.Libraries {
		if lib.Path != "" && lib.Slug != "" {
			if !safeSlugRe.MatchString(lib.Slug) {
				warn(fmt.Sprintf("skipping library with unsafe slug %q (must match [a-z0-9][a-z0-9-]*)", lib.Slug))
				continue
			}
			external = append(external, lib)
		}
	}

	if len(external) == 0 {
		return removeIfExists(outputPath)
	}

	// librariesOverrideData holds the values interpolated into libraries.yml.tmpl.
	type librariesOverrideData struct {
		Services  []string
		Libraries []cliLibrary
	}

	tmplData := librariesOverrideData{
		Services:  []string{"jellyfin", "sonarr", "radarr", "procula", "pelicula-api", "bazarr"},
		Libraries: external,
	}

	tmpl, err := template.New("libraries.yml").Parse(librariesTmpl)
	if err != nil {
		return fmt.Errorf("parse libraries.yml.tmpl: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, tmplData); err != nil {
		return fmt.Errorf("render libraries.yml.tmpl: %w", err)
	}

	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("write libraries override: %w", err)
	}
	return nil
}

// removeIfExists removes path if it exists, ignoring not-found errors.
func removeIfExists(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
