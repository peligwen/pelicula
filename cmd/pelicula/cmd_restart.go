package main

import (
	"path/filepath"
)

var acquireServices = []string{
	"gluetun", "qbittorrent", "prowlarr", "sonarr", "radarr", "pelicula-api", "procula",
}

func cmdRestart(ctx *Context, args []string) {
	if len(args) == 0 {
		// Full restart: down + up
		cmdDown(ctx, nil)
		cmdUp(ctx, nil)
		return
	}

	// Single service restart
	requireEnv(ctx.EnvFile)
	ctx.LoadEnv()
	c := composeInvocation(ctx)

	if err := c.Run(append([]string{"restart"}, args...)...); err != nil {
		fatal("docker compose restart failed: " + err.Error())
	}
	pass("Restarted: " + args[0])
}

func cmdRestartAcquire(ctx *Context, _ []string) {
	ctx.LoadEnv()
	c := composeInvocation(ctx)

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
	configDir := ctx.Env["CONFIG_DIR"]
	for _, svc := range []string{"sonarr", "radarr", "prowlarr"} {
		cfgPath := filepath.Join(configDir, svc, "config.xml")
		if err := enforceArrConfig(cfgPath); err != nil {
			warn("enforceArrConfig " + svc + ": " + err.Error())
		}
	}

	pass("Acquisition services restarted")
}
