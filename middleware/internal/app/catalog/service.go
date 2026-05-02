package catalog

import (
	"context"

	arr "pelicula-api/internal/clients/arr"
)

// ArrClient is the subset of ServiceClients that the catalog package needs.
type ArrClient interface {
	// Keys returns API keys for Sonarr, Radarr, and Prowlarr.
	Keys() (sonarr, radarr, prowlarr string)
	// SonarrClient returns the typed Sonarr HTTP client.
	SonarrClient() *arr.Client
	// RadarrClient returns the typed Radarr HTTP client.
	RadarrClient() *arr.Client
	// ProwlarrClient returns the typed Prowlarr HTTP client.
	ProwlarrClient() *arr.Client
}

// JellyfinMetaClient is the subset needed for Jellyfin metadata sync.
// It is a function-based interface to break the cycle between catalog and jellyfin packages.
type JellyfinMetaClient interface {
	// GetJellyfinAPIKey returns the Jellyfin API key.
	GetJellyfinAPIKey() string
	// GetJellyfinUserID returns (and optionally resolves) the pelicula-internal user ID.
	GetJellyfinUserID() string
	// SetJellyfinUserID caches the resolved user ID.
	SetJellyfinUserID(id string)
	// JellyfinGet makes a GET request to Jellyfin.
	JellyfinGet(ctx context.Context, path, apiKey string) ([]byte, error)
}
