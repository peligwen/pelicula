package main

// Service endpoint URL defaults. Declared as vars (not consts) so that tests
// can point them at httptest.Server instances. The canonical values are read
// from environment variables at startup; see envOr in setup.go.
var (
	sonarrURL   = envOr("SONARR_URL", "http://sonarr:8989/sonarr")
	radarrURL   = envOr("RADARR_URL", "http://radarr:7878/radarr")
	prowlarrURL = envOr("PROWLARR_URL", "http://gluetun:9696/prowlarr")
	bazarrURL   = envOr("BAZARR_URL", "http://bazarr:6767/bazarr")
)
