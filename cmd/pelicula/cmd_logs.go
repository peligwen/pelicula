package main

// cmdLogs follows logs of all services, or of the named ones.
func cmdLogs(ctx *Context, args []string) {
	ctx.LoadEnv()
	// Ctrl+C ends `logs -f`; that is the normal way out, not an error.
	_ = ctx.compose(true).Run(logsArgs(args)...)
}

// logsArgs builds the compose arguments for `pelicula logs [svc...]`.
func logsArgs(services []string) []string {
	return append([]string{"logs", "-f", "--tail", "100"}, services...)
}
