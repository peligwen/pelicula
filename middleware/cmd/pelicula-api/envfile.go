package main

// envfile.go — package-level wrappers for .env file I/O, delegating to
// internal/app/settings. These exist so that cmd/ files that have not yet
// been migrated (search.go, main.go, jfWirer injection) can call
// parseEnvFile/writeEnvFile without importing the settings package directly.

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
