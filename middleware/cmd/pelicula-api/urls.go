package main

// Service endpoint URL defaults. Declared as vars (not consts) so that tests
// can point them at httptest.Server instances. The canonical values are read
// from environment variables at startup; see envOr in setup.go.
var (
	sonarrURL   = envOr("SONARR_URL", "http://sonarr:8989/sonarr")
	radarrURL   = envOr("RADARR_URL", "http://radarr:7878/radarr")
	prowlarrURL = envOr("PROWLARR_URL", "http://gluetun:9696/prowlarr")
	bazarrURL   = envOr("BAZARR_URL", "http://bazarr:6767/bazarr")

	// jellyfinURL is a var (not const) so tests can point it at an httptest.Server
	// and so power users can override it via JELLYFIN_URL.
	jellyfinURL = envOr("JELLYFIN_URL", "http://jellyfin:8096/jellyfin")

	// proculaURL is the base URL for the Procula processing-pipeline service.
	// Used by hooks, catalog, jobs, actions, and services health check.
	proculaURL = envOr("PROCULA_URL", "http://procula:8282")
)
