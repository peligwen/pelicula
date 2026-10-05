package autowire

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// wireJellyfin completes the startup wizard when needed (creating the admin
// the server authenticates as), then makes sure the Movies and TV Shows
// libraries exist.
func wireJellyfin(ctx context.Context, log *slog.Logger, d Deps) error {
	done, err := d.Jellyfin.StartupWizardCompleted(ctx)
	if err != nil {
		return fmt.Errorf("check startup wizard: %w", err)
	}
	wizardRan := false
	if !done {
		log.Info("completing Jellyfin startup wizard", "admin", d.Cfg.JellyfinAdminUser)
		if err := d.Jellyfin.CompleteStartupWizard(ctx, d.Cfg.JellyfinAdminUser, d.Cfg.JellyfinAdminPassword); err != nil {
			return fmt.Errorf("complete startup wizard: %w", err)
		}
		wizardRan = true
	} else {
		log.Info("Jellyfin startup wizard already completed")
	}

	token, err := adminToken(ctx, d.JFAdmin, wizardRan)
	if err != nil {
		return fmt.Errorf("jellyfin admin token: %w", err)
	}

	libs, err := d.Jellyfin.ListLibraries(ctx, token)
	if err != nil {
		return fmt.Errorf("list libraries: %w", err)
	}

	var first firstErr
	for _, want := range []struct{ name, ctype, path string }{
		{"Movies", "movies", d.Cfg.MoviesPath},
		{"TV Shows", "tvshows", d.Cfg.TVPath},
	} {
		first.note(ensureLibrary(ctx, log, d.Jellyfin, token, libs, want.name, want.ctype, want.path))
	}
	return first.err
}

// adminToken fetches the Jellyfin admin token. Right after the wizard the new
// admin can take a moment to become usable, so then we retry a few times.
func adminToken(ctx context.Context, src TokenSource, afterWizard bool) (string, error) {
	attempts := 1
	if afterWizard {
		attempts = 5
	}
	var err error
	for i := 0; i < attempts; i++ {
		var token string
		if token, err = src.Token(ctx); err == nil {
			return token, nil
		}
		if i == attempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	return "", err
}

// ensureLibrary adds the library when no library of that name exists. An
// existing library is never modified: the Jellyfin client interface has no
// path repair, and a user may have added folders on purpose.
func ensureLibrary(ctx context.Context, log *slog.Logger, jf JellyfinClient, token string, existing []Library, name, collectionType, path string) error {
	for _, l := range existing {
		if !strings.EqualFold(l.Name, name) {
			continue
		}
		found := false
		for _, loc := range l.Locations {
			if samePath(loc, path) {
				found = true
				break
			}
		}
		if found {
			log.Info("Jellyfin library already configured", "library", name)
		} else {
			log.Warn("Jellyfin library exists without the expected path, leaving it as is",
				"library", name, "want", path, "have", l.Locations)
		}
		return nil
	}
	if err := jf.AddLibrary(ctx, token, name, collectionType, path); err != nil {
		return fmt.Errorf("add library %q: %w", name, err)
	}
	log.Info("added Jellyfin library", "library", name, "path", path)
	return nil
}
