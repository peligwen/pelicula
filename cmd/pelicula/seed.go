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

// jellyfinNetworkXML returns the contents of Jellyfin's network.xml.
// When JELLYFIN_PUBLISHED_URL is set in the environment, it is included as a
// <PublishedServerUrl> element so LAN clients discovering the server via UDP
// 7359 broadcast see the correct host-reachable URL instead of the container's
// internal IP. When unset, the element is omitted — Jellyfin falls back to its
// default advertising behavior, and the file stays byte-identical to prior
// versions (backwards compatible for existing installs).
//
// KnownProxies is included so nginx's Docker-network IP is trusted for
// X-Forwarded-For (required for remote-access auth to log real client IPs).
// PELICULA_KNOWN_PROXIES overrides the default Docker subnet CIDR.
func jellyfinNetworkXML() string {
	const header = `<?xml version="1.0" encoding="utf-8"?><NetworkConfiguration xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema"><BaseUrl>/jellyfin</BaseUrl>`
	const footer = `</NetworkConfiguration>`

	knownProxies := jellyfinKnownProxiesXML()

	var middle string
	if url := os.Getenv("JELLYFIN_PUBLISHED_URL"); url != "" {
		middle = "<PublishedServerUrl>" + xmlEscape(url) + "</PublishedServerUrl>"
	}
	return header + middle + knownProxies + footer
}

// jellyfinKnownProxiesXML returns the <KnownProxies> XML fragment.
// Uses PELICULA_KNOWN_PROXIES (comma-separated) when set; otherwise defaults
// to the Docker bridge subnet 172.16.0.0/12 (matches qBittorrent auth whitelist).
func jellyfinKnownProxiesXML() string {
	raw := os.Getenv("PELICULA_KNOWN_PROXIES")
	var entries []string
	if raw != "" {
		for _, e := range strings.Split(raw, ",") {
			e = strings.TrimSpace(e)
			if e != "" {
				entries = append(entries, "<string>"+xmlEscape(e)+"</string>")
			}
		}
	}
	if len(entries) == 0 {
		entries = []string{"<string>172.16.0.0/12</string>"}
	}
	return "<KnownProxies>" + strings.Join(entries, "") + "</KnownProxies>"
}

