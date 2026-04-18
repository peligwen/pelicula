package main

import (
	"os"
	"testing"

	"pelicula-api/internal/clients/apprise"
	"pelicula-api/internal/clients/docker"
)

// TestMain initialises package-level globals that main() would normally set up,
// so that handler tests can run without starting the full server.
func TestMain(m *testing.M) {
	dockerCli = docker.New("http://docker-proxy:2375", "pelicula")
	appriseCli = apprise.New("http://apprise:8000/notify", "/config")
	os.Exit(m.Run())
}
