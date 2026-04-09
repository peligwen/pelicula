package main

import (
	"context"
	"fmt"
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
	if _, err := os.Stat(envFile); err != nil {
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

	// Build compose up args with optional profiles
	upArgs := []string{"up", "-d"}

	// VPN profile: only when WireGuard key is configured
	wgKey := env["WIREGUARD_PRIVATE_KEY"]
	if wgKey != "" {
		upArgs = append(upArgs, "--profile", "vpn")
	}

	// Apprise profile: when notifications use apprise
	notifMode := envDefault(env, "NOTIFICATIONS_MODE", "internal")
	if notifMode == "apprise" {
		upArgs = append(upArgs, "--profile", "apprise")
	}

	if err := c.Run(upArgs...); err != nil {
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

	// Print admin credentials if auth is enabled
	if jfPass := env["JELLYFIN_PASSWORD"]; jfPass != "" {
		adminUser := envDefault(env, "JELLYFIN_ADMIN_USER", "admin")
		fmt.Println()
		fmt.Printf("  %sAdmin login:%s  %s / %s\n", colorBold, colorReset, adminUser, jfPass)
	}

	fmt.Println()
	fmt.Println("  Service         URL")
	fmt.Println("  ─────────────── ──────────────────────────")
	fmt.Printf("  Dashboard       http://localhost:%s/\n", port)
	fmt.Printf("  Sonarr          http://localhost:%s/sonarr/\n", port)
	fmt.Printf("  Radarr          http://localhost:%s/radarr/\n", port)
	if wgKey != "" {
		fmt.Printf("  Prowlarr        http://localhost:%s/prowlarr/\n", port)
		fmt.Printf("  qBittorrent     http://localhost:%s/qbt/\n", port)
	}
	fmt.Printf("  Jellyfin        http://localhost:%s/jellyfin/\n", port)
	fmt.Printf("  Bazarr          http://localhost:%s/bazarr/\n", port)
	fmt.Printf("  Procula API     http://localhost:%s/api/procula/status\n", port)

	if env["REMOTE_ACCESS_ENABLED"] == "true" {
		rHost := env["REMOTE_HOSTNAME"]
		rHTTPS := envDefault(env, "REMOTE_HTTPS_PORT", "8920")
		if rHost != "" {
			fmt.Println()
			fmt.Printf("  Remote Jellyfin  https://%s:%s/\n", rHost, rHTTPS)
		}
	}
}
