package main

import (
	"path/filepath"
)

func cmdDown(_ []string) {
	scriptDir := getScriptDir()
	plat := Detect(scriptDir)
	c := NewCompose(scriptDir, plat.NeedsSudo)

	requireEnv(filepath.Join(scriptDir, ".env"))

	if err := c.Run("--profile", "apprise", "down", "--remove-orphans"); err != nil {
		fatal("docker compose down failed: " + err.Error())
	}
	pass("Stack stopped")
}
