package main

import (
	"path/filepath"
)

func cmdRebuild(args []string) {
	scriptDir := getScriptDir()
	requireEnv(filepath.Join(scriptDir, ".env"))

	plat := Detect(scriptDir)
	c := NewCompose(scriptDir, plat.NeedsSudo)

	targets := args
	if len(targets) == 0 {
		targets = []string{"pelicula-api", "procula"}
	}

	for _, svc := range targets {
		switch svc {
		case "nginx":
			info("Reloading nginx...")
			if err := c.DockerExec("nginx", "nginx", "-s", "reload"); err != nil {
				fatal("nginx reload failed: " + err.Error())
			}
			pass("nginx reloaded")

		case "pelicula-api", "middleware":
			info("Rebuilding pelicula-api...")
			if err := c.Run("build", "pelicula-api"); err != nil {
				fatal("build pelicula-api failed: " + err.Error())
			}
			if err := c.Run("up", "-d", "--no-deps", "pelicula-api"); err != nil {
				fatal("up pelicula-api failed: " + err.Error())
			}
			pass("pelicula-api rebuilt and restarted")

		case "procula":
			info("Rebuilding procula...")
			if err := c.Run("build", "procula"); err != nil {
				fatal("build procula failed: " + err.Error())
			}
			if err := c.Run("up", "-d", "--no-deps", "procula"); err != nil {
				fatal("up procula failed: " + err.Error())
			}
			pass("procula rebuilt and restarted")

		default:
			warn("Unknown service '" + svc + "'. Known targets: pelicula-api, procula, nginx")
		}
	}
}
