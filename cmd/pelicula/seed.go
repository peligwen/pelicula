package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// xmlEscape escapes special XML characters in s so it is safe to interpolate
// into an XML element value.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

// seedConfig writes content to file only if the file does not already exist.
func seedConfig(file, content string) error {
	if _, err := os.Stat(file); err == nil {
		// Already exists — skip
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		return fmt.Errorf("seedConfig mkdir %s: %w", filepath.Dir(file), err)
	}
	return os.WriteFile(file, []byte(content), 0644)
}

// enforceArrAuth patches an *arr config.xml to use External authentication
// (DisabledForLocalAddresses) and enforce dark theme.
// This is idempotent and safe to call on every startup.
func enforceArrAuth(configPath string) error {
	if _, err := os.Stat(configPath); err != nil {
		return nil // doesn't exist yet — nothing to do
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}

	original := string(data)
	patched := original

	// Fix auth bypass — only patch if Enabled (leave DisabledForLocalAddresses alone)
	if strings.Contains(patched, "<AuthenticationRequired>Enabled</AuthenticationRequired>") {
		reMethod := regexp.MustCompile(`<AuthenticationMethod>[^<]*</AuthenticationMethod>`)
		reRequired := regexp.MustCompile(`<AuthenticationRequired>[^<]*</AuthenticationRequired>`)
		patched = reMethod.ReplaceAllString(patched, "<AuthenticationMethod>External</AuthenticationMethod>")
		patched = reRequired.ReplaceAllString(patched, "<AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>")
	}

	// Enforce dark theme
	if strings.Contains(patched, "<Theme>") {
		reTheme := regexp.MustCompile(`<Theme>[^<]*</Theme>`)
		patched = reTheme.ReplaceAllString(patched, "<Theme>dark</Theme>")
	}

	if patched == original {
		return nil // no changes
	}

	return os.WriteFile(configPath, []byte(patched), 0644)
}

// extractAPIKey reads the <ApiKey> value from an *arr config.xml.
// Returns "" if not found.
func extractAPIKey(configPath string) string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`<ApiKey>([^<]*)</ApiKey>`)
	match := re.FindSubmatch(data)
	if match == nil {
		return ""
	}
	return string(match[1])
}

// SeedAllConfigs seeds all service configs under configDir.
// This mirrors the seeding done in cmd_up in the bash CLI.
func SeedAllConfigs(configDir string) error {
	// *arr configs
	if err := seedConfig(
		filepath.Join(configDir, "sonarr", "config.xml"),
		`<Config><UrlBase>/sonarr</UrlBase><AuthenticationMethod>External</AuthenticationMethod><AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired></Config>`,
	); err != nil {
		return fmt.Errorf("seed sonarr: %w", err)
	}
	if err := seedConfig(
		filepath.Join(configDir, "radarr", "config.xml"),
		`<Config><UrlBase>/radarr</UrlBase><AuthenticationMethod>External</AuthenticationMethod><AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired></Config>`,
	); err != nil {
		return fmt.Errorf("seed radarr: %w", err)
	}
	if err := seedConfig(
		filepath.Join(configDir, "prowlarr", "config.xml"),
		`<Config><UrlBase>/prowlarr</UrlBase><AuthenticationMethod>External</AuthenticationMethod><AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired></Config>`,
	); err != nil {
		return fmt.Errorf("seed prowlarr: %w", err)
	}

	// Enforce auth bypass on existing arr configs (arr rewrites config.xml on first boot)
	for _, svc := range []string{"sonarr", "radarr", "prowlarr"} {
		if err := enforceArrAuth(filepath.Join(configDir, svc, "config.xml")); err != nil {
			return fmt.Errorf("enforceArrAuth %s: %w", svc, err)
		}
	}

	// Jellyfin network.xml
	if err := seedConfig(
		filepath.Join(configDir, "jellyfin", "network.xml"),
		`<?xml version="1.0" encoding="utf-8"?><NetworkConfiguration xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema"><BaseUrl>/jellyfin</BaseUrl></NetworkConfiguration>`,
	); err != nil {
		return fmt.Errorf("seed jellyfin network.xml: %w", err)
	}

	// Jellyfin branding.xml (Cotton Candy CSS theme)
	jellyfinConfigDir := filepath.Join(configDir, "jellyfin", "config")
	if err := os.MkdirAll(jellyfinConfigDir, 0755); err != nil {
		return err
	}
	if err := seedConfig(
		filepath.Join(jellyfinConfigDir, "branding.xml"),
		jellyfinBrandingXML,
	); err != nil {
		return fmt.Errorf("seed jellyfin branding.xml: %w", err)
	}

	// Bazarr config.ini
	bazarrConfigDir := filepath.Join(configDir, "bazarr", "config")
	if err := os.MkdirAll(bazarrConfigDir, 0755); err != nil {
		return err
	}
	if err := seedConfig(
		filepath.Join(bazarrConfigDir, "config.ini"),
		bazarrConfigIni,
	); err != nil {
		return fmt.Errorf("seed bazarr config.ini: %w", err)
	}

	// qBittorrent config
	qbtConfigDir := filepath.Join(configDir, "qbittorrent", "qBittorrent")
	if err := os.MkdirAll(qbtConfigDir, 0755); err != nil {
		return err
	}
	if err := seedConfig(
		filepath.Join(qbtConfigDir, "qBittorrent.conf"),
		qbtConf,
	); err != nil {
		return fmt.Errorf("seed qBittorrent.conf: %w", err)
	}
	if err := seedConfig(
		filepath.Join(qbtConfigDir, "categories.json"),
		`{"radarr":{"save_path":"/downloads/radarr/"},"tv-sonarr":{"save_path":"/downloads/tv-sonarr/"}}`,
	); err != nil {
		return fmt.Errorf("seed qBittorrent categories.json: %w", err)
	}

	return nil
}

