package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func cmdResetConfig(args []string) {
	scriptDir := getScriptDir()
	envFile := filepath.Join(scriptDir, ".env")
	env := loadEnvOrFatal(envFile)

	arg := ""
	if len(args) > 0 {
		arg = args[0]
	}

	switch arg {
	case "":
		resetConfigSoft(scriptDir, envFile, env)
	case "all", "full":
		resetConfigAll(scriptDir, envFile, env)
	case "sonarr", "radarr", "prowlarr", "jellyfin", "qbittorrent", "procula-jobs":
		resetConfigService(scriptDir, envFile, arg, env)
	default:
		fmt.Fprintf(os.Stderr, "Unknown reset target: %s\n", arg)
		fmt.Fprintln(os.Stderr, "Usage: pelicula reset-config [sonarr|radarr|prowlarr|jellyfin|qbittorrent|procula-jobs|all]")
		os.Exit(1)
	}
}

// resetConfigSoft wipes service configs, preserving API keys / gluetun / certs / user auth.
func resetConfigSoft(scriptDir, envFile string, env EnvMap) {
	configDir := env["CONFIG_DIR"]

	fmt.Printf("%sReset configuration%s\n", colorBold, colorReset)
	fmt.Println()
	fmt.Println("This wipes service config and databases so auto-wiring runs fresh.")
	fmt.Println("Your API keys, VPN config, TLS certs, and user auth are preserved.")
	fmt.Println()
	fmt.Printf("%sWhat will be cleared:%s\n", colorBold, colorReset)
	fmt.Println("  sonarr/      — all config except API key")
	fmt.Println("  radarr/      — all config except API key")
	fmt.Println("  prowlarr/    — config only (indexer database kept)")
	fmt.Println("  jellyfin/    — full wipe (wizard re-runs on next up)")
	fmt.Println("  qbittorrent/ — full wipe (re-seeded on next up)")
	fmt.Println("  procula/jobs/ — pending job queue")
	fmt.Println()
	fmt.Printf("%sWhat is kept:%s\n", colorBold, colorReset)
	fmt.Println("  gluetun/   — WireGuard config")
	fmt.Println("  certs/     — TLS certificates")
	fmt.Println("  pelicula/  — users and auth config")
	fmt.Println("  procula/profiles/, procula/notifications.json")
	fmt.Println()
	fmt.Printf("%sContinue? [y/N]%s ", colorRed, colorReset)

	if !confirmYN() {
		info("Aborted.")
		return
	}

	plat := Detect(scriptDir)
	c := NewCompose(scriptDir, plat.NeedsSudo)
	ensureStackDown(c)

	// Extract API keys before wiping
	sonarrKey := extractAPIKey(filepath.Join(configDir, "sonarr", "config.xml"))
	radarrKey := extractAPIKey(filepath.Join(configDir, "radarr", "config.xml"))
	prowlarrKey := extractAPIKey(filepath.Join(configDir, "prowlarr", "config.xml"))

	if err := ResetArrService("Sonarr", filepath.Join(configDir, "sonarr"), "/sonarr", sonarrKey); err != nil {
		warn("sonarr reset: " + err.Error())
	}
	if err := ResetArrService("Radarr", filepath.Join(configDir, "radarr"), "/radarr", radarrKey); err != nil {
		warn("radarr reset: " + err.Error())
	}

	// Prowlarr: only reset config.xml — preserve database (indexers live there)
	info("Resetting Prowlarr config (keeping indexer database)...")
	keyXML := ""
	if prowlarrKey != "" {
		keyXML = "<ApiKey>" + xmlEscape(prowlarrKey) + "</ApiKey>"
	}
	prowlarrConf := fmt.Sprintf(
		"<Config><UrlBase>/prowlarr</UrlBase>%s<AuthenticationMethod>External</AuthenticationMethod><AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired></Config>",
		keyXML,
	)
	_ = os.WriteFile(filepath.Join(configDir, "prowlarr", "config.xml"), []byte(prowlarrConf), 0644)

	_ = resetJellyfin(configDir)
	_ = resetQBittorrent(configDir)
	_ = resetProculaJobs(configDir)

	fmt.Println()
	pass("Config reset — run " + bold("pelicula up") + " to start fresh with auto-wiring.")
}

// resetConfigService resets a single named service.
func resetConfigService(scriptDir, envFile, svc string, env EnvMap) {
	configDir := env["CONFIG_DIR"]

	plat := Detect(scriptDir)
	c := NewCompose(scriptDir, plat.NeedsSudo)
	ensureStackDown(c)

	switch svc {
	case "sonarr":
		key := extractAPIKey(filepath.Join(configDir, "sonarr", "config.xml"))
		if err := ResetArrService("Sonarr", filepath.Join(configDir, "sonarr"), "/sonarr", key); err != nil {
			fatal(err.Error())
		}
	case "radarr":
		key := extractAPIKey(filepath.Join(configDir, "radarr", "config.xml"))
		if err := ResetArrService("Radarr", filepath.Join(configDir, "radarr"), "/radarr", key); err != nil {
			fatal(err.Error())
		}
	case "prowlarr":
		info("Resetting Prowlarr config (keeping indexer database)...")
		key := extractAPIKey(filepath.Join(configDir, "prowlarr", "config.xml"))
		keyXML := ""
		if key != "" {
			keyXML = "<ApiKey>" + xmlEscape(key) + "</ApiKey>"
		}
		content := fmt.Sprintf(
			"<Config><UrlBase>/prowlarr</UrlBase>%s<AuthenticationMethod>External</AuthenticationMethod><AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired></Config>",
			keyXML,
		)
		if err := os.WriteFile(filepath.Join(configDir, "prowlarr", "config.xml"), []byte(content), 0644); err != nil {
			fatal(err.Error())
		}
	case "jellyfin":
		if err := resetJellyfin(configDir); err != nil {
			fatal(err.Error())
		}
	case "qbittorrent":
		if err := resetQBittorrent(configDir); err != nil {
			fatal(err.Error())
		}
	case "procula-jobs":
		if err := resetProculaJobs(configDir); err != nil {
			fatal(err.Error())
		}
	}

	fmt.Println()
	pass(svc + " reset — run " + bold("pelicula up") + " to apply.")
}

