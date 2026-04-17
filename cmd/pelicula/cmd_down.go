package main

import (
	"os"
)

func cmdDown(ctx *Context, _ []string) {
	if _, err := os.Stat(ctx.EnvFile); err != nil {
		// No .env — tear down by project name only (no YAML parsing needed).
		// Docker finds containers via the com.docker.compose.project label.
		warn("No .env file found — tearing down by project name")
		c := ctx.newCompose()
		if err := c.RunProjectOnly("down", "--remove-orphans"); err != nil {
			fatal("docker compose down failed: " + err.Error())
		}
		pass("Stack stopped")
		return
	}

	// Normal path: .env exists — load env and set profiles from config.
	ctx.LoadEnv()
	c := composeInvocation(ctx)
	if err := c.Run("down", "--remove-orphans"); err != nil {
		fatal("docker compose down failed: " + err.Error())
	}
	pass("Stack stopped")
}
