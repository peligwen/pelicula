package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// cmdUp implements the "up" subcommand.
func cmdUp(_ []string) {
	scriptDir := getScriptDir()
	envFile := filepath.Join(scriptDir, ".env")

	// If no .env, run setup wizard, then continue with up
	firstRun := false
	if _, err := os.Stat(envFile); err != nil {
		firstRun = true
		fmt.Println()
		fmt.Printf("%sNo configuration found — starting setup wizard.%s\n", colorBold, colorReset)
		fmt.Println()

		plat := Detect(scriptDir)
		c := NewCompose(scriptDir, plat.NeedsSudo)

		setupCompose := filepath.Join(scriptDir, "docker-compose.setup.yml")
		if _, err := os.Stat(setupCompose); err != nil {
			fatal("docker-compose.setup.yml not found — make sure you're running from the pelicula directory")
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
		)

		setupCmd := c.buildSetupCmd(setupCompose, setupEnv)
		if err := setupCmd.Run(); err != nil {
			fatal("Failed to start setup containers: " + err.Error())
		}

		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		defer func() {
			_ = c.runSetupDown(setupCompose)
		}()

		fmt.Printf("  Open %s in your browser to continue setup\n", bold("http://localhost:7354/"))
		fmt.Println()
		openBrowser("http://localhost:7354/")

		info("Waiting for setup to complete (Ctrl+C to abort)...")
		const maxWait = 150
		completed := false
		for i := 0; i < maxWait; i++ {
			select {
			case <-ctx.Done():
				fmt.Println()
				warn("Setup cancelled.")
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
				return
			}
		}
		if !completed {
			return
		}
		fmt.Println()
	}

	// Load and migrate .env
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

	plat := Detect(scriptDir)
	c := NewCompose(scriptDir, plat.NeedsSudo)

	info("Starting stack...")

	// Create directory structure
	if err := setupDirs(configDir, libraryDir, workDir); err != nil {
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

	if err := c.Run("up", "-d"); err != nil {
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

	fmt.Println()

	if firstRun {
		dashURL := fmt.Sprintf("http://%s:%s/", host, port)
		openBrowser(dashURL)
	}
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
