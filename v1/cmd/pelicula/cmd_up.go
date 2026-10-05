package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

const (
	gluetunWaitAttempts = 60 // × 2s = 2 minutes
	healthWaitAttempts  = 60 // × 2s = 2 minutes
	waitInterval        = 2 * time.Second
)

// cmdUp implements `pelicula up`: first-run setup, directories, TUN override,
// config seeding, `docker compose up`, then waits for the stack to answer.
func cmdUp(ctx *Context, _ []string) {
	if composeFile := filepath.Join(ctx.ScriptDir, composeMarker); !fileExists(composeFile) {
		fatal(composeFile + " not found — run pelicula from a Pelicula checkout")
	}
	if !fileExists(ctx.EnvFile) {
		firstRunSetup(ctx)
	}

	progress("Loading configuration...")
	env := loadEnvOrFatal(ctx.EnvFile)
	if set := completeEnv(env, ctx.Plat); len(set) > 0 {
		if err := WriteEnv(ctx.EnvFile, env); err != nil {
			fatal("Failed to update .env: " + err.Error())
		}
		warn("Filled in blank .env values: " + strings.Join(set, ", "))
		if slices.Contains(set, "JELLYFIN_PASSWORD") {
			printAdminCredentials(env)
		}
	}
	if err := validateEnv(env); err != nil {
		fatal(err.Error())
	}
	ctx.Env = env

	configDir, libraryDir, workDir := env["CONFIG_DIR"], env["LIBRARY_DIR"], env["WORK_DIR"]
	vpn := vpnEnabled(env)

	progress("Detected: " + ctx.Plat.PlatformLabel())

	progress("Setting up directories...")
	if err := setupDirs(configDir, libraryDir, workDir); err != nil {
		reportDirError(err, ctx.Plat)
	}

	if err := ensureTUN(runtime.GOOS, filepath.Join(ctx.ScriptDir, "compose"), tunDevice, vpn); err != nil {
		fatal(err.Error())
	}

	progress("Seeding service configs...")
	if err := SeedAllConfigs(configDir); err != nil {
		fatal("Config seeding failed: " + err.Error())
	}

	c := ctx.compose(false)
	progress("Starting containers (the first run builds the Pelicula image)...")
	if err := c.Run("up", "-d", "--build", "--remove-orphans"); err != nil {
		fatal("docker compose up failed: " + err.Error())
	}

	if vpn {
		progress("Waiting for the VPN tunnel...")
		if waitForGluetun(c, gluetunWaitAttempts, waitInterval) {
			ok("VPN connected")
		} else {
			warn("VPN not healthy yet — check: pelicula logs gluetun")
		}
	} else {
		info("No WireGuard key configured — VPN, download client and indexer manager are not started")
	}

	progress("Waiting for the Pelicula API...")
	healthURL := peliculaBaseURL(env) + "/api/health"
	h, healthy := waitForHealth(&http.Client{Timeout: 5 * time.Second}, healthURL, healthWaitAttempts, waitInterval)
	if !healthy {
		warn("The API did not answer at " + healthURL + " within 2 minutes — check: pelicula doctor")
	} else if h.Wired {
		ok("API healthy (" + h.Version + "); services are wired")
	} else {
		ok("API healthy (" + h.Version + "); auto-wiring is still running — see: pelicula logs pelicula")
	}

	printUpSummary(env, lanIP())
	if !healthy {
		os.Exit(1)
	}
}

// firstRunSetup runs the terminal wizard and writes the first .env.
func firstRunSetup(ctx *Context) {
	fmt.Println()
	if !isTerminal(os.Stdin) {
		warn("stdin is not a terminal — setup will use the defaults")
	}
	home, _ := os.UserHomeDir()
	env := runWizard(os.Stdin, os.Stdout, ctx.Plat, ctx.ScriptDir, home)
	if err := WriteEnv(ctx.EnvFile, env); err != nil {
		fatal("Failed to write .env: " + err.Error())
	}
	fmt.Println()
	ok("Wrote " + ctx.EnvFile)
	printAdminCredentials(env)
	fmt.Println()
}