// enforceJellyfinNetwork patches Jellyfin's network.xml to re-assert
// KnownProxies when PELICULA_KNOWN_PROXIES is set in the environment.
// When the env var is unset, the function is a no-op — the seeded default
// is left in place and the user can modify it freely.
func enforceJellyfinNetwork(configDir string) error {
	if os.Getenv("PELICULA_KNOWN_PROXIES") == "" {
		return nil
	}
	path := filepath.Join(configDir, "jellyfin", "network.xml")
	knownProxies := jellyfinKnownProxiesXML()
	return patchXMLFile(path, []xmlPatch{
		{
			matchRe:        regexp.MustCompile(`<KnownProxies>.*?</KnownProxies>`),
			replacement:    knownProxies,
			insertBeforeRe: regexp.MustCompile(`</NetworkConfiguration>`),
			insertText:     knownProxies + "</NetworkConfiguration>",
		},
	})
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

// xmlPatch describes one targeted substitution in an XML config file.
//
// Behavior:
//   - If condRe is non-nil the patch is skipped unless condRe matches the
//     current file content (used for guarded patches like auth bypass).
//   - matchRe is used with ReplaceAllString to replace existing elements.
//   - If matchRe does not match and insertBeforeRe is non-nil, insertText is
//     inserted immediately before the first match of insertBeforeRe.
type xmlPatch struct {
	condRe         *regexp.Regexp // optional guard: skip if this does NOT match
	matchRe        *regexp.Regexp // replace all occurrences when present
	replacement    string         // replacement string for matchRe
	insertBeforeRe *regexp.Regexp // fallback insert anchor
	insertText     string         // text to insert before insertBeforeRe
}

// patchXMLFile reads path, applies patches in order, and writes the result only
// if the content changed. Returns nil if the file does not exist (treated as
// not-yet-created) or if no patches changed the content.
func patchXMLFile(path string, patches []xmlPatch) error {
	if _, err := os.Stat(path); err != nil {
		return nil // file doesn't exist yet — nothing to do
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	original := string(data)
	patched := original

	for _, p := range patches {
		// Honour guard condition.
		if p.condRe != nil && !p.condRe.MatchString(patched) {
			continue
		}
		if p.matchRe.MatchString(patched) {
			patched = p.matchRe.ReplaceAllString(patched, p.replacement)
		} else if p.insertBeforeRe != nil {
			patched = p.insertBeforeRe.ReplaceAllString(patched, p.insertText)
		}
	}

	if patched == original {
		return nil
	}
	return os.WriteFile(path, []byte(patched), 0644)
}

// enforceArrConfig patches an *arr config.xml to use External authentication
// (DisabledForLocalAddresses), enforce dark theme, disable analytics, and
// ensure the log level is not set to debug.
// This is idempotent and safe to call on every startup.
func enforceArrConfig(configPath string) error {
	return patchXMLFile(configPath, []xmlPatch{
		// Fix auth bypass — only when still set to Enabled.
		{
			condRe:      regexp.MustCompile(`<AuthenticationRequired>Enabled</AuthenticationRequired>`),
			matchRe:     regexp.MustCompile(`<AuthenticationMethod>[^<]*</AuthenticationMethod>`),
			replacement: "<AuthenticationMethod>External</AuthenticationMethod>",
		},
		{
			condRe:      regexp.MustCompile(`<AuthenticationRequired>Enabled</AuthenticationRequired>`),
			matchRe:     regexp.MustCompile(`<AuthenticationRequired>[^<]*</AuthenticationRequired>`),
			replacement: "<AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>",
		},
		// Enforce dark theme (only if element exists).
		{
			condRe:      regexp.MustCompile(`<Theme>`),
			matchRe:     regexp.MustCompile(`<Theme>[^<]*</Theme>`),
			replacement: "<Theme>dark</Theme>",
		},
		// Disable analytics telemetry; insert before </Config> if absent.
		{
			matchRe:        regexp.MustCompile(`<AnalyticsEnabled>[^<]*</AnalyticsEnabled>`),
			replacement:    "<AnalyticsEnabled>False</AnalyticsEnabled>",
			insertBeforeRe: regexp.MustCompile(`(\s*)</Config>\s*$`),
			insertText:     "${1}<AnalyticsEnabled>False</AnalyticsEnabled>${1}</Config>",
		},
		// Silence debug log level — replace debug→info only.
		{
			matchRe:     regexp.MustCompile(`(?i)<LogLevel>debug</LogLevel>`),
			replacement: "<LogLevel>info</LogLevel>",
		},
	})
}

// purgeSentryDirs removes Sentry crash-report cache directories for the *arr
// services. These are safe to delete at startup (the arrs recreate them on
// next crash); they must NOT be removed while the services are running.
func purgeSentryDirs(configDir string) {
	for _, svc := range []string{"sonarr", "radarr", "prowlarr"} {
		_ = os.RemoveAll(filepath.Join(configDir, svc, "Sentry"))
	}
}

// enforceJellyfinSystem patches Jellyfin's system.xml on every startup to:
//   - Disable client log uploads.
//   - Disable QuickConnect (6-digit-code auth bypass; unwanted for remote installs).
//   - Populate an empty ServerName from PELICULA_DASHBOARD_NAME or os.Hostname
//     on first boot (seed-only: regex only matches the empty element, so it's
//     a no-op once a non-empty value is present).
//
// The file lives inside the config subdirectory (linuxserver image maps
// CONFIG_DIR/jellyfin → /config; system.xml is at /config/config/).
// This is idempotent and safe to call on every startup.
func enforceJellyfinSystem(configDir string) error {
	path := filepath.Join(configDir, "jellyfin", "config", "system.xml")

	serverName := os.Getenv("PELICULA_DASHBOARD_NAME")
	if serverName == "" {
		if h, err := os.Hostname(); err == nil {
			serverName = h
		} else {
			serverName = "Pelicula"
		}
	}

	return patchXMLFile(path, []xmlPatch{
		// Disable client log uploads.
		{
			matchRe:        regexp.MustCompile(`<AllowClientLogUpload>[^<]*</AllowClientLogUpload>`),
			replacement:    "<AllowClientLogUpload>false</AllowClientLogUpload>",
			insertBeforeRe: regexp.MustCompile(`</ServerConfiguration>`),
			insertText:     "  <AllowClientLogUpload>false</AllowClientLogUpload>\n</ServerConfiguration>",
		},
		// Disable QuickConnect (present in Jellyfin 10.8+).
		{
			matchRe:        regexp.MustCompile(`<QuickConnectAvailable>[^<]*</QuickConnectAvailable>`),
			replacement:    "<QuickConnectAvailable>false</QuickConnectAvailable>",
			insertBeforeRe: regexp.MustCompile(`</ServerConfiguration>`),
			insertText:     "  <QuickConnectAvailable>false</QuickConnectAvailable>\n</ServerConfiguration>",
		},
		// Some older Jellyfin versions use EnableQuickConnect — patch if present,
		// do not insert (avoid duplicating the element across versions).
		{
			matchRe:     regexp.MustCompile(`<EnableQuickConnect>[^<]*</EnableQuickConnect>`),
			replacement: "<EnableQuickConnect>false</EnableQuickConnect>",
		},
		// Seed ServerName on first boot only — regex matches the empty self-closing
		// or empty-body forms; once set, the regex no longer matches.
		{
			matchRe:     regexp.MustCompile(`<ServerName\s*/>|<ServerName>\s*</ServerName>`),
			replacement: "<ServerName>" + xmlEscape(serverName) + "</ServerName>",
		},
	})
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

	// Enforce auth bypass and privacy settings on existing arr configs
	// (*arr apps rewrite config.xml on first boot, re-enabling auth)
	for _, svc := range []string{"sonarr", "radarr", "prowlarr"} {
		if err := enforceArrConfig(filepath.Join(configDir, svc, "config.xml")); err != nil {
			return fmt.Errorf("enforceArrConfig %s: %w", svc, err)
		}
	}

	// Purge Sentry crash-report cache dirs (safe at startup; arrs recreate on next crash)
	purgeSentryDirs(configDir)

	// Disable Jellyfin client log uploads (system.xml; no-op if Jellyfin hasn't run yet)
	if err := enforceJellyfinSystem(configDir); err != nil {
		return fmt.Errorf("enforceJellyfinSystem: %w", err)
	}

	// Jellyfin network.xml
	if err := seedConfig(
		filepath.Join(configDir, "jellyfin", "network.xml"),
		jellyfinNetworkXML(),
	); err != nil {
		return fmt.Errorf("seed jellyfin network.xml: %w", err)
	}

	// Re-assert env-driven KnownProxies every boot when env is set.
	if err := enforceJellyfinNetwork(configDir); err != nil {
		return fmt.Errorf("enforceJellyfinNetwork: %w", err)
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

	if err := seedConfig(
		filepath.Join(jellyfinConfigDir, "dlna.xml"),
		jellyfinDlnaXML,
	); err != nil {
		return fmt.Errorf("seed jellyfin dlna.xml: %w", err)
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

var jellyfinDlnaXML = `<DlnaOptions xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema"><EnableServer>false</EnableServer><EnablePlayTo>false</EnablePlayTo></DlnaOptions>`

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

// resetProwlarr resets Prowlarr's config.xml, preserving the API key and
// the indexer database. The database is left in place; only config.xml is
// rewritten.
func resetProwlarr(configDir, apiKey string) error {
	info("Resetting Prowlarr config (keeping indexer database)...")
	keyXML := ""
	if apiKey != "" {
		keyXML = "<ApiKey>" + xmlEscape(apiKey) + "</ApiKey>"
	}
	content := fmt.Sprintf(
		"<Config><UrlBase>/prowlarr</UrlBase>%s<AuthenticationMethod>External</AuthenticationMethod><AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired></Config>",
		keyXML,
	)
	return os.WriteFile(filepath.Join(configDir, "prowlarr", "config.xml"), []byte(content), 0644)
}

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
		[]byte(jellyfinNetworkXML()),
		0644,
	); err != nil {
		return err
	}
	if err := os.WriteFile(
		filepath.Join(jellyfinDir, "config", "branding.xml"),
		[]byte(jellyfinBrandingXML),
		0644,
	); err != nil {
		return err
	}
	return os.WriteFile(
		filepath.Join(jellyfinDir, "config", "dlna.xml"),
		[]byte(jellyfinDlnaXML),
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

// resetProculaJobs clears the Procula job queue (SQLite database and legacy JSON directory).
func resetProculaJobs(configDir string) error {
	info("Clearing Procula job queue...")
	proculaDir := filepath.Join(configDir, "procula")
	// Remove SQLite database and WAL files (schema is auto-recreated on next startup)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(filepath.Join(proculaDir, "procula.db"+suffix))
	}
	// Remove legacy JSON jobs directory
	jobsDir := filepath.Join(proculaDir, "jobs")
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
