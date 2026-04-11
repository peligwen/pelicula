package main

import (
	"crypto/rand"
	"encoding/base64"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// sudoRun creates an exec.Cmd prefixed with "sudo".
func sudoRun(args ...string) *exec.Cmd {
	return exec.Command("sudo", args...)
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

// expandHome replaces a leading ~ with the user's home directory.
func expandHome(path, home string) string {
	if strings.HasPrefix(path, "~/") {
		return home + path[1:]
	}
	if path == "~" {
		return home
	}
	return path
}
