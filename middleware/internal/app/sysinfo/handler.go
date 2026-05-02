// Package sysinfo groups stateless diagnostic handlers: host info, speedtest,
// and log aggregation. These handlers have no persistent state of their own
// and are safe to construct once and reuse across requests.
package sysinfo

import (
	"context"
	"net/http"

	"pelicula-api/internal/clients/docker"
)

// LibraryClient is the subset of ServiceClients that the host info handler
// needs to fetch movie and series counts.
type LibraryClient interface {
	Keys() (sonarr, radarr, prowlarr string)
	ArrGet(ctx context.Context, baseURL, apiKey, path string) ([]byte, error)
}

// Handler holds injected dependencies for the sysinfo handlers.
// All fields are set at construction time and never mutated.
type Handler struct {
	// Svc provides *arr API access for library counts in ServeHost.
	Svc LibraryClient

	// RadarrURL and SonarrURL are the base URLs for the respective services.
	RadarrURL string
	SonarrURL string

	// DockerClient is used by ServeLogs to fan-out log fetches across containers.
	DockerClient *docker.Client
}

// ServeHost handles GET /api/pelicula/host — container uptime, disk usage, library counts.
func (h *Handler) ServeHost(w http.ResponseWriter, r *http.Request) {
	handleHost(h, w, r)
}

// ServeSpeedtest handles POST /api/pelicula/speedtest — VPN speed test via gluetun HTTP proxy.
func (h *Handler) ServeSpeedtest(w http.ResponseWriter, r *http.Request) {
	handleSpeedTest(w, r)
}

// ServeLogs handles GET /api/pelicula/logs/aggregate — fan-in container logs.
func (h *Handler) ServeLogs(w http.ResponseWriter, r *http.Request) {
	handleLogsAggregate(h, w, r)
}
