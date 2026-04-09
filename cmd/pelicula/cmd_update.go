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
	if envDefault(env, "NOTIFICATIONS_MODE", "internal") == "apprise" {
		c.profiles = append(c.profiles, "apprise")
	}
	if wgKey := env["WIREGUARD_PRIVATE_KEY"]; wgKey != "" {
		c.profiles = append(c.profiles, "vpn")
	}
	if err := c.Run("up", "-d"); err != nil {
		fatal("docker compose up failed: " + err.Error())
	}

	pass("Update complete")
}
