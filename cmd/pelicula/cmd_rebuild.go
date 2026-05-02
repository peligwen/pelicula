package main

import (
	"strings"
)

func cmdRedeploy(ctx *Context, args []string) {
	requireEnv(ctx.EnvFile)
	// intentional: only targets non-profile services (pelicula-api, procula); down/up handled by cmdDown/cmdUp
	c := ctx.newCompose()

	targets := args
	if len(targets) == 0 {
		targets = []string{"pelicula-api", "procula"}
	}

	for i, svc := range targets {
		switch svc {
		case "pelicula-api", "middleware", "procula":
			// normalise middleware alias so docker compose build receives the real service name
			if svc == "middleware" {
				targets[i] = "pelicula-api"
			}
		default:
			warn("Unknown service '" + svc + "'. Known targets: pelicula-api, procula")
			return
		}
	}

	info("Building images: " + strings.Join(targets, ", "))
	buildArgs := []string{"build", "--build-arg", "VERSION=" + gitDescribe()}
	buildArgs = append(buildArgs, targets...)
	if err := c.Run(buildArgs...); err != nil {
		fatal("build failed: " + err.Error())
	}

	cmdDown(ctx, nil)
	cmdUp(ctx, nil)
}

func cmdRebuild(ctx *Context, args []string) {
	requireEnv(ctx.EnvFile)
	// intentional: only targets non-profile services (nginx, pelicula-api, procula)
	c := ctx.newCompose()

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
			if err := c.Run("build", "--build-arg", "VERSION="+gitDescribe(), "pelicula-api"); err != nil {
				fatal("build pelicula-api failed: " + err.Error())
			}
			if err := c.Run("up", "-d", "--no-deps", "pelicula-api"); err != nil {
				fatal("up pelicula-api failed: " + err.Error())
			}
			pass("pelicula-api rebuilt and restarted")

		case "procula":
			info("Rebuilding procula...")
			if err := c.Run("build", "--build-arg", "VERSION="+gitDescribe(), "procula"); err != nil {
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
