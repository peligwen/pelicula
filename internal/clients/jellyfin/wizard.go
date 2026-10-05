package jellyfin

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// wizardSettle is how long to wait after the wizard so Jellyfin can finish
// switching out of startup mode before the first login. Tests shorten it.
var wizardSettle = 2 * time.Second

// CompleteStartupWizard runs Jellyfin's first-run wizard, creating adminUser
// (the wizard's user is always an administrator) with adminPass. The startup
// endpoints are unauthenticated, so this only works on a fresh server.
func (c *Client) CompleteStartupWizard(ctx context.Context, adminUser, adminPass string) error {
	if adminUser == "" || adminPass == "" {
		return errors.New("jellyfin: wizard needs an admin username and password")
	}
	slog.Info("completing Jellyfin startup wizard", "component", "jellyfin", "admin", adminUser)

	if _, err := c.Post(ctx, "/Startup/Configuration", "", map[string]any{
		"UICulture":           "en-US",
		"MetadataCountryCode": "US",
	}); err != nil {
		return wrap("set startup configuration", err)
	}
	// Jellyfin 10.11+ creates the startup user lazily on GET; POST fails until then.
	if _, err := c.Get(ctx, "/Startup/User", ""); err != nil {
		slog.Warn("could not fetch Jellyfin startup user", "component", "jellyfin", "error", err)
	}
	if _, err := c.Post(ctx, "/Startup/User", "", map[string]any{
		"Name":     adminUser,
		"Password": adminPass,
	}); err != nil {
		return wrap("create admin user", err)
	}
	if _, err := c.Post(ctx, "/Startup/Complete", "", nil); err != nil {
		return wrap("complete wizard", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wizardSettle):
	}
	return nil
}
