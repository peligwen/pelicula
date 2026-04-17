package main

import (
	"strings"

	proculaclient "pelicula-api/internal/clients/procula"
)

// procClient is the typed HTTP client for the Procula processing pipeline API.
// It is initialised once at startup from the PROCULA_URL and PROCULA_API_KEY
// environment variables and replaces ad-hoc http.NewRequest call sites.
var procClient = proculaclient.New(
	envOr("PROCULA_URL", "http://procula:8282"),
	strings.TrimSpace(proculaAPIKeyFromEnv()),
)

// proculaAPIKeyFromEnv reads PROCULA_API_KEY without importing os at package
// init time in a way that conflicts with other files; envOr already does os.Getenv.
func proculaAPIKeyFromEnv() string { return envOr("PROCULA_API_KEY", "") }
