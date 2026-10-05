package main

import "strings"

// cmdRestart restarts the named services, or every service when none is given.
func cmdRestart(ctx *Context, args []string) {
	ctx.LoadEnv()
	if err := ctx.compose(true).Run(append([]string{"restart"}, args...)...); err != nil {
		fatal("docker compose restart failed: " + err.Error())
	}
	if len(args) == 0 {
		ok("Restarted all services")
		return
	}
	ok("Restarted: " + strings.Join(args, ", "))
}
