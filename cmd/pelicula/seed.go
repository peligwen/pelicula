package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// dockerSubnet is the Docker bridge range trusted by qBittorrent (WebUI
// subnet whitelist) and Jellyfin (KnownProxies, so nginx's X-Forwarded-For is
// honoured).
const dockerSubnet = "172.16.0.0/12"

// arrServices maps each *arr service to its URL base behind nginx.
var arrServices = []struct{ name, urlBase string }{
	{"sonarr", "/sonarr"},
	{"radarr", "/radarr"},
	{"prowlarr", "/prowlarr"},
}

// xmlEscape escapes the characters that are special in XML text.
func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

// seedConfig writes content to file only if the file does not exist yet.
func seedConfig(file, content string) error {
	if fileExists(file) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(file), err)
	}
	return os.WriteFile(file, []byte(content), 0644)
}

// setXMLElement sets <tag>value</tag> in doc. An existing element (including
// the self-closing form) is replaced; otherwise the element is inserted just
// before the last occurrence of rootClose. Applying it twice is a no-op.
func setXMLElement(doc, tag, value, rootClose string) string {
	elem := "<" + tag + ">" + xmlEscape(value) + "</" + tag + ">"
	t := regexp.QuoteMeta(tag)
	re := regexp.MustCompile(`<` + t + `>[^<]*</` + t + `>|<` + t + `\s*/>`)
	if re.MatchString(doc) {
		return re.ReplaceAllLiteralString(doc, elem)
	}
	if i := strings.LastIndex(doc, rootClose); i >= 0 {
		return doc[:i] + "  " + elem + "\n" + doc[i:]
	}
	return doc
}

// patchFile applies fn to the file's content and writes the result only when
// it changed. A missing file is not an error: the service has not created it
// yet, and the seed step will.
func patchFile(path string, fn func(string) string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	patched := fn(string(data))
	if patched == string(data) {
		return nil
	}
	return os.WriteFile(path, []byte(patched), 0644)
}

// arrConfigXML is the config.xml seeded for an *arr service: UrlBase for the
// nginx sub-path, and External authentication with local addresses exempt so
// Pelicula's own login (nginx auth_request) is the only gate while the
// services can still call each other over the Docker network. Analytics are
// off.
func arrConfigXML(urlBase string) string {
	return "<Config><UrlBase>" + urlBase + "</UrlBase>" +
		"<AuthenticationMethod>External</AuthenticationMethod>" +
		"<AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>" +
		"<AnalyticsEnabled>False</AnalyticsEnabled></Config>"
}

// enforceArrConfig re-asserts UrlBase and the authentication settings in an
// *arr config.xml (the apps rewrite the file and can re-enable their own
// login) and disables analytics. Idempotent; a missing file is skipped.
func enforceArrConfig(path, urlBase string) error {
	return patchFile(path, func(doc string) string {
		doc = setXMLElement(doc, "UrlBase", urlBase, "</Config>")
		doc = setXMLElement(doc, "AuthenticationMethod", "External", "</Config>")
		doc = setXMLElement(doc, "AuthenticationRequired", "DisabledForLocalAddresses", "</Config>")
		return setXMLElement(doc, "AnalyticsEnabled", "False", "</Config>")
	})
}

// jellyfinNetworkXML is the seeded network.xml: BaseUrl so Jellyfin serves
// under nginx's /jellyfin, and the Docker subnet as a known proxy.
func jellyfinNetworkXML() string {
	return `<?xml version="1.0" encoding="utf-8"?>` +
		`<NetworkConfiguration xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema">` +
		`<BaseUrl>/jellyfin</BaseUrl>` +
		`<KnownProxies><string>` + dockerSubnet + `</string></KnownProxies>` +
		`</NetworkConfiguration>`
}

// jellyfinNetworkPaths lists where network.xml may be read from. The
// linuxserver image keeps Jellyfin's configuration in <config>/config/, but
// the file has historically also been placed at the config root; seeding both
// is harmless (Jellyfin reads one, ignores the other) and guarantees BaseUrl.
func jellyfinNetworkPaths(configDir string) []string {
	return []string{
		filepath.Join(configDir, "jellyfin", "config", "network.xml"),
		filepath.Join(configDir, "jellyfin", "network.xml"),
	}
}

