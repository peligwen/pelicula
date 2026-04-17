package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// cmdImport opens the browser to the import wizard.
func cmdImport(ctx *Context, _ []string) {
	ctx.LoadEnv()
	port := envDefault(ctx.Env, "PELICULA_PORT", "7354")

	url := fmt.Sprintf("http://localhost:%s/import", port)
	info("Opening import wizard: " + url)
	openBrowser(url)
}

// cmdImportBackup restores a backup via the middleware API.
func cmdImportBackup(ctx *Context, args []string) {
	ctx.LoadEnv()
	port := envDefault(ctx.Env, "PELICULA_PORT", "7354")

	if len(args) == 0 || args[0] == "" {
		fail("Usage: pelicula import-backup <backup-file.json>")
		os.Exit(1)
	}

	backupFile := args[0]
	if _, err := os.Stat(backupFile); err != nil {
		fail("File not found: " + backupFile)
		os.Exit(1)
	}

	data, err := os.ReadFile(backupFile)
	if err != nil {
		fatal("Failed to read " + backupFile + ": " + err.Error())
	}

	info("Importing backup from " + backupFile + "...")

	url := fmt.Sprintf("http://localhost:%s/api/pelicula/import-backup", port)
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		fail("Could not reach pelicula-api at " + url + " — is the stack running?")
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		fail("Authentication required — use the dashboard to import backups when auth is enabled")
		os.Exit(1)
	}
	if resp.StatusCode != 200 {
		fail(fmt.Sprintf("Import failed (HTTP %d): %s", resp.StatusCode, string(body)))
		os.Exit(1)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		fail("Could not parse response: " + err.Error())
		os.Exit(1)
	}

	moviesAdded, _ := result["moviesAdded"].(float64)
	moviesSkipped, _ := result["moviesSkipped"].(float64)
	moviesFailed, _ := result["moviesFailed"].(float64)
	seriesAdded, _ := result["seriesAdded"].(float64)
	seriesSkipped, _ := result["seriesSkipped"].(float64)
	seriesFailed, _ := result["seriesFailed"].(float64)

	fmt.Printf("  Movies  — added: %.0f, skipped: %.0f, failed: %.0f\n",
		moviesAdded, moviesSkipped, moviesFailed)
	fmt.Printf("  Series  — added: %.0f, skipped: %.0f, failed: %.0f\n",
		seriesAdded, seriesSkipped, seriesFailed)

	if errors, ok := result["errors"].([]interface{}); ok && len(errors) > 0 {
		fmt.Printf("\n  %sErrors:%s\n", colorRed, colorReset)
		limit := 10
		if len(errors) < limit {
			limit = len(errors)
		}
		for _, e := range errors[:limit] {
			fmt.Printf("    %v\n", e)
		}
		if len(errors) > 10 {
			fmt.Printf("    ... and %d more\n", len(errors)-10)
		}
	} else {
		fmt.Printf("\n  %s%sImport complete%s\n", colorGreen, colorBold, colorReset)
	}
}
