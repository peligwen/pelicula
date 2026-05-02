package backup

import (
	"context"

	"pelicula-api/internal/peligrosa"
)

// ArrClient is the minimal interface the Handler needs to reach *arr services.
// *ServiceClients from package main satisfies this interface.
type ArrClient interface {
	Keys() (sonarr, radarr, prowlarr string)
	ArrGet(ctx context.Context, baseURL, apiKey, path string) ([]byte, error)
	ArrPost(ctx context.Context, baseURL, apiKey, path string, payload any) ([]byte, error)
}

// LibPathResolver resolves the first available library root path for a given
// *arr service type. *library.Handler from internal/app/library satisfies this.
type LibPathResolver interface {
	FirstLibraryPath(arr, defaultPath string) string
}

// Handler owns all backup/restore state. Construct with New.
//
// Auth, Invites, and Requests may each be nil — the handler degrades gracefully
// when auth is disabled or stores are unavailable.
type Handler struct {
	Svc      ArrClient
	Lib      LibPathResolver
	Auth     *peligrosa.Auth         // nil if auth is disabled
	Invites  *peligrosa.InviteStore  // nil if unavailable
	Requests *peligrosa.RequestStore // nil if unavailable

	RadarrURL string
	SonarrURL string
}

// New constructs a Handler. Svc and the URL strings are required; all other
// fields are optional and handled gracefully when nil.
func New(
	svc ArrClient,
	lib LibPathResolver,
	auth *peligrosa.Auth,
	invites *peligrosa.InviteStore,
	requests *peligrosa.RequestStore,
	radarrURL, sonarrURL string,
) *Handler {
	return &Handler{
		Svc:       svc,
		Lib:       lib,
		Auth:      auth,
		Invites:   invites,
		Requests:  requests,
		RadarrURL: radarrURL,
		SonarrURL: sonarrURL,
	}
}
