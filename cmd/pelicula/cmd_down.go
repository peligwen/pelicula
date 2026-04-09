package main

import (
	"path/filepath"
)

func cmdDown(_ []string) {
	scriptDir := getScriptDir()
	plat := Detect(scriptDir)
	c := NewCompose(scriptDir, plat.NeedsSudo)

	requireEnv(filepath.Join(scriptDir, ".env"))

	// Enable all profiles so down catches every container
	c.profiles = append(c.profiles, "vpn", "apprise")

	if err := c.Run("down", "--remove-orphans"); err != nil {
		fatal("docker compose down failed: " + err.Error())
	}
	pass("Stack stopped")
}
