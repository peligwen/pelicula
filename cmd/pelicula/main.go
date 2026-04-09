package main

import (
	"fmt"
	"os"
)

var version = "dev" // set via -ldflags at build time

func main() {
	// Strip -v/--verbose flag from args
	verbose := false
	var args []string
	for _, a := range os.Args[1:] {
		switch a {
		case "-v", "--verbose":
			verbose = true
		default:
			args = append(args, a)
		}
	}
	_ = verbose // consumed by output helpers via package-level var

	if len(args) == 0 {
		usage()
		return
	}

	switch args[0] {
	case "setup":
		cmdSetup(args[1:])
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
		// Delegate to bash test runner for now
		fmt.Println("Test mode delegates to the bash test runner.")
		fmt.Println("Run: bash ./pelicula test")
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

func usage() {
	fmt.Print(`Pelicula — clone-and-run media stack

Usage: pelicula <command> [options]

Lifecycle:
  setup               First-time setup (opens browser wizard)
  up                  Start the stack
  down                Stop the stack
  restart [service]   Restart service(s)
  rebuild [service]   Rebuild and restart middleware/procula/nginx
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
