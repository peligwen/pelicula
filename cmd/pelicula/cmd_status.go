package main

// cmdStatus shows container state (`docker compose ps`).
func cmdStatus(ctx *Context, _ []string) {
	ctx.LoadEnv()
	if err := ctx.compose(true).Run("ps"); err != nil {
		fatal("docker compose ps failed: " + err.Error())
	}
}
