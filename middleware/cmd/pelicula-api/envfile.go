package main

// envfile.go — transitional shim: thin wrappers so cmd-level callers
// (search.go, main.go, jfapp.NewWirer) can use settings.ParseEnvFile /
// WriteEnvFile under their existing identifier names until those call sites
// are migrated or extracted. Remove this file when all callers are gone.

import "pelicula-api/internal/app/settings"

// parseEnvFile reads a .env file and returns a key→value map.
// Delegates to settings.ParseEnvFile.
func parseEnvFile(path string) (map[string]string, error) {
	return settings.ParseEnvFile(path)
}

// writeEnvFile writes a .env file from the provided key-value map.
// Delegates to settings.WriteEnvFile.
func writeEnvFile(path string, vars map[string]string) error {
	return settings.WriteEnvFile(path, vars)
}
