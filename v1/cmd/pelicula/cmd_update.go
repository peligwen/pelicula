package main

// cmdUpdate pulls newer images, rebuilds the Pelicula image and recreates
// whatever changed.
func cmdUpdate(ctx *Context, _ []string) {
	ctx.LoadEnv()
	c := ctx.compose(false)

	progress("Pulling images...")
	if err := c.Run("pull"); err != nil {
		fatal("docker compose pull failed: " + err.Error())
	}

	progress("Rebuilding and recreating containers...")
	if err := c.Run("up", "-d", "--build"); err != nil {
		fatal("docker compose up failed: " + err.Error())
	}
	ok("Update complete")
}
