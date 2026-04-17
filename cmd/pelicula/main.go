package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

var version = "dev" // set via -ldflags at build time

func main() {
	// Strip -v/--verbose/--debug flags from args
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

	// Fast-path commands that need no bootstrap context.
	if len(args) == 0 {
		usage()
		return
	}
	switch args[0] {
	case "--version", "-V":
		fmt.Println("pelicula", version)
		return
	case "-h", "--help", "help":
		usage()
		return
	}

	// Build the context once — runs platform detection (docker info) one time.
	ctx := newContext()

	if debugMode {
		printDiagnostics(ctx)
	}

	switch args[0] {
	case "up":
		cmdUp(ctx, args[1:])
	case "down":
		cmdDown(ctx, args[1:])
	case "restart":
		cmdRestart(ctx, args[1:])
	case "restart-acquire":
		cmdRestartAcquire(ctx, args[1:])
	case "rebuild":
		cmdRebuild(ctx, args[1:])
	case "redeploy":
		cmdRedeploy(ctx, args[1:])
	case "reset-config":
		cmdResetConfig(ctx, args[1:])
	case "status":
		cmdStatus(ctx, args[1:])
	case "logs":
		cmdLogs(ctx, args[1:])
	case "check-vpn":
		cmdCheckVPN(ctx, args[1:])
	case "update":
		cmdUpdate(ctx, args[1:])
	case "export":
		cmdExport(ctx, args[1:])
	case "import-backup":
		cmdImportBackup(ctx, args[1:])
	case "import":
		cmdImport(ctx, args[1:])
	case "test":
		cmdTest(ctx, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
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
	_, envErr := os.Stat(ctx.EnvFile)

	debug("pelicula version: " + version)
	debug("GOOS: " + runtime.GOOS + " GOARCH: " + runtime.GOARCH)
	debug("binary: " + resolved)
	debug("script dir: " + ctx.ScriptDir)
	if envErr == nil {
		debug(".env: " + ctx.EnvFile + " (found)")
	} else {
		debug(".env: " + ctx.EnvFile + " (not found)")
	}

	plat := ctx.Plat
	debug(fmt.Sprintf("platform: %s (synology=%v, wsl=%v, needsSudo=%v, uid=%d, gid=%d)",
		plat.PlatformLabel(), plat.IsSynology, plat.IsWSL, plat.NeedsSudo, plat.UID, plat.GID))
	debug("TZ: " + plat.TZ)
	debug("default config dir: " + plat.DefaultConfigDir)
	debug("default library dir: " + plat.DefaultLibraryDir)

	composeFile := filepath.Join(ctx.ScriptDir, "compose", "docker-compose.yml")
	if _, err := os.Stat(composeFile); err == nil {
		debug("compose file: " + composeFile + " (found)")
	} else {
		debug("compose file: " + composeFile + " (NOT FOUND)")
	}

	if out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Output(); err == nil {
		debug("docker server: " + string(out))
	} else {
		debug("docker: " + err.Error())
	}
}

func usage() {
	fmt.Print(`Pelicula — clone-and-run media stack

Usage: pelicula <command> [options]

Lifecycle:
  up                  Start the stack (runs setup wizard on first run)
  down                Stop the stack
  restart [service]   Restart service(s)
  rebuild [service]   Rebuild and restart middleware/procula/nginx
  redeploy [service]  Rebuild images then do a full stack down/up
  update              Pull latest images and restart
  status              Show service health
  logs [service]      Tail service logs

Configuration:
  reset-config [svc]  Reset service configs (soft/per-service/all)

Data:
  export [file]       Export library backup
  import-backup file  Restore from backup
  import [dir]        Open media import wizard

Network:
  check-vpn           Verify VPN connectivity

Options:
  -v, --verbose       Verbose output
  -h, --help          Show this help
  --version           Show version
`)
}
