package services

import (
	"context"
)

// jellyfinGet makes a GET request to Jellyfin with the Emby authorization header.
// Moved from cmd/pelicula-api/jellyfin_wiring.go where it was defined as a
// package-level function. Now a private method on Clients.
func (c *Clients) jellyfinGet(ctx context.Context, path, token string) ([]byte, error) {
	return c.jellyfin.Get(ctx, path, token)
}
