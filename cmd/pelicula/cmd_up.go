package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// cmdUp implements the "up" subcommand.
func cmdUp(_ []string) {
	scriptDir := getScriptDir()
	envFile := filepath.Join(scriptDir, ".env")

	// If no .env, run setup wizard, then continue with up
	if _, err := os.Stat(envFile); err != nil {
		fmt.Println()
		fmt.Printf("%sNo configuration found — starting setup wizard.%s\n", colorBold, colorReset)
		fmt.Println()

		plat := Detect(scriptDir)
		c := NewCompose(scriptDir, plat.NeedsSudo)

		setupCompose := filepath.Join(scriptDir, "compose", "docker-compose.setup.yml")
		if _, err := os.Stat(setupCompose); err != nil {
			fatal("compose/docker-compose.setup.yml not found — make sure you're running from the pelicula directory")
		}

		home, _ := os.UserHomeDir()
		setupEnv := os.Environ()
		setupEnv = append(setupEnv,
			"HOST_PLATFORM="+plat.HostPlatformID(),
			"HOST_TZ="+plat.TZ,
			fmt.Sprintf("HOST_PUID=%d", plat.UID),
			fmt.Sprintf("HOST_PGID=%d", plat.GID),
			"HOST_HOME="+home,
			"HOST_CONFIG_DIR="+plat.DefaultConfigDir,
			"HOST_LIBRARY_DIR="+plat.DefaultLibraryDir,
			"HOST_WORK_DIR="+plat.DefaultWorkDir,
			"HOST_LAN_URL="+detectLANURL(),
		)

		setupCmd := c.buildSetupCmd(setupCompose, setupEnv)
		if err := setupCmd.Run(); err != nil {
			fatal("Failed to start setup containers: " + err.Error())
		}

		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		fmt.Printf("  Open %s in your browser to continue setup\n", bold("http://localhost:7354/"))
		fmt.Println()
		openBrowser("http://localhost:7354/")

		info("Waiting for setup to complete (Ctrl+C to abort)...")
		maxWait := 150
		if v := os.Getenv("PELICULA_SETUP_TIMEOUT"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				maxWait = n
			}
		}
		completed := false
		for i := 0; i < maxWait; i++ {
			select {
			case <-ctx.Done():
				fmt.Println()
				warn("Setup cancelled.")
				_ = c.runSetupDown(setupCompose)
				return
			default:
			}
			time.Sleep(2 * time.Second)
			if _, err := os.Stat(envFile); err == nil {
				pass("Configuration saved")
				completed = true
				break
			}
			if i == maxWait-1 {
				warn("Setup timed out after 5 minutes")
				_ = c.runSetupDown(setupCompose)
				return
			}
		}
		if !completed {
			_ = c.runSetupDown(setupCompose)
			return
		}

		// Tear down setup containers before starting main stack —
		// they share container names (pelicula-api, nginx) and port 7354.
		info("Cleaning up setup containers...")
		if err := c.runSetupDown(setupCompose); err != nil {
			warn("Failed to stop setup containers: " + err.Error())
		}

		fmt.Println()
	}

	// Load and migrate .env
	progress("Loading configuration...")
	env, err := ParseEnv(envFile)
	if err != nil {
		fatal("Failed to read .env: " + err.Error())
	}
	if _, err := MigrateEnv(envFile); err != nil {
		warn("Failed to migrate .env: " + err.Error())
	}
	// Re-read after migration
	env, err = ParseEnv(envFile)
	if err != nil {
		fatal("Failed to read .env after migration: " + err.Error())
	}

	configDir := env["CONFIG_DIR"]
	libraryDir := env["LIBRARY_DIR"]
	workDir := env["WORK_DIR"]
	port := envDefault(env, "PELICULA_PORT", "7354")

	// Validate critical path vars — empty values produce cryptic Docker Compose errors.
	for _, check := range []struct{ key, val string }{
		{"CONFIG_DIR", configDir},
		{"LIBRARY_DIR", libraryDir},
		{"WORK_DIR", workDir},
	} {
		if check.val == "" {
			fatal(check.key + ` is empty in .env — set it manually or run: pelicula reset-config`)
		}
	}

	plat := Detect(scriptDir)
	progress("Detected: " + plat.PlatformLabel())
	c := NewCompose(scriptDir, plat.NeedsSudo)

	info("Starting stack...")

	// Ensure libraries.json exists (migrates old installs to the library registry).
	configPeliculaDir := filepath.Join(configDir, "pelicula")
	libs, err := readOrCreateLibraries(configPeliculaDir)
	if err != nil {
		warn("Failed to read libraries.json: " + err.Error())
		libs = defaultLibraries().Libraries
	}

	// Create directory structure
	progress("Setting up directories...")
	if err := setupDirs(configDir, libraryDir, workDir, libs); err != nil {
		var dce *dirCreateError
		if errors.As(err, &dce) && os.IsPermission(dce.err) {
			ancestor := firstExistingAncestor(dce.path)
			if ancestor == "" {
				ancestor = filepath.Dir(dce.path)
			}
			fmt.Fprintf(os.Stderr, "%s✗ Permission denied creating %s%s\n", colorRed, dce.path, colorReset)
			fmt.Fprintf(os.Stderr, "  The directory %s%s%s exists but is not writable.\n", colorBold, ancestor, colorReset)
			fmt.Fprintf(os.Stderr, "  Create the required folder first, then re-run %s:\n\n", bold("pelicula up"))
			fmt.Fprintf(os.Stderr, "    sudo mkdir -p %s\n", filepath.Dir(dce.path))
			fmt.Fprintf(os.Stderr, "    sudo chown %d:%d %s\n\n", plat.UID, plat.GID, filepath.Dir(dce.path))
			fmt.Fprintf(os.Stderr, "  On Synology: create the shared folder in DSM File Station instead.\n\n")
			os.Exit(1)
		}
		fatal("Failed to create directories: " + err.Error())
	}

	// Generate TLS cert if missing
	if err := SetupCert(configDir); err != nil {
		warn("TLS cert generation failed: " + err.Error())
	}

	// Render remote configs
	if err := RenderRemoteConfigs(scriptDir, env); err != nil {
		fatal(err.Error())
	}

	// Check /dev/net/tun on Linux
	if err := CheckTUN(); err != nil {
		fmt.Fprintf(os.Stderr, "%s%s%s Run %s to create it.\n",
			colorRed, err.Error(), colorReset, bold("pelicula up"))
		os.Exit(1)
	}

	// Ensure nginx remote.conf placeholder exists (needed even when remote is disabled)
	nginxRemoteConf := filepath.Join(configDir, "nginx", "remote.conf")
	if _, err := os.Stat(nginxRemoteConf); err != nil {
		_ = os.MkdirAll(filepath.Join(configDir, "nginx"), 0755)
		_ = os.WriteFile(nginxRemoteConf, []byte("# Remote access disabled\n"), 0644)
	}

	// Seed all service configs
	if err := SeedAllConfigs(configDir); err != nil {
		fatal("Config seeding failed: " + err.Error())
	}

	// Enable compose profiles based on config
	wgKey := env["WIREGUARD_PRIVATE_KEY"]
	if wgKey != "" {
		c.profiles = append(c.profiles, "vpn")
	}

	notifMode := envDefault(env, "NOTIFICATIONS_MODE", "internal")
	if notifMode == "apprise" {
		c.profiles = append(c.profiles, "apprise")
	}

	// Regenerate the external-libraries compose override before starting.
	librariesOverridePath := filepath.Join(scriptDir, "compose", "docker-compose.libraries.yml")
	if err := generateLibrariesOverride(configPeliculaDir, librariesOverridePath); err != nil {
		warn("Failed to generate libraries override: " + err.Error())
	}

	progress("Starting containers...")
	if err := c.Run("up", "-d", "--remove-orphans"); err != nil {
		fatal("docker compose up failed: " + err.Error())
	}

	if wgKey != "" {
		// Wait for gluetun health
		info("Connecting to VPN...")
		const maxAttempts = 30
		vpnConnected := false
		for i := 0; i < maxAttempts; i++ {
			health, err := c.DockerInspect("{{.State.Health.Status}}", "gluetun")
			if err == nil && health == "healthy" {
				vpnConnected = true
				break
			}
			time.Sleep(2 * time.Second)
		}
		if vpnConnected {
			pass("VPN connected")
		} else {
			warn("VPN not ready — check: pelicula logs gluetun")
		}
	} else {
		info("VPN not configured — download services skipped")
	}

	fmt.Println()
	fmt.Printf("%s%sStack is running!%s\n", colorGreen, colorBold, colorReset)

	host := lanIP()
	remoteEnabled := env["REMOTE_ACCESS_ENABLED"] == "true"

	fmt.Println()
	if remoteEnabled {
		fmt.Printf("  %sDashboard (LAN only)%s\n", colorBold, colorReset)
	} else {
		fmt.Printf("  %sDashboard%s\n", colorBold, colorReset)
	}
	fmt.Printf("  http://%s:%s/\n", host, port)
	fmt.Println()
	fmt.Printf("  %sJellyfin%s\n", colorBold, colorReset)
	fmt.Printf("  http://%s:%s/jellyfin/\n", host, port)

	// Remote access section
	if remoteEnabled {
		rHost := env["REMOTE_HOSTNAME"]
		rHTTPS := envDefault(env, "REMOTE_HTTPS_PORT", "8920")
		if rHost != "" {
			fmt.Println()
			fmt.Printf("  %sRemote Jellyfin%s\n", colorBold, colorReset)
			fmt.Println("  ────────────────────────────────────────────")
			fmt.Printf("  https://%s:%s/\n", rHost, rHTTPS)
			fmt.Println()
			fmt.Printf("  Port forwarding: route port %s to this machine.\n", rHTTPS)
			fmt.Printf("  Do %sNOT%s forward port %s — it exposes admin tools.\n", colorBold, colorReset, port)
		}
	}

	// Check if any admin has registered yet. The middleware may still be
	// starting, so poll for up to 15 seconds before giving up quietly.
	regURL := fmt.Sprintf("http://localhost:%s/api/pelicula/register/check", port)
	if needsAdmin := checkNeedsAdmin(regURL); needsAdmin {
		fmt.Printf("  %s%s No admin account yet — register now:%s\n", colorYellow, colorBold, colorReset)
		dashURL := fmt.Sprintf("http://%s:%s/register", host, port)
		fmt.Printf("  %s\n", dashURL)
		fmt.Println()
		openBrowser(dashURL)
	}

	fmt.Println()

}

// checkNeedsAdmin polls the register/check endpoint (up to 15s) and returns
// true if initial_setup is true (no admin has registered yet).
func checkNeedsAdmin(url string) bool {
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		var data struct {
			InitialSetup bool `json:"initial_setup"`
		}
		err = json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		if err != nil {
			return false
		}
		return data.InitialSetup
	}
	return false
}

// lanIP returns the first non-loopback IPv4 address, or "localhost" if none found.
func lanIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "localhost"
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && ip.To4() != nil {
				return ip.String()
			}
		}
	}
	return "localhost"
}
