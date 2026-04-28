package main

import (
	"crypto/rand"
	"encoding/base64"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// getScriptDir returns the pelicula project root directory.
// It walks up from the binary's location looking for compose/docker-compose.yml.
// Falls back to the binary's directory if not found.
func getScriptDir() string {
	// Start from the binary's resolved location
	start := ""
	exe, err := os.Executable()
	if err == nil {
		resolved, err := filepath.EvalSymlinks(exe)
		if err == nil {
			start = filepath.Dir(resolved)
		} else {
			start = filepath.Dir(exe)
		}
	}
	if start == "" {
		start, _ = os.Getwd()
	}

	// Walk up looking for compose/docker-compose.yml (the project root marker)
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "compose", "docker-compose.yml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return start
}

// openBrowser opens url in the default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start() // best-effort, ignore errors
}

// generateAPIKey generates a 32-character alphanumeric random key.
func generateAPIKey() string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 32)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			// Fall back to base64 if crypto/rand fails
			fb := make([]byte, 24)
			_, _ = rand.Read(fb)
			return base64.RawURLEncoding.EncodeToString(fb)[:32]
		}
		b[i] = chars[n.Int64()]
	}
	return string(b)
}

// capitalize uppercases the first byte of s if it is a lowercase ASCII letter.
// Returns s unchanged if s is empty or the first byte is not a–z.
func capitalize(s string) string {
	if len(s) == 0 {
		return s
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-32) + s[1:]
	}
	return s
}

// requireEnv prints an error and exits if the .env file does not exist.
func requireEnv(envFile string) {
	if _, err := os.Stat(envFile); err != nil {
		fatal("No .env file found. Run " + bold("pelicula up") + " first.")
	}
}

// loadEnvOrFatal loads the .env file or exits on failure.
func loadEnvOrFatal(envFile string) EnvMap {
	requireEnv(envFile)
	env, err := ParseEnv(envFile)
	if err != nil {
		fatal("Failed to read .env: " + err.Error())
	}
	return env
}

// peliculaBaseURL returns the base URL for the pelicula-api on localhost using
// the PELICULA_PORT from env, defaulting to 7354 if the key is absent or empty.
// Callers append the path, e.g. peliculaBaseURL(env) + "/api/pelicula/health".
func peliculaBaseURL(env EnvMap) string {
	port := envDefault(env, "PELICULA_PORT", "7354")
	return "http://localhost:" + port
}

// checkAuthError exits with an actionable message when resp indicates
// authentication is required (HTTP 401 or 403).
func checkAuthError(resp *http.Response) {
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		fail("Authentication required — run: pelicula up")
		os.Exit(1)
	}
}
