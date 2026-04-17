package main

func cmdUpdate(ctx *Context, _ []string) {
	ctx.LoadEnv()
	c := composeInvocation(ctx)

	info("Pulling latest images...")
	if err := c.Run("pull"); err != nil {
		fatal("docker compose pull failed: " + err.Error())
	}

	info("Recreating containers...")
	if err := c.Run("up", "-d"); err != nil {
		fatal("docker compose up failed: " + err.Error())
	}

	pass("Update complete")
}
