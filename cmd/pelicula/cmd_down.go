package main

import (
	"os"
	"path/filepath"
)

func cmdDown(_ []string) {
	scriptDir := getScriptDir()
	plat := Detect(scriptDir)
	c := NewCompose(scriptDir, plat.NeedsSudo)

	envFile := filepath.Join(scriptDir, ".env")
	if _, err := os.Stat(envFile); err != nil {
		// No .env — tear down by project name only (no YAML parsing needed).
		// Docker finds containers via the com.docker.compose.project label.
		warn("No .env file found — tearing down by project name")
		if err := c.RunProjectOnly("down", "--remove-orphans"); err != nil {
			fatal("docker compose down failed: " + err.Error())
		}
		pass("Stack stopped")
		return
	}

	// Normal path: .env exists, use full compose files + all profiles.
	c.profiles = append(c.profiles, "vpn", "apprise")
	if err := c.Run("down", "--remove-orphans"); err != nil {
		fatal("docker compose down failed: " + err.Error())
	}
	pass("Stack stopped")
}
