package main

import (
	"crypto/rand"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// composeMarker is the file whose presence identifies the repository root.
var composeMarker = filepath.Join("compose", "docker-compose.yml")

// walkUpForMarker walks up from start and returns the first directory that
// contains marker (a relative path), or start unchanged if none does.
func walkUpForMarker(start, marker string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

// getScriptDir returns the repository root: the nearest ancestor of the
// binary's location (bin/pelicula) that holds compose/docker-compose.yml.
func getScriptDir() string {
	start := ""
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			start = filepath.Dir(resolved)
		} else {
			start = filepath.Dir(exe)
		}
	}
	if start == "" {
		start, _ = os.Getwd()
	}
	return walkUpForMarker(start, composeMarker)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

const secretAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// generateSecret returns n random alphanumeric characters from crypto/rand.
// Alphanumeric keeps values safe inside .env, shell healthchecks and headers.
func generateSecret(n int) string {
	limit := big.NewInt(int64(len(secretAlphabet)))
	b := make([]byte, n)
	for i := range b {
		v, err := rand.Int(rand.Reader, limit)
		if err != nil {
			// crypto/rand failing means the OS entropy source is broken;
			// a predictable secret would be worse than stopping.
			fatal("cannot read system randomness: " + err.Error())
		}
		b[i] = secretAlphabet[v.Int64()]
	}
	return string(b)
}

var (
	gitVersionOnce sync.Once
	gitVersionVal  string
)

// gitDescribe returns `git describe --tags --always --dirty` for the repo at
// dir. When git is unavailable it falls back to the version baked into this
// binary, so compose still gets a meaningful PELICULA_VERSION.
func gitDescribe(dir string) string {
	out, err := exec.Command("git", "-C", dir, "describe", "--tags", "--always", "--dirty").Output()
	if v := strings.TrimSpace(string(out)); err == nil && v != "" {
		return v
	}
	return version
}

// cachedGitVersion runs gitDescribe once per process.
func cachedGitVersion(dir string) string {
	gitVersionOnce.Do(func() { gitVersionVal = gitDescribe(dir) })
	return gitVersionVal
}

// requireEnv exits with a pointer to `pelicula up` when .env does not exist.
func requireEnv(envFile string) {
	if !fileExists(envFile) {
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

// peliculaBaseURL returns the local dashboard URL for env's PELICULA_PORT
// (default 7354). Append a path such as "/api/health".
func peliculaBaseURL(env EnvMap) string {
	return "http://localhost:" + envDefault(env, "PELICULA_PORT", "7354")
}
