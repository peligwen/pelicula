package main

import (
	"path/filepath"
)

var acquireServices = []string{
	"gluetun", "qbittorrent", "prowlarr", "sonarr", "radarr", "pelicula-api", "procula",
}

func cmdRestart(args []string) {
	if len(args) == 0 {
		// Full restart: down + up
		cmdDown(nil)
		cmdUp(nil)
		return
	}

	// Single service restart
	scriptDir := getScriptDir()
	plat := Detect(scriptDir)
	c := NewCompose(scriptDir, plat.NeedsSudo)
	requireEnv(filepath.Join(scriptDir, ".env"))

	if err := c.Run(append([]string{"restart"}, args...)...); err != nil {
		fatal("docker compose restart failed: " + err.Error())
	}
	pass("Restarted: " + args[0])
}

func cmdRestartAcquire(_ []string) {
	scriptDir := getScriptDir()
	envFile := filepath.Join(scriptDir, ".env")
	env := loadEnvOrFatal(envFile)

	plat := Detect(scriptDir)
	c := NewCompose(scriptDir, plat.NeedsSudo)

	info("Restarting acquisition services (jellyfin and nginx stay up)...")

	stopArgs := append([]string{"stop"}, acquireServices...)
	if err := c.Run(stopArgs...); err != nil {
		fatal("docker compose stop failed: " + err.Error())
	}

	startArgs := append([]string{"start"}, acquireServices...)
	if err := c.Run(startArgs...); err != nil {
		fatal("docker compose start failed: " + err.Error())
	}

	// *arr apps rewrite config.xml on startup with auth enabled — patch it back
	configDir := env["CONFIG_DIR"]
	for _, svc := range []string{"sonarr", "radarr", "prowlarr"} {
		cfgPath := filepath.Join(configDir, svc, "config.xml")
		if err := enforceArrAuth(cfgPath); err != nil {
			warn("enforceArrAuth " + svc + ": " + err.Error())
		}
	}

	pass("Acquisition services restarted")
}
