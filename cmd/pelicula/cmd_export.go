package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func cmdExport(args []string) {
	scriptDir := getScriptDir()
	envFile := filepath.Join(scriptDir, ".env")
	env := loadEnvOrFatal(envFile)
	port := envDefault(env, "PELICULA_PORT", "7354")

	// Default output filename: pelicula-backup-YYYY-MM-DD.json
	date := time.Now().Format("2006-01-02")
	outFile := "pelicula-backup-" + date + ".json"
	if len(args) > 0 && args[0] != "" {
		outFile = args[0]
	}

	info("Exporting library metadata...")

	url := fmt.Sprintf("http://localhost:%s/api/pelicula/export", port)
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fail("Could not reach pelicula-api at " + url + " — is the stack running?")
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		fail("Authentication required — use the dashboard to export when auth is enabled")
		os.Exit(1)
	}
	if resp.StatusCode != 200 {
		fail(fmt.Sprintf("Export failed (HTTP %d): %s", resp.StatusCode, string(body)))
		os.Exit(1)
	}

	// Validate JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		fail("API returned invalid JSON — is the stack running and wired?")
		os.Exit(1)
	}

	if err := os.WriteFile(outFile, body, 0644); err != nil {
		fatal("Failed to write " + outFile + ": " + err.Error())
	}

	// Count movies and series
	movies := countSlice(parsed, "movies")
	series := countSlice(parsed, "series")
	pass(fmt.Sprintf("Exported %d movies, %d series → %s", movies, series, outFile))
}

func countSlice(m map[string]interface{}, key string) int {
	if v, ok := m[key]; ok {
		if s, ok := v.([]interface{}); ok {
			return len(s)
		}
	}
	return 0
}
