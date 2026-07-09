package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"text/template"
)

//go:embed templates/libraries.yml.tmpl
var librariesTmpl string

// unsafeLibraryPathRe matches characters that must not appear in a library's
// external host Path, because libraries.yml.tmpl interpolates it verbatim
// into a double-quoted YAML flow scalar (`"{{.Path}}:/media/{{.Slug}}"`) via
// Go's text/template, which — unlike html/template — performs no escaping.
// Inside a YAML double-quoted scalar:
//   - `"` ends the scalar early, corrupting every line after it in the
//     generated file (docker compose then fails the *entire* stack with a
//     cryptic YAML-parse error, not just the offending library).
//   - `\` introduces an escape sequence (\n, \t, \U, ...). An embedded
//     backslash either hard-fails the parse (malformed escape — most
//     Windows-style paths like `C:\Users\...` produce exactly this) or,
//     worse, silently reinterprets part of the path (e.g. a literal `\n`
//     becomes an actual newline), corrupting the mounted host path with no
//     error at all. Real bind-mount paths for Compose should use forward
//     slashes on every platform (including Windows/WSL2), so rejecting `\`
//     costs nothing legitimate.
//   - raw control characters (newlines, tabs, etc., 0x00-0x1F and 0x7F) are
//     either invalid in a double-quoted scalar or get folded/normalized,
//     silently changing the value.
//
// Spaces, colons, and other ordinary path punctuation are safe inside a
// double-quoted scalar and are intentionally left unrestricted.
var unsafeLibraryPathRe = regexp.MustCompile(`["\\\x00-\x1f\x7f]`)

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
			if err := validateLibraryPath(lib.Path); err != nil {
				warn(fmt.Sprintf("skipping library %q: %v", lib.Slug, err))
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

// validateLibraryPath rejects library host paths containing characters that
// would corrupt the double-quoted YAML scalar libraries.yml.tmpl embeds them
// in (see unsafeLibraryPathRe). Returns a nil error for safe paths, or an
// actionable error identifying the offending character otherwise.
func validateLibraryPath(path string) error {
	if loc := unsafeLibraryPathRe.FindStringIndex(path); loc != nil {
		return fmt.Errorf("path %q contains an unsafe character %q at position %d (quotes, backslashes, and control characters are not allowed)", path, path[loc[0]:loc[1]], loc[0])
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
