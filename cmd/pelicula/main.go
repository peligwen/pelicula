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

	if debugMode {
		printDiagnostics()
	}

	if len(args) == 0 {
		usage()
		return
	}

	switch args[0] {
	case "up":
		cmdUp(args[1:])
	case "down":
		cmdDown(args[1:])
	case "restart":
		cmdRestart(args[1:])
	case "restart-acquire":
		cmdRestartAcquire(args[1:])
	case "rebuild":
		cmdRebuild(args[1:])
	case "redeploy":
		cmdRedeploy(args[1:])
	case "reset-config":
		cmdResetConfig(args[1:])
	case "status":
		cmdStatus(args[1:])
	case "logs":
		cmdLogs(args[1:])
	case "check-vpn":
		cmdCheckVPN(args[1:])
	case "update":
		cmdUpdate(args[1:])
	case "export":
		cmdExport(args[1:])
	case "import-backup":
		cmdImportBackup(args[1:])
	case "import":
		cmdImport(args[1:])
	case "test":
		testScript := filepath.Join(getScriptDir(), "tests", "e2e.sh")
		cmd := exec.Command("bash", append([]string{testScript}, args[1:]...)...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			os.Exit(1)
		}
	case "--version", "-V":
		fmt.Println("pelicula", version)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		usage()
		os.Exit(1)
	}
}

func printDiagnostics() {
	exe, _ := os.Executable()
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe + " (symlink resolve failed: " + err.Error() + ")"
	}
	scriptDir := getScriptDir()
	envFile := filepath.Join(scriptDir, ".env")
	_, envErr := os.Stat(envFile)

	debug("pelicula version: " + version)
	debug("GOOS: " + runtime.GOOS + " GOARCH: " + runtime.GOARCH)
	debug("binary: " + resolved)
	debug("script dir: " + scriptDir)
	if envErr == nil {
		debug(".env: " + envFile + " (found)")
	} else {
		debug(".env: " + envFile + " (not found)")
	}

	plat := Detect(scriptDir)
	debug(fmt.Sprintf("platform: %s (synology=%v, wsl=%v, needsSudo=%v, uid=%d, gid=%d)",
		plat.PlatformLabel(), plat.IsSynology, plat.IsWSL, plat.NeedsSudo, plat.UID, plat.GID))
	debug("TZ: " + plat.TZ)
	debug("default config dir: " + plat.DefaultConfigDir)
	debug("default library dir: " + plat.DefaultLibraryDir)

	composeFile := filepath.Join(scriptDir, "compose", "docker-compose.yml")
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
