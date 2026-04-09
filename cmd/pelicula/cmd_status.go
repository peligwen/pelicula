package main

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"
)

func cmdStatus(_ []string) {
	scriptDir := getScriptDir()
	envFile := filepath.Join(scriptDir, ".env")
	env := loadEnvOrFatal(envFile)
	port := envDefault(env, "PELICULA_PORT", "7354")

	url := fmt.Sprintf("http://localhost:%s/api/pelicula/health", port)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fail("Could not reach middleware at " + url + " — is the stack running?")
		fmt.Println()
		info("Run: pelicula up")
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		fail(fmt.Sprintf("Health check returned HTTP %d", resp.StatusCode))
		fmt.Println(string(body))
		return
	}

	// Print the raw JSON response (the middleware formats it)
	fmt.Println(string(body))
}