// ResetArrService wipes an *arr service directory and re-seeds config.xml,
// preserving the given API key.
func ResetArrService(name, dir, urlBase, apiKey string) error {
	info(fmt.Sprintf("Resetting %s...", name))
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	keyXML := ""
	if apiKey != "" {
		keyXML = "<ApiKey>" + xmlEscape(apiKey) + "</ApiKey>"
	}
	content := fmt.Sprintf(
		"<Config><UrlBase>%s</UrlBase>%s<AuthenticationMethod>External</AuthenticationMethod><AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired></Config>",
		urlBase, keyXML,
	)
	return os.WriteFile(filepath.Join(dir, "config.xml"), []byte(content), 0644)
}

// jellyfinBrandingXML is the Cotton Candy CSS theme for Jellyfin.
// This must match the exact content from the bash CLI.
var jellyfinBrandingXML = strings.Join([]string{
	`<?xml version="1.0" encoding="utf-8"?>`,
	`<BrandingOptions xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema">`,
	`  <CustomCss>`,
	`:root {`,
	`  --accent: #f060a8;`,
	`  --accent2: #7080e8;`,
	`  --success: #40c8a8;`,
	`  --warning: #f8d040;`,
	`  --error: #f060a8;`,
	`  --background-input: #ffffff;`,
	`  --background-page: #f8f0fb;`,
	`  --color-text-body: #2a1f35;`,
	`  --color-text-header: #2a1f35;`,
	`  --background-header: rgba(255,255,255,0.92);`,
	`  --theme-border: rgba(180,140,220,0.3);`,
	`}`,
	`@import url("https://fonts.googleapis.com/css2?family=Nunito:wght@400;700;900&family=Nunito+Sans:wght@400;600&display=swap");`,
	`body { font-family: "Nunito Sans", sans-serif !important; background: #f8f0fb !important; }`,
	`.jellyfin-header-bar { background: rgba(255,255,255,0.92) !important; backdrop-filter: blur(16px) !important; }`,
	`  </CustomCss>`,
	`</BrandingOptions>`,
}, "\n")

var bazarrConfigIni = "[general]\nbase_url=/bazarr\n"

var qbtConf = "[Preferences]\n" +
	`WebUI\AuthSubnetWhitelistEnabled=true` + "\n" +
	`WebUI\AuthSubnetWhitelist=172.16.0.0/12` + "\n" +
	`WebUI\LocalHostAuth=false` + "\n" +
	`WebUI\CSRFProtection=false` + "\n" +
	"\n" +
	"[BitTorrent]\n" +
	`Session\DefaultSavePath=/downloads/` + "\n" +
	`Session\TempPathEnabled=true` + "\n" +
	`Session\TempPath=/downloads/incomplete/`

// resetJellyfin wipes the Jellyfin config dir and re-seeds the base files.
func resetJellyfin(configDir string) error {
	info("Resetting Jellyfin...")
	jellyfinDir := filepath.Join(configDir, "jellyfin")
	if err := os.RemoveAll(jellyfinDir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(jellyfinDir, "config"), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(
		filepath.Join(jellyfinDir, "network.xml"),
		[]byte(`<?xml version="1.0" encoding="utf-8"?><NetworkConfiguration xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema"><BaseUrl>/jellyfin</BaseUrl></NetworkConfiguration>`),
		0644,
	); err != nil {
		return err
	}
	return os.WriteFile(
		filepath.Join(jellyfinDir, "config", "branding.xml"),
		[]byte(jellyfinBrandingXML),
		0644,
	)
}

// resetQBittorrent wipes and re-seeds the qBittorrent config dir.
func resetQBittorrent(configDir string) error {
	info("Resetting qBittorrent...")
	qbtDir := filepath.Join(configDir, "qbittorrent")
	if err := os.RemoveAll(qbtDir); err != nil {
		return err
	}
	subDir := filepath.Join(qbtDir, "qBittorrent")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(subDir, "qBittorrent.conf"), []byte(qbtConf), 0644); err != nil {
		return err
	}
	return os.WriteFile(
		filepath.Join(subDir, "categories.json"),
		[]byte(`{"radarr":{"save_path":"/downloads/radarr/"},"tv-sonarr":{"save_path":"/downloads/tv-sonarr/"}}`),
		0644,
	)
}

// resetProculaJobs clears the Procula job queue directory.
func resetProculaJobs(configDir string) error {
	info("Clearing Procula job queue...")
	jobsDir := filepath.Join(configDir, "procula", "jobs")
	if err := os.RemoveAll(jobsDir); err != nil {
		return err
	}
	return os.MkdirAll(jobsDir, 0755)
}

// isStackRunning checks if any compose services are running using docker compose ps.
func isStackRunning(c *Compose) bool {
	out, err := c.RunSilent("ps", "--services", "--filter", "status=running")
	if err != nil {
		return false
	}
	return len(bytes.TrimSpace(out)) > 0
}
