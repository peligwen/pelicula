package main

func cmdLogs(ctx *Context, args []string) {
	requireEnv(ctx.EnvFile)
	ctx.LoadEnv()
	c := composeInvocation(ctx)

	composeArgs := []string{"logs", "-f"}
	if len(args) > 0 {
		composeArgs = append(composeArgs, args...)
	}

	if err := c.Run(composeArgs...); err != nil {
		// logs -f exits on Ctrl+C — that's expected, not an error
		_ = err
	}
}