// resetConfigAll does a full wipe: CONFIG_DIR + regenerate .env,
// preserving WireGuard key, VPN country, and path/host vars.
func resetConfigAll(scriptDir, envFile string, env EnvMap) {
	configDir := env["CONFIG_DIR"]

	// Safety guard
	if configDir == "" || configDir == "/" || configDir == os.Getenv("HOME") {
		fatal("Unsafe CONFIG_DIR: '" + configDir + "' — aborting")
	}

	libraryDir := env["LIBRARY_DIR"]
	workDir := env["WORK_DIR"]

	fmt.Printf("%s%sFull configuration reset%s\n", colorRed, colorBold, colorReset)
	fmt.Println()
	fmt.Println("This wipes the entire config directory and .env credentials, then rebuilds from scratch.")
	fmt.Println()
	fmt.Printf("%sWhat will be cleared:%s\n", colorBold, colorReset)
	fmt.Printf("  ALL service configs, databases, and caches under %s\n", configDir)
	fmt.Printf("  Jellyfin library metadata (media files in %s are kept)\n", libraryDir)
	fmt.Println("  Sonarr/Radarr API keys, series/movie databases")
	fmt.Println("  Pelicula admin accounts and invite tokens")
	fmt.Println("  Procula custom transcoding profiles and notification settings")
	fmt.Println("  TLS certificates (regenerated on next up)")
	fmt.Println()
	fmt.Printf("%sWhat is kept:%s\n", colorBold, colorReset)
	fmt.Println("  Prowlarr indexer database (indexers survive)")
	fmt.Println("  WireGuard key, VPN country, paths, PUID/PGID, TZ, port")
	fmt.Printf("  %s — all media files\n", libraryDir)
	fmt.Printf("  %s/downloads — torrents and seeding files\n", workDir)
	fmt.Println()
	fmt.Printf("%sType 'reset' to confirm, or anything else to abort:%s ", colorRed, colorReset)

	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	if strings.TrimSpace(line) != "reset" {
		info("Aborted.")
		return
	}

	plat := Detect(scriptDir)
	c := NewCompose(scriptDir, plat.NeedsSudo)
	ensureStackDown(c)

	// Stash values to preserve
	savedWGKey := env["WIREGUARD_PRIVATE_KEY"]
	savedCountries := env["SERVER_COUNTRIES"]

	// Stash Prowlarr indexer database
	prowlarrDB := filepath.Join(configDir, "prowlarr", "prowlarr.db")
	var prowlarrStash string
	if _, err := os.Stat(prowlarrDB); err == nil {
		tmpDir, err := os.MkdirTemp("", "pelicula-prowlarr-stash-")
		if err == nil {
			prowlarrStash = tmpDir
			// Copy prowlarr.db and .db-wal, .db-shm etc.
			entries, _ := filepath.Glob(prowlarrDB + "*")
			for _, entry := range entries {
				dest := filepath.Join(tmpDir, filepath.Base(entry))
				_ = copyFile(entry, dest)
			}
			info("Prowlarr indexer database stashed")
		}
	}

	info("Wiping " + configDir + "...")
	if err := os.RemoveAll(configDir); err != nil {
		fatal("Failed to wipe config dir: " + err.Error())
	}

	// Rebuild directory structure and TLS cert
	if err := setupDirs(configDir, libraryDir, workDir); err != nil {
		fatal("Failed to recreate directories: " + err.Error())
	}
	if err := SetupCert(configDir); err != nil {
		warn("TLS cert generation failed: " + err.Error())
	}

	// Restore Prowlarr indexer database
	if prowlarrStash != "" {
		_ = os.MkdirAll(filepath.Join(configDir, "prowlarr"), 0755)
		entries, _ := filepath.Glob(filepath.Join(prowlarrStash, "prowlarr.db*"))
		for _, entry := range entries {
			dest := filepath.Join(configDir, "prowlarr", filepath.Base(entry))
			_ = os.Rename(entry, dest)
		}
		_ = os.RemoveAll(prowlarrStash)
		info("Prowlarr indexer database restored")
	}

	// Regenerate .env — preserved identity/VPN values, fresh internal API key.
	// Admin password is left empty — the setup wizard handles that on next up.
	newProculaKey := generateAPIKey()

	if err := writeEnvFile(
		envFile,
		configDir, libraryDir, workDir,
		env["PUID"], env["PGID"], env["TZ"],
		savedWGKey, savedCountries,
		envDefault(env, "PELICULA_PORT", "7354"),
		"", newProculaKey, "",
	); err != nil {
		fatal("Failed to write .env: " + err.Error())
	}
	pass("Wrote fresh " + envFile)

	fmt.Println()
	pass("Full reset complete — run " + bold("pelicula up") + " to start fresh.")
}

// ensureStackDown stops the stack if any services are running.
func ensureStackDown(c *Compose) {
	if isStackRunning(c) {
		warn("Stack is running — stopping it first...")
		_ = c.Run("down")
	}
}

// confirmYN reads a yes/no answer from stdin. Returns true for y/Y.
func confirmYN() bool {
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	return line == "y" || line == "Y"
}
