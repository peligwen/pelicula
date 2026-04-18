// Package config handles environment loading, URL resolution, and path constants
// for the pelicula-api service. It has no internal dependencies.
package config

import (
	"os"
	"strconv"
)

// EnvOr returns the value of the environment variable key, or fallback if unset/empty.
func EnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// URLs holds the resolved base URLs for all downstream services.
// All values are read from environment variables with sensible Docker-internal defaults.
type URLs struct {
	Sonarr   string
	Radarr   string
	Prowlarr string
	Bazarr   string
	Jellyfin string
	Procula  string
	Gluetun  string
	QBT      string
	Apprise  string
	// PeliculaAPI is this service's own base URL (used by autowire webhook registration).
	PeliculaAPI string
}

// LoadURLs reads all service base URLs from the environment.
func LoadURLs() URLs {
	return URLs{
		Sonarr:      EnvOr("SONARR_URL", "http://sonarr:8989/sonarr"),
		Radarr:      EnvOr("RADARR_URL", "http://radarr:7878/radarr"),
		Prowlarr:    EnvOr("PROWLARR_URL", "http://gluetun:9696/prowlarr"),
		Bazarr:      EnvOr("BAZARR_URL", "http://bazarr:6767/bazarr"),
		Jellyfin:    EnvOr("JELLYFIN_URL", "http://jellyfin:8096/jellyfin"),
		Procula:     EnvOr("PROCULA_URL", "http://procula:8282"),
		Gluetun:     EnvOr("GLUETUN_CONTROL_URL", "http://gluetun:8000"),
		QBT:         EnvOr("QBITTORRENT_URL", "http://gluetun:8080"),
		Apprise:     EnvOr("APPRISE_URL", "http://apprise:8000/notify"),
		PeliculaAPI: EnvOr("PELICULA_API_URL", "http://pelicula-api:8181"),
	}
}

// Config holds all environment-derived configuration for pelicula-api.
// Fields are grouped by concern; all values are populated by Load().
// Callers should treat this as read-only after construction.
type Config struct {
	// --- Service URLs ---
	URLs URLs

	// --- VPN / connectivity ---
	// WireguardPrivateKey is the WireGuard private key; non-empty means VPN is configured.
	WireguardPrivateKey string
	// GluetunProxyURL is the HTTP proxy endpoint exposed by Gluetun for speed tests.
	GluetunProxyURL string
	// GluetunHTTPUser and GluetunHTTPPass are Basic Auth credentials for the Gluetun control API.
	GluetunHTTPUser string
	GluetunHTTPPass string

	// --- Auth / security ---
	// WebhookSecret is the shared secret used to verify *arr import webhooks.
	WebhookSecret string
	// ProculaAPIKey is the API key for Procula's HTTP API.
	ProculaAPIKey string
	// JellyfinAPIKey is the Jellyfin admin API key.
	JellyfinAPIKey string

	// --- Feature flags ---
	// SetupMode indicates the server should only serve setup endpoints.
	SetupMode bool
	// OpenRegistration enables unauthenticated user registration.
	OpenRegistration bool
	// SeedingRemoveOnComplete removes the torrent from qBittorrent after *arr import.
	SeedingRemoveOnComplete bool

	// --- Path / directory configuration ---
	ConfigDir       string
	LibraryDir      string
	ImportSourceDir string
	ProjectName     string

	// --- Media / acquisition settings ---
	SubLangs                string
	AudioLang               string
	JellyfinLibraryLimit    int
	RequestsRadarrProfileID int
	RequestsRadarrRoot      string
	RequestsSonarrProfileID int
	RequestsSonarrRoot      string

	// --- Docker ---
	DockerHost string

	// --- Host detection (setup wizard / settings reset) ---
	HostPlatform   string
	HostTZ         string
	HostPUID       string
	HostPGID       string
	HostConfigDir  string
	HostLibraryDir string
	HostWorkDir    string
	HostLANURL     string
}

// Load reads all configuration from the environment, applying the same
// defaults that were previously scattered across the cmd/pelicula-api package.
func Load() *Config {
	return &Config{
		URLs: LoadURLs(),

		WireguardPrivateKey: os.Getenv("WIREGUARD_PRIVATE_KEY"),
		GluetunProxyURL:     EnvOr("GLUETUN_PROXY_URL", "http://gluetun:8888"),
		GluetunHTTPUser:     EnvOr("GLUETUN_HTTP_USER", "pelicula"),
		GluetunHTTPPass:     EnvOr("GLUETUN_HTTP_PASS", ""),

		WebhookSecret:  os.Getenv("WEBHOOK_SECRET"),
		ProculaAPIKey:  os.Getenv("PROCULA_API_KEY"),
		JellyfinAPIKey: os.Getenv("JELLYFIN_API_KEY"),

		SetupMode:               os.Getenv("SETUP_MODE") == "true",
		OpenRegistration:        os.Getenv("PELICULA_OPEN_REGISTRATION") == "true",
		SeedingRemoveOnComplete: os.Getenv("SEEDING_REMOVE_ON_COMPLETE") == "true",

		ConfigDir:       EnvOr("CONFIG_DIR", "/config"),
		LibraryDir:      EnvOr("LIBRARY_DIR", "/media"),
		ImportSourceDir: os.Getenv("IMPORT_SOURCE_DIR"),
		ProjectName:     EnvOr("PELICULA_PROJECT_NAME", "pelicula"),

		SubLangs:  os.Getenv("PELICULA_SUB_LANGS"),
		AudioLang: EnvOr("PELICULA_AUDIO_LANG", "eng"),

		JellyfinLibraryLimit:    envIntOr("JELLYFIN_LIBRARY_LIMIT", 5000),
		RequestsRadarrProfileID: envIntOr("REQUESTS_RADARR_PROFILE_ID", 0),
		RequestsRadarrRoot:      os.Getenv("REQUESTS_RADARR_ROOT"),
		RequestsSonarrProfileID: envIntOr("REQUESTS_SONARR_PROFILE_ID", 0),
		RequestsSonarrRoot:      os.Getenv("REQUESTS_SONARR_ROOT"),

		DockerHost: EnvOr("DOCKER_HOST", "http://docker-proxy:2375"),

		HostPlatform:   EnvOr("HOST_PLATFORM", "linux"),
		HostTZ:         EnvOr("HOST_TZ", "America/New_York"),
		HostPUID:       EnvOr("HOST_PUID", "1000"),
		HostPGID:       EnvOr("HOST_PGID", "1000"),
		HostConfigDir:  EnvOr("HOST_CONFIG_DIR", "./config"),
		HostLibraryDir: EnvOr("HOST_LIBRARY_DIR", "~/media"),
		HostWorkDir:    EnvOr("HOST_WORK_DIR", "~/media"),
		HostLANURL:     os.Getenv("HOST_LAN_URL"),
	}
}

// envIntOr reads an environment variable as an integer.
// Returns fallback if the variable is unset, empty, or not a valid integer.
func envIntOr(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
