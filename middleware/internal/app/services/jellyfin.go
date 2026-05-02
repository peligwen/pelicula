package services

import (
	"context"

	jfclient "pelicula-api/internal/clients/jellyfin"
)

// jellyfinGet makes a GET request to Jellyfin with the Emby authorization header.
// Moved from cmd/pelicula-api/jellyfin_wiring.go where it was defined as a
// package-level function. Now a private method on Clients.
func (c *Clients) jellyfinGet(ctx context.Context, path, token string) ([]byte, error) {
	jfc := jfclient.NewWithHTTPClient(c.jellyfinURL, c.client)
	return jfc.Get(ctx, path, token)
}
