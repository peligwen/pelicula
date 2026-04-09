package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// cmdUp implements the "up" subcommand.
func cmdUp(_ []string) {
	scriptDir := getScriptDir()
	envFile := filepath.Join(scriptDir, ".env")

	// If no .env, run setup wizard first, then continue with up
	if _, err := os.Stat(envFile); err != nil {
		fmt.Println()
		fmt.Printf("%sNo configuration found.%s\n", colorBold, colorReset)
		fmt.Println()
		fmt.Printf("  Option 1: Open %s in your browser to set up\n", bold("http://localhost:7354/"))
		fmt.Printf("  Option 2: Run %s for CLI setup (Ctrl+C to abort)\n", bold("pelicula setup"))
		fmt.Println()
		cmdSetup(nil)
		// If setup still didn't produce a .env (cancelled / timed out), bail.
		if _, err2 := os.Stat(envFile); err2 != nil {
			return
		}
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
			colorRed, err.Error(), colorReset, bold("pelicula setup"))
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

	// Build compose up args with optional apprise profile
	upArgs := []string{"up", "-d"}
	notifMode := envDefault(env, "NOTIFICATIONS_MODE", "internal")
	if notifMode == "apprise" {
		upArgs = append(upArgs, "--profile", "apprise")
	}

	if err := c.Run(upArgs...); err != nil {
		fatal("docker compose up failed: " + err.Error())
	}

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

	fmt.Println()
	fmt.Printf("%s%sStack is running!%s\n", colorGreen, colorBold, colorReset)

	// Print admin credentials if JELLYFIN_PASSWORD is set
	if jfPass := env["JELLYFIN_PASSWORD"]; jfPass != "" {
		fmt.Println()
		fmt.Printf("  %sAdmin login:%s  admin / %s\n", colorBold, colorReset, jfPass)
	}

	// Print service URLs
	fmt.Println()
	fmt.Println("  Service         URL")
	fmt.Println("  ─────────────── ──────────────────────────")
	fmt.Printf("  Dashboard       http://localhost:%s/\n", port)
	fmt.Printf("  Sonarr          http://localhost:%s/sonarr/\n", port)
	fmt.Printf("  Radarr          http://localhost:%s/radarr/\n", port)
	fmt.Printf("  Prowlarr        http://localhost:%s/prowlarr/\n", port)
	fmt.Printf("  qBittorrent     http://localhost:%s/qbt/\n", port)
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