// printAdminCredentials shows the generated Jellyfin admin login once.
func printAdminCredentials(env EnvMap) {
	fmt.Printf("\n  %sJellyfin / dashboard admin login%s (shown only now; also kept in .env)\n", colorBold, colorReset)
	fmt.Printf("    user:     %s\n    password: %s\n", env["JELLYFIN_ADMIN_USER"], env["JELLYFIN_PASSWORD"])
}

// printUpSummary prints the URLs and where the admin credentials live.
func printUpSummary(env EnvMap, host string) {
	port := envDefault(env, "PELICULA_PORT", "7354")
	fmt.Println()
	fmt.Printf("%s%sStack is running!%s\n\n", colorGreen, colorBold, colorReset)
	fmt.Printf("  %sDashboard%s  http://%s:%s/\n", colorBold, colorReset, host, port)
	fmt.Printf("  %sJellyfin%s   http://%s:%s/jellyfin/\n", colorBold, colorReset, host, port)
	fmt.Println()
	fmt.Printf("  Sign in with the Jellyfin admin account: user %q, password in JELLYFIN_PASSWORD (.env).\n", envDefault(env, "JELLYFIN_ADMIN_USER", "admin"))
	fmt.Println()
}

// reportDirError explains a failed mkdir (with fix-it commands for permission
// errors) and exits.
func reportDirError(err error, plat Platform) {
	var dce *dirCreateError
	if errors.As(err, &dce) && os.IsPermission(dce.err) {
		ancestor := firstExistingAncestor(dce.path)
		if ancestor == "" {
			ancestor = filepath.Dir(dce.path)
		}
		fmt.Fprintf(os.Stderr, "%s✗ Permission denied creating %s%s\n", colorRed, dce.path, colorReset)
		fmt.Fprintf(os.Stderr, "  The directory %s exists but is not writable.\n", bold(ancestor))
		fmt.Fprintf(os.Stderr, "  Create the folder first, then re-run %s:\n\n", bold("pelicula up"))
		fmt.Fprintf(os.Stderr, "    sudo mkdir -p %s\n", filepath.Dir(dce.path))
		fmt.Fprintf(os.Stderr, "    sudo chown %d:%d %s\n\n", plat.UID, plat.GID, filepath.Dir(dce.path))
		fmt.Fprintf(os.Stderr, "  On Synology, create the shared folder in DSM File Station instead.\n")
		os.Exit(1)
	}
	fatal("Failed to create directories: " + err.Error())
}

// waitForGluetun polls gluetun's docker health until it is "healthy".
func waitForGluetun(c *Compose, attempts int, interval time.Duration) bool {
	for i := 0; i < attempts; i++ {
		if status, err := c.ServiceHealth("gluetun"); err == nil && status == "healthy" {
			return true
		}
		time.Sleep(interval)
	}
	return false
}

// apiHealth is the body of GET /api/health.
type apiHealth struct {
	OK      bool   `json:"ok"`
	Wired   bool   `json:"wired"`
	Version string `json:"version"`
}

// fetchHealth GETs url and decodes a healthy /api/health response.
func fetchHealth(client *http.Client, url string) (apiHealth, error) {
	var h apiHealth
	resp, err := client.Get(url)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return h, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return h, err
	}
	if !h.OK {
		return h, errors.New("health reports not ok")
	}
	return h, nil
}

// waitForHealth polls url until it reports ok, returning the last response
// (so the caller can report `wired`) and whether it became healthy.
func waitForHealth(client *http.Client, url string, attempts int, interval time.Duration) (apiHealth, bool) {
	for i := 0; i < attempts; i++ {
		if h, err := fetchHealth(client, url); err == nil {
			return h, true
		}
		time.Sleep(interval)
	}
	return apiHealth{}, false
}
