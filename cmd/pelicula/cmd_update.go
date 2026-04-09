package main

import (
	"path/filepath"
)

func cmdUpdate(_ []string) {
	scriptDir := getScriptDir()
	envFile := filepath.Join(scriptDir, ".env")
	env := loadEnvOrFatal(envFile)

	plat := Detect(scriptDir)
	c := NewCompose(scriptDir, plat.NeedsSudo)

	info("Pulling latest images...")
	if err := c.Run("pull"); err != nil {
		fatal("docker compose pull failed: " + err.Error())
	}

	info("Recreating containers...")
	upArgs := []string{"up", "-d"}
	if envDefault(env, "NOTIFICATIONS_MODE", "internal") == "apprise" {
		upArgs = append(upArgs, "--profile", "apprise")
	}
	if err := c.Run(upArgs...); err != nil {
		fatal("docker compose up failed: " + err.Error())
	}

	pass("Update complete")
}