// qbtConf is the seeded qBittorrent.conf: no authentication for requests from
// Docker subnets (Pelicula's server and the *arr apps reach the WebUI that
// way), queueing limits, and peer discovery and RSS off.
var qbtConf = "[Preferences]\n" +
	`WebUI\AuthSubnetWhitelistEnabled=true` + "\n" +
	`WebUI\AuthSubnetWhitelist=` + dockerSubnet + "\n" +
	`WebUI\LocalHostAuth=false` + "\n" +
	`WebUI\CSRFProtection=false` + "\n" +
	`Queueing\QueueingEnabled=true` + "\n" +
	`Queueing\MaxActiveDownloads=3` + "\n" +
	`Queueing\MaxActiveTorrents=8` + "\n" +
	`Queueing\MaxActiveUploads=3` + "\n" +
	`Queueing\IgnoreSlowTorrentsForQueueing=true` + "\n" +
	`Bittorrent\DHT=false` + "\n" +
	`Bittorrent\PeX=false` + "\n" +
	`Bittorrent\LSD=false` + "\n" +
	`RSS\AutoDownloader\enabled=false` + "\n" +
	`RSS\Session\Enabled=false` + "\n" +
	"\n" +
	"[BitTorrent]\n" +
	`Session\DefaultSavePath=/downloads/` + "\n" +
	`Session\TempPathEnabled=true` + "\n" +
	`Session\TempPath=/downloads/incomplete/`

const qbtCategories = `{"radarr":{"save_path":"/downloads/radarr/"},"tv-sonarr":{"save_path":"/downloads/tv-sonarr/"}}`

// patchINIKey sets key=value in an INI blob. An existing key (any value) is
// replaced in place; otherwise the line is appended to the end of [section],
// creating the section when needed. Applying it twice is a no-op.
func patchINIKey(content []byte, section, key, value string) []byte {
	line := key + "=" + value

	keyRe := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `=.*$`)
	if keyRe.Match(content) {
		return keyRe.ReplaceAllLiteral(content, []byte(line))
	}

	text := string(content)
	header := "[" + section + "]"
	lines := strings.Split(text, "\n")

	inSection, insertBefore := false, -1
	for i, l := range lines {
		stripped := strings.TrimSpace(l)
		if stripped == header {
			inSection = true
			continue
		}
		if inSection && strings.HasPrefix(stripped, "[") {
			insertBefore = i
			break
		}
	}

	switch {
	case !inSection:
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		return []byte(text + "\n" + header + "\n" + line + "\n")
	case insertBefore == -1:
		// Section runs to EOF: insert after its last non-empty line.
		last := len(lines) - 1
		for last > 0 && strings.TrimSpace(lines[last]) == "" {
			last--
		}
		out := append(append(append([]string{}, lines[:last+1]...), line), lines[last+1:]...)
		return []byte(strings.Join(out, "\n"))
	default:
		out := append(append(append([]string{}, lines[:insertBefore]...), line), lines[insertBefore:]...)
		return []byte(strings.Join(out, "\n"))
	}
}

// enforceQBittorrentConf re-asserts RSS off and DHT/PeX/LSD off in
// qBittorrent.conf (qBittorrent rewrites the file and the WebUI can flip
// them). Idempotent; a missing file is skipped.
func enforceQBittorrentConf(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	patched := data
	for _, key := range []string{
		`RSS\AutoDownloader\enabled`,
		`RSS\Session\Enabled`,
		`Bittorrent\DHT`,
		`Bittorrent\PeX`,
		`Bittorrent\LSD`,
	} {
		patched = patchINIKey(patched, "Preferences", key, "false")
	}
	if bytes.Equal(patched, data) {
		return nil
	}
	return os.WriteFile(path, patched, 0644)
}

// SeedAllConfigs seeds every service's config under configDir (files that
// already exist are left alone) and re-enforces the security-relevant
// settings in the ones that do. Safe to run on every `up`.
func SeedAllConfigs(configDir string) error {
	for _, s := range arrServices {
		path := filepath.Join(configDir, s.name, "config.xml")
		if err := seedConfig(path, arrConfigXML(s.urlBase)); err != nil {
			return fmt.Errorf("seed %s: %w", s.name, err)
		}
		if err := enforceArrConfig(path, s.urlBase); err != nil {
			return fmt.Errorf("enforce %s config: %w", s.name, err)
		}
	}

	for _, path := range jellyfinNetworkPaths(configDir) {
		if err := seedConfig(path, jellyfinNetworkXML()); err != nil {
			return fmt.Errorf("seed jellyfin network.xml: %w", err)
		}
		err := patchFile(path, func(doc string) string {
			return setXMLElement(doc, "BaseUrl", "/jellyfin", "</NetworkConfiguration>")
		})
		if err != nil {
			return fmt.Errorf("enforce jellyfin BaseUrl: %w", err)
		}
	}

	qbtDir := filepath.Join(configDir, "qbittorrent", "qBittorrent")
	if err := seedConfig(filepath.Join(qbtDir, "qBittorrent.conf"), qbtConf); err != nil {
		return fmt.Errorf("seed qBittorrent.conf: %w", err)
	}
	if err := seedConfig(filepath.Join(qbtDir, "categories.json"), qbtCategories); err != nil {
		return fmt.Errorf("seed qBittorrent categories.json: %w", err)
	}
	if err := enforceQBittorrentConf(filepath.Join(qbtDir, "qBittorrent.conf")); err != nil {
		return fmt.Errorf("enforce qBittorrent.conf: %w", err)
	}
	return nil
}
