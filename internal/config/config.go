// Package config loads the server's runtime configuration from the
// environment. The CLI (cmd/pelicula) owns .env and compose; the server only
// ever reads what compose hands it. Nothing here writes a file.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Config is the server's complete runtime configuration.
type Config struct {
	Listen    string // address the API listens on, default ":8181"
	ConfigDir string // mounted config root, default "/config"
	DBPath    string // SQLite file, default ConfigDir/pelicula/pelicula.db

	// Service URLs inside the compose network. Prowlarr and qBittorrent run in
	// gluetun's network namespace, so they are addressed as gluetun:<port>.
	SonarrURL   string
	RadarrURL   string
	ProwlarrURL string
	QBTURL      string
	JellyfinURL string
	GluetunURL  string
	SelfURL     string // how *arr reaches this server for webhooks

	WebhookSecret string

	JellyfinAdminUser     string
	JellyfinAdminPassword string

	GluetunUser string
	GluetunPass string
	VPNEnabled  bool

	MoviesPath string // /media/movies inside containers
	TVPath     string // /media/tv inside containers

	ServerCountries string
	HostConfigDir   string // display only
	HostLibraryDir  string // display only
	HostWorkDir     string // display only
	TZ              string
	Version         string
}

// FromEnv builds a Config from the process environment. version is stamped
// by the binary (ldflags) and passed through unchanged.
func FromEnv(version string) Config {
	configDir := envOr("CONFIG_DIR", "/config")
	return Config{
		Listen:                envOr("PELICULA_LISTEN", ":8181"),
		ConfigDir:             configDir,
		DBPath:                envOr("PELICULA_DB", filepath.Join(configDir, "pelicula", "pelicula.db")),
		SonarrURL:             envOr("SONARR_URL", "http://sonarr:8989/sonarr"),
		RadarrURL:             envOr("RADARR_URL", "http://radarr:7878/radarr"),
		ProwlarrURL:           envOr("PROWLARR_URL", "http://gluetun:9696/prowlarr"),
		QBTURL:                envOr("QBITTORRENT_URL", "http://gluetun:8080"),
		JellyfinURL:           envOr("JELLYFIN_URL", "http://jellyfin:8096/jellyfin"),
		GluetunURL:            envOr("GLUETUN_CONTROL_URL", "http://gluetun:8000"),
		SelfURL:               envOr("PELICULA_URL", "http://pelicula:8181"),
		WebhookSecret:         os.Getenv("WEBHOOK_SECRET"),
		JellyfinAdminUser:     envOr("JELLYFIN_ADMIN_USER", "admin"),
		JellyfinAdminPassword: os.Getenv("JELLYFIN_PASSWORD"),
		GluetunUser:           envOr("GLUETUN_HTTP_USER", "pelicula"),
		GluetunPass:           os.Getenv("GLUETUN_HTTP_PASS"),
		VPNEnabled:            strings.EqualFold(os.Getenv("PELICULA_VPN"), "true"),
		MoviesPath:            envOr("MOVIES_PATH", "/media/movies"),
		TVPath:                envOr("TV_PATH", "/media/tv"),
		ServerCountries:       os.Getenv("SERVER_COUNTRIES"),
		HostConfigDir:         os.Getenv("HOST_CONFIG_DIR"),
		HostLibraryDir:        os.Getenv("HOST_LIBRARY_DIR"),
		HostWorkDir:           os.Getenv("HOST_WORK_DIR"),
		TZ:                    envOr("TZ", "UTC"),
		Version:               version,
	}
}

var apiKeyRe = regexp.MustCompile(`<ApiKey>([^<]+)</ApiKey>`)

// ArrAPIKey reads the API key from a mounted *arr config.xml
// (ConfigDir/<service>/config.xml). service is "sonarr", "radarr" or
// "prowlarr". The *arr apps write this file on first boot, so callers should
// retry until it appears.
func (c Config) ArrAPIKey(service string) (string, error) {
	path := filepath.Join(c.ConfigDir, service, "config.xml")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	m := apiKeyRe.FindSubmatch(data)
	if m == nil {
		return "", fmt.Errorf("%s: no <ApiKey> in %s", service, path)
	}
	return strings.TrimSpace(string(m[1])), nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
