package main

// cmdDown stops and removes the stack's containers.
func cmdDown(ctx *Context, _ []string) {
	if !fileExists(ctx.EnvFile) {
		// No .env: tear down by project name alone (compose finds the
		// containers through their project label).
		warn("No .env file found — tearing down by project name")
		c := NewCompose(ctx.ScriptDir, ctx.EnvFile, ctx.Plat.NeedsSudo, ctx.Plat.IsSynology, "pelicula")
		if err := c.RunProjectOnly("down", "--remove-orphans"); err != nil {
			fatal("docker compose down failed: " + err.Error())
		}
		ok("Stack stopped")
		return
	}

	ctx.LoadEnv()
	// vpn profile always on, so down also removes VPN containers started
	// before the WireGuard key was cleared.
	if err := ctx.compose(true).Run("down", "--remove-orphans"); err != nil {
		fatal("docker compose down failed: " + err.Error())
	}
	ok("Stack stopped")
}
