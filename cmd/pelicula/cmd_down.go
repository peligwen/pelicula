package main

import (
	"os"
)

func cmdDown(ctx *Context, _ []string) {
	if _, err := os.Stat(ctx.EnvFile); err != nil {
		// No .env — tear down by project name only (no YAML parsing needed).
		// Docker finds containers via the com.docker.compose.project label.
		// intentional: pre-env path; no profiles available, RunProjectOnly used
		warn("No .env file found — tearing down by project name")
		c := ctx.newCompose()
		if err := c.RunProjectOnly("down", "--remove-orphans"); err != nil {
			fatal("docker compose down failed: " + err.Error())
		}
		pass("Stack stopped")
		return
	}

	// Normal path: .env exists — activate every known profile unconditionally so
	// that teardown covers whatever `up` started, regardless of current env state.
	// composeInvocation is intentionally NOT used here: it only activates profiles
	// matching the current env, which would leak profile-gated containers if env
	// changed between `up` and `down` (e.g. WireGuard key cleared after VPN start).
	ctx.LoadEnv()
	c := composeInvocation(ctx)
	c.profiles = []string{"vpn", "apprise"}
	if err := c.Run("down", "--remove-orphans"); err != nil {
		fatal("docker compose down failed: " + err.Error())
	}
	pass("Stack stopped")
}
