package main

import (
	"pelicula-api/internal/clients/apprise"
	"pelicula-api/internal/clients/docker"
)

// dockerCli and appriseCli are test-only package-level vars.
// Initialised by TestMain in testmain_test.go before any test runs.
var dockerCli *docker.Client
var appriseCli *apprise.Client
