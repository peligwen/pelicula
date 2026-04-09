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

// setupDirs creates the required directory tree for configDir, libraryDir, and workDir.
func setupDirs(configDir, libraryDir, workDir string) error {
	dirs := []string{
		filepath.Join(configDir, "gluetun"),
		filepath.Join(configDir, "qbittorrent"),
		filepath.Join(configDir, "prowlarr"),
		filepath.Join(configDir, "sonarr"),
		filepath.Join(configDir, "radarr"),
		filepath.Join(configDir, "jellyfin"),
		filepath.Join(configDir, "bazarr"),
		filepath.Join(configDir, "procula", "jobs"),
		filepath.Join(configDir, "procula", "profiles"),
		filepath.Join(configDir, "pelicula"),
		filepath.Join(configDir, "certs"),
		filepath.Join(libraryDir, "movies"),
		filepath.Join(libraryDir, "tv"),
		filepath.Join(workDir, "downloads"),
		filepath.Join(workDir, "downloads", "incomplete"),
		filepath.Join(workDir, "downloads", "radarr"),
		filepath.Join(workDir, "downloads", "tv-sonarr"),
		filepath.Join(workDir, "processing"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return nil
}

// writeEnvFile writes a fresh .env file with the given parameters.
func writeEnvFile(envPath, configDir, libraryDir, workDir, puid, pgid, tz,
	wgKey, countries, port, auth, proculaKey, jfPass string) error {

	// Back up if exists
	if _, err := os.Stat(envPath); err == nil {
		bak := fmt.Sprintf("%s.bak.%d", envPath, time.Now().Unix())
		_ = copyFile(envPath, bak)
	}

	m := EnvMap{
		"CONFIG_DIR":            configDir,
		"LIBRARY_DIR":           libraryDir,
		"WORK_DIR":              workDir,
		"PUID":                  puid,
		"PGID":                  pgid,
		"TZ":                    tz,
		"WIREGUARD_PRIVATE_KEY": wgKey,
		"SERVER_COUNTRIES":      countries,
		"PELICULA_PORT":         port,
		"PELICULA_AUTH":         auth,
		"JELLYFIN_PASSWORD":     jfPass,
		"PROCULA_API_KEY":       proculaKey,
		"TRANSCODING_ENABLED":   "false",
		"NOTIFICATIONS_ENABLED": "false",
		"NOTIFICATIONS_MODE":    "internal",
	}
	return WriteEnv(envPath, m)
}

// cmdSetup implements the "setup" subcommand.
// In the Go CLI, setup is a browser-based wizard: it starts the setup containers
// and opens the browser. CLI (--advanced) setup is preserved from the bash CLI.
func cmdSetup(args []string) {
	scriptDir := getScriptDir()
	envFile := filepath.Join(scriptDir, ".env")

	if _, err := os.Stat(envFile); err == nil {
		warn(".env already exists. Run " + bold("pelicula up") + " to start the stack.")
		warn("To reconfigure, delete .env and run setup again.")
		return
	}

	plat := Detect(scriptDir)
	c := NewCompose(scriptDir, plat.NeedsSudo)

	fmt.Printf("%spelicula setup%s\n", colorBold, colorReset)
	fmt.Println()
	info("Detected platform: " + bold(plat.PlatformLabel()))
	fmt.Println()

	// Start setup containers
	setupCompose := filepath.Join(scriptDir, "docker-compose.setup.yml")
	if _, err := os.Stat(setupCompose); err != nil {
		fatal("docker-compose.setup.yml not found — make sure you're running from the pelicula directory")
	}

	home, _ := os.UserHomeDir()
	env := os.Environ()
	env = append(env,
		"HOST_PLATFORM="+plat.HostPlatformID(),
		"HOST_TZ="+plat.TZ,
		fmt.Sprintf("HOST_PUID=%d", plat.UID),
		fmt.Sprintf("HOST_PGID=%d", plat.GID),
		"HOST_HOME="+home,
		"HOST_CONFIG_DIR="+plat.DefaultConfigDir,
		"HOST_LIBRARY_DIR="+plat.DefaultLibraryDir,
		"HOST_WORK_DIR="+plat.DefaultWorkDir,
	)

	setupCmd := c.buildSetupCmd(setupCompose, env)
	if err := setupCmd.Run(); err != nil {
		fatal("Failed to start setup containers: " + err.Error())
	}

	// Tear down setup containers on exit (signal or normal return).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	defer func() {
		_ = c.runSetupDown(setupCompose)
	}()

	fmt.Println()
	fmt.Printf("  Open %s in your browser to continue setup\n", bold("http://localhost:7354/"))
	fmt.Println()

	// Try to open the browser
	openBrowser("http://localhost:7354/")

	// Poll for .env to appear (written by the wizard)
	info("Waiting for setup to complete (Ctrl+C to abort)...")
	const maxWait = 150 // 150 * 2s = 5 minutes
	completed := false
	for i := 0; i < maxWait; i++ {
		select {
		case <-ctx.Done():
			fmt.Println()
			warn("Setup cancelled — tearing down setup containers...")
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
			fmt.Printf("Run %s for CLI setup instead.\n", bold("pelicula setup --advanced"))
			return
		}
	}
	if !completed {
		return
	}

	fmt.Println()
	fmt.Printf("%s%sSetup complete!%s\n", colorGreen, colorBold, colorReset)
	fmt.Printf("Run %s to start the stack.\n", bold("pelicula up"))
}
