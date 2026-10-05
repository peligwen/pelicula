// Command pelicula is the Pelicula CLI: setup, lifecycle and health checks for
// the Docker Compose stack. It is deliberately stdlib-only and independent of
// the server's packages.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

var version = "dev" // set via -ldflags "-X main.version=..."

func main() {
	var args []string
	for _, a := range os.Args[1:] {
		switch a {
		case "-v", "--verbose":
			verboseMode = true
		case "--debug":
			debugMode = true
			verboseMode = true
		default:
			args = append(args, a)
		}
	}

	// Commands that need no bootstrap (platform detection runs `docker info`).
	if len(args) == 0 {
		usage()
		return
	}
	switch args[0] {
	case "version", "--version", "-V":
		fmt.Println("pelicula", version)
		return
	case "help", "-h", "--help":
		usage()
		return
	}

	ctx := newContext()
	if debugMode {
		printDiagnostics(ctx)
	}

	switch args[0] {
	case "up":
		cmdUp(ctx, args[1:])
	case "down":
		cmdDown(ctx, args[1:])
	case "status":
		cmdStatus(ctx, args[1:])
	case "logs":
		cmdLogs(ctx, args[1:])
	case "restart":
		cmdRestart(ctx, args[1:])
	case "update":
		cmdUpdate(ctx, args[1:])
	case "check-vpn":
		cmdCheckVPN(ctx, args[1:])
	case "reset-config":
		cmdResetConfig(ctx, args[1:])
	case "doctor":
		cmdDoctor(ctx, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", args[0])
		usage()
		os.Exit(1)
	}
}

func printDiagnostics(ctx *Context) {
	exe, _ := os.Executable()
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe + " (symlink resolve failed: " + err.Error() + ")"
	}
	debug("pelicula version: " + version)
	debug("GOOS: " + runtime.GOOS + " GOARCH: " + runtime.GOARCH)
	debug("binary: " + resolved)
	debug("repo dir: " + ctx.ScriptDir)
	debug(".env: " + ctx.EnvFile + fmt.Sprintf(" (exists=%v)", fileExists(ctx.EnvFile)))
	plat := ctx.Plat
	debug(fmt.Sprintf("platform: %s (synology=%v, wsl=%v, needsSudo=%v, uid=%d, gid=%d)",
		plat.PlatformLabel(), plat.IsSynology, plat.IsWSL, plat.NeedsSudo, plat.UID, plat.GID))
	debug("TZ: " + plat.TZ)
	debug("default dirs: config=" + plat.DefaultConfigDir + " library=" + plat.DefaultLibraryDir + " work=" + plat.DefaultWorkDir)
	composeFile := filepath.Join(ctx.ScriptDir, composeMarker)
	debug(fmt.Sprintf("compose file: %s (exists=%v)", composeFile, fileExists(composeFile)))
	docker := dockerBinary(plat.IsSynology, fileExists)
	if out, err := exec.Command(docker, "version", "--format", "{{.Server.Version}}").Output(); err == nil {
		debug("docker server: " + string(out))
	} else {
		debug("docker: " + err.Error())
	}
}

func usage() {
	fmt.Print(`Pelicula — clone-and-run media stack

Usage: pelicula <command> [options]

Commands:
  up                     Start the stack (first run: terminal setup wizard)
  down                   Stop and remove the containers
  status                 Show container status
  logs [service]         Follow service logs
  restart [service]      Restart one service, or all
  update                 Pull images, rebuild Pelicula, recreate what changed
  check-vpn              Show the VPN's public IP and forwarded port
  reset-config <svc|all> [--yes]
                         Delete a service's config dir (all = every service;
                         .env and media are kept), then run 'pelicula up'
  doctor                 Print a support report (secrets redacted)
  version                Print the CLI version
  help                   Show this help

Options:
  -v, --verbose          Verbose output
  --debug                Diagnostics (implies -v)
`)
}
