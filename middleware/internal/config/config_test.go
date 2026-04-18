package config_test

import (
	"testing"

	"pelicula-api/internal/config"
)

func TestLoad_Defaults(t *testing.T) {
	// Unset every env var we own so defaults are exercised.
	// t.Setenv restores the original value after the test.
	vars := []string{
		"SONARR_URL", "RADARR_URL", "PROWLARR_URL", "BAZARR_URL",
		"JELLYFIN_URL", "PROCULA_URL", "GLUETUN_CONTROL_URL",
		"QBITTORRENT_URL", "APPRISE_URL", "PELICULA_API_URL",
		"WIREGUARD_PRIVATE_KEY", "GLUETUN_PROXY_URL",
		"GLUETUN_HTTP_USER", "GLUETUN_HTTP_PASS",
		"WEBHOOK_SECRET", "PROCULA_API_KEY", "JELLYFIN_API_KEY",
		"SETUP_MODE", "PELICULA_OPEN_REGISTRATION", "SEEDING_REMOVE_ON_COMPLETE",
		"CONFIG_DIR", "LIBRARY_DIR", "IMPORT_SOURCE_DIR", "PELICULA_PROJECT_NAME",
		"PELICULA_SUB_LANGS", "PELICULA_AUDIO_LANG",
		"JELLYFIN_LIBRARY_LIMIT",
		"REQUESTS_RADARR_PROFILE_ID", "REQUESTS_RADARR_ROOT",
		"REQUESTS_SONARR_PROFILE_ID", "REQUESTS_SONARR_ROOT",
		"DOCKER_HOST",
		"HOST_PLATFORM", "HOST_TZ", "HOST_PUID", "HOST_PGID",
		"HOST_CONFIG_DIR", "HOST_LIBRARY_DIR", "HOST_WORK_DIR", "HOST_LAN_URL",
	}
	for _, k := range vars {
		t.Setenv(k, "")
	}

	cfg := config.Load()

	cases := []struct {
		name string
		got  string
		want string
	}{
		// URLs
		{"URLs.Sonarr", cfg.URLs.Sonarr, "http://sonarr:8989/sonarr"},
		{"URLs.Radarr", cfg.URLs.Radarr, "http://radarr:7878/radarr"},
		{"URLs.Prowlarr", cfg.URLs.Prowlarr, "http://gluetun:9696/prowlarr"},
		{"URLs.Bazarr", cfg.URLs.Bazarr, "http://bazarr:6767/bazarr"},
		{"URLs.Jellyfin", cfg.URLs.Jellyfin, "http://jellyfin:8096/jellyfin"},
		{"URLs.Procula", cfg.URLs.Procula, "http://procula:8282"},
		{"URLs.Gluetun", cfg.URLs.Gluetun, "http://gluetun:8000"},
		{"URLs.QBT", cfg.URLs.QBT, "http://gluetun:8080"},
		{"URLs.Apprise", cfg.URLs.Apprise, "http://apprise:8000/notify"},
		{"URLs.PeliculaAPI", cfg.URLs.PeliculaAPI, "http://pelicula-api:8181"},
		// VPN / connectivity
		{"WireguardPrivateKey", cfg.WireguardPrivateKey, ""},
		{"GluetunProxyURL", cfg.GluetunProxyURL, "http://gluetun:8888"},
		{"GluetunHTTPUser", cfg.GluetunHTTPUser, "pelicula"},
		{"GluetunHTTPPass", cfg.GluetunHTTPPass, ""},
		// Auth / security
		{"WebhookSecret", cfg.WebhookSecret, ""},
		{"ProculaAPIKey", cfg.ProculaAPIKey, ""},
		{"JellyfinAPIKey", cfg.JellyfinAPIKey, ""},
		// Paths
		{"ConfigDir", cfg.ConfigDir, "/config"},
		{"LibraryDir", cfg.LibraryDir, "/media"},
		{"ImportSourceDir", cfg.ImportSourceDir, ""},
		{"ProjectName", cfg.ProjectName, "pelicula"},
		// Media settings
		{"SubLangs", cfg.SubLangs, ""},
		{"AudioLang", cfg.AudioLang, "eng"},
		// Request roots
		{"RequestsRadarrRoot", cfg.RequestsRadarrRoot, ""},
		{"RequestsSonarrRoot", cfg.RequestsSonarrRoot, ""},
		// Docker
		{"DockerHost", cfg.DockerHost, "http://docker-proxy:2375"},
		// Host detection
		{"HostPlatform", cfg.HostPlatform, "linux"},
		{"HostTZ", cfg.HostTZ, "America/New_York"},
		{"HostPUID", cfg.HostPUID, "1000"},
		{"HostPGID", cfg.HostPGID, "1000"},
		{"HostConfigDir", cfg.HostConfigDir, "./config"},
		{"HostLibraryDir", cfg.HostLibraryDir, "~/media"},
		{"HostWorkDir", cfg.HostWorkDir, "~/media"},
		{"HostLANURL", cfg.HostLANURL, ""},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	// Boolean defaults
	if cfg.SetupMode {
		t.Error("SetupMode: want false by default")
	}
	if cfg.OpenRegistration {
		t.Error("OpenRegistration: want false by default")
	}
	if cfg.SeedingRemoveOnComplete {
		t.Error("SeedingRemoveOnComplete: want false by default")
	}

	// Integer defaults
	if cfg.JellyfinLibraryLimit != 5000 {
		t.Errorf("JellyfinLibraryLimit: got %d, want 5000", cfg.JellyfinLibraryLimit)
	}
	if cfg.RequestsRadarrProfileID != 0 {
		t.Errorf("RequestsRadarrProfileID: got %d, want 0", cfg.RequestsRadarrProfileID)
	}
	if cfg.RequestsSonarrProfileID != 0 {
		t.Errorf("RequestsSonarrProfileID: got %d, want 0", cfg.RequestsSonarrProfileID)
	}
}

func TestLoad_Overrides(t *testing.T) {
	t.Setenv("SONARR_URL", "http://custom-sonarr:1234/sonarr")
	t.Setenv("RADARR_URL", "http://custom-radarr:5678/radarr")
	t.Setenv("PROWLARR_URL", "http://custom-prowlarr:9999/prowlarr")
	t.Setenv("BAZARR_URL", "http://custom-bazarr:6767/bazarr")
	t.Setenv("JELLYFIN_URL", "http://custom-jellyfin:8096/jellyfin")
	t.Setenv("PROCULA_URL", "http://custom-procula:8282")
	t.Setenv("GLUETUN_CONTROL_URL", "http://custom-gluetun:8000")
	t.Setenv("QBITTORRENT_URL", "http://custom-qbt:8080")
	t.Setenv("APPRISE_URL", "http://custom-apprise:9000/notify")
	t.Setenv("PELICULA_API_URL", "http://custom-api:8181")
	t.Setenv("WIREGUARD_PRIVATE_KEY", "supersecretkey")
	t.Setenv("GLUETUN_PROXY_URL", "http://custom-proxy:9999")
	t.Setenv("GLUETUN_HTTP_USER", "admin")
	t.Setenv("GLUETUN_HTTP_PASS", "hunter2")
	t.Setenv("WEBHOOK_SECRET", "mywebhooksecret")
	t.Setenv("PROCULA_API_KEY", "proculakey123")
	t.Setenv("JELLYFIN_API_KEY", "jellykey456")
	t.Setenv("SETUP_MODE", "true")
	t.Setenv("PELICULA_OPEN_REGISTRATION", "true")
	t.Setenv("SEEDING_REMOVE_ON_COMPLETE", "true")
	t.Setenv("CONFIG_DIR", "/custom/config")
	t.Setenv("LIBRARY_DIR", "/custom/media")
	t.Setenv("IMPORT_SOURCE_DIR", "/custom/import")
	t.Setenv("PELICULA_PROJECT_NAME", "mypelicula")
	t.Setenv("PELICULA_SUB_LANGS", "en,es,fr")
	t.Setenv("PELICULA_AUDIO_LANG", "spa")
	t.Setenv("JELLYFIN_LIBRARY_LIMIT", "9999")
	t.Setenv("REQUESTS_RADARR_PROFILE_ID", "7")
	t.Setenv("REQUESTS_RADARR_ROOT", "/media/movies")
	t.Setenv("REQUESTS_SONARR_PROFILE_ID", "3")
	t.Setenv("REQUESTS_SONARR_ROOT", "/media/tv")
	t.Setenv("DOCKER_HOST", "http://custom-docker:2375")
	t.Setenv("HOST_PLATFORM", "darwin")
	t.Setenv("HOST_TZ", "Europe/London")
	t.Setenv("HOST_PUID", "501")
	t.Setenv("HOST_PGID", "20")
	t.Setenv("HOST_CONFIG_DIR", "/Users/test/config")
	t.Setenv("HOST_LIBRARY_DIR", "/Users/test/media")
	t.Setenv("HOST_WORK_DIR", "/Users/test/work")
	t.Setenv("HOST_LAN_URL", "http://192.168.1.100:7354")

	cfg := config.Load()

	strCases := []struct {
		name string
		got  string
		want string
	}{
		{"URLs.Sonarr", cfg.URLs.Sonarr, "http://custom-sonarr:1234/sonarr"},
		{"URLs.Radarr", cfg.URLs.Radarr, "http://custom-radarr:5678/radarr"},
		{"URLs.Prowlarr", cfg.URLs.Prowlarr, "http://custom-prowlarr:9999/prowlarr"},
		{"URLs.Bazarr", cfg.URLs.Bazarr, "http://custom-bazarr:6767/bazarr"},
		{"URLs.Jellyfin", cfg.URLs.Jellyfin, "http://custom-jellyfin:8096/jellyfin"},
		{"URLs.Procula", cfg.URLs.Procula, "http://custom-procula:8282"},
		{"URLs.Gluetun", cfg.URLs.Gluetun, "http://custom-gluetun:8000"},
		{"URLs.QBT", cfg.URLs.QBT, "http://custom-qbt:8080"},
		{"URLs.Apprise", cfg.URLs.Apprise, "http://custom-apprise:9000/notify"},
		{"URLs.PeliculaAPI", cfg.URLs.PeliculaAPI, "http://custom-api:8181"},
		{"WireguardPrivateKey", cfg.WireguardPrivateKey, "supersecretkey"},
		{"GluetunProxyURL", cfg.GluetunProxyURL, "http://custom-proxy:9999"},
		{"GluetunHTTPUser", cfg.GluetunHTTPUser, "admin"},
		{"GluetunHTTPPass", cfg.GluetunHTTPPass, "hunter2"},
		{"WebhookSecret", cfg.WebhookSecret, "mywebhooksecret"},
		{"ProculaAPIKey", cfg.ProculaAPIKey, "proculakey123"},
		{"JellyfinAPIKey", cfg.JellyfinAPIKey, "jellykey456"},
		{"ConfigDir", cfg.ConfigDir, "/custom/config"},
		{"LibraryDir", cfg.LibraryDir, "/custom/media"},
		{"ImportSourceDir", cfg.ImportSourceDir, "/custom/import"},
		{"ProjectName", cfg.ProjectName, "mypelicula"},
		{"SubLangs", cfg.SubLangs, "en,es,fr"},
		{"AudioLang", cfg.AudioLang, "spa"},
		{"RequestsRadarrRoot", cfg.RequestsRadarrRoot, "/media/movies"},
		{"RequestsSonarrRoot", cfg.RequestsSonarrRoot, "/media/tv"},
		{"DockerHost", cfg.DockerHost, "http://custom-docker:2375"},
		{"HostPlatform", cfg.HostPlatform, "darwin"},
		{"HostTZ", cfg.HostTZ, "Europe/London"},
		{"HostPUID", cfg.HostPUID, "501"},
		{"HostPGID", cfg.HostPGID, "20"},
		{"HostConfigDir", cfg.HostConfigDir, "/Users/test/config"},
		{"HostLibraryDir", cfg.HostLibraryDir, "/Users/test/media"},
		{"HostWorkDir", cfg.HostWorkDir, "/Users/test/work"},
		{"HostLANURL", cfg.HostLANURL, "http://192.168.1.100:7354"},
	}
	for _, tc := range strCases {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	if !cfg.SetupMode {
		t.Error("SetupMode: want true when SETUP_MODE=true")
	}
	if !cfg.OpenRegistration {
		t.Error("OpenRegistration: want true when PELICULA_OPEN_REGISTRATION=true")
	}
	if !cfg.SeedingRemoveOnComplete {
		t.Error("SeedingRemoveOnComplete: want true when SEEDING_REMOVE_ON_COMPLETE=true")
	}
	if cfg.JellyfinLibraryLimit != 9999 {
		t.Errorf("JellyfinLibraryLimit: got %d, want 9999", cfg.JellyfinLibraryLimit)
	}
	if cfg.RequestsRadarrProfileID != 7 {
		t.Errorf("RequestsRadarrProfileID: got %d, want 7", cfg.RequestsRadarrProfileID)
	}
	if cfg.RequestsSonarrProfileID != 3 {
		t.Errorf("RequestsSonarrProfileID: got %d, want 3", cfg.RequestsSonarrProfileID)
	}
}

func TestLoad_IntFallbackOnInvalidValue(t *testing.T) {
	t.Setenv("JELLYFIN_LIBRARY_LIMIT", "not-a-number")
	t.Setenv("REQUESTS_RADARR_PROFILE_ID", "")
	t.Setenv("REQUESTS_SONARR_PROFILE_ID", "bogus")

	cfg := config.Load()

	if cfg.JellyfinLibraryLimit != 5000 {
		t.Errorf("JellyfinLibraryLimit: got %d, want 5000 on invalid input", cfg.JellyfinLibraryLimit)
	}
	if cfg.RequestsRadarrProfileID != 0 {
		t.Errorf("RequestsRadarrProfileID: got %d, want 0 on empty input", cfg.RequestsRadarrProfileID)
	}
	if cfg.RequestsSonarrProfileID != 0 {
		t.Errorf("RequestsSonarrProfileID: got %d, want 0 on invalid input", cfg.RequestsSonarrProfileID)
	}
}
