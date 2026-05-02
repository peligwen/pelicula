// migrate.go — one-time migration from legacy JSON flat files to SQLite.
// Each function is idempotent: if the JSON file doesn't exist it returns nil.
// On success the JSON file is renamed to <path>.migrated so it won't be
// re-processed on the next restart.
package migratejson

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"pelicula-api/internal/peligrosa"
	"time"
)

// markMigrated is a package-level var so tests can override it to simulate
// rename failures without touching the filesystem.
var markMigrated = func(path string) error {
	return os.Rename(path, path+".migrated")
}

// Run orchestrates all JSON → SQLite migrations.
// configDir is the base directory that contains the pelicula sub-directory
// (e.g. "/config/pelicula").
func Run(ctx context.Context, db *sql.DB, configDir string) error {
	if err := migrateRolesJSON(ctx, db, filepath.Join(configDir, "roles.json")); err != nil {
		slog.Warn("roles migration failed", "component", "migrate", "error", err)
	}
	if err := migrateInvitesJSON(ctx, db, filepath.Join(configDir, "invites.json")); err != nil {
		slog.Warn("invites migration failed", "component", "migrate", "error", err)
	}
	if err := migrateRequestsJSON(ctx, db, filepath.Join(configDir, "requests.json")); err != nil {
		slog.Warn("requests migration failed", "component", "migrate", "error", err)
	}
	return nil
}

// truncToken returns the first 8 characters of a token for logging, or the full token if shorter.
func truncToken(t string) string {
	if len(t) <= 8 {
		return t
	}
	return t[:8] + "…"
}

// alreadyMigrated reports whether the given filename has been recorded in
// migrated_json_files inside tx.
func alreadyMigrated(ctx context.Context, tx *sql.Tx, filename string) (bool, error) {
	var dummy int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM migrated_json_files WHERE filename = ?`, filename).Scan(&dummy)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check migrated_json_files: %w", err)
	}
	return true, nil
}

// recordMigrated inserts filename into migrated_json_files inside tx.
func recordMigrated(ctx context.Context, tx *sql.Tx, filename string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO migrated_json_files (filename, migrated_at) VALUES (?, ?)`,
		filename, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert migrated_json_files: %w", err)
	}
	return nil
}

// migrateRolesJSON reads roles.json and inserts any new entries into the roles table.
func migrateRolesJSON(ctx context.Context, db *sql.DB, jsonPath string) error {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		slog.Warn("roles migration: cannot read file, will retry next startup", "component", "migrate", "path", jsonPath, "error", err)
		return nil
	}

	var f peligrosa.RolesFile
	if err := json.Unmarshal(data, &f); err != nil {
		slog.Warn("roles migration: corrupt JSON, renaming to .corrupt", "component", "migrate", "path", jsonPath, "error", err)
		if renameErr := os.Rename(jsonPath, jsonPath+".corrupt"); renameErr != nil {
			slog.Warn("roles migration: could not rename corrupt file", "component", "migrate", "path", jsonPath, "error", renameErr)
		}
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	done, err := alreadyMigrated(ctx, tx, jsonPath)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	for _, u := range f.Users {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO roles (jellyfin_id, username, role) VALUES (?, ?, ?)
			 ON CONFLICT(jellyfin_id) DO NOTHING`,
			u.JellyfinID, u.Username, string(u.Role),
		)
		if err != nil {
			return fmt.Errorf("insert role %q: %w", u.JellyfinID, err)
		}
	}

	if err := recordMigrated(ctx, tx, jsonPath); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	slog.Info("roles migration complete", "component", "migrate", "count", len(f.Users))
	if err := markMigrated(jsonPath); err != nil {
		slog.Warn("roles migration: could not rename migrated file (cosmetic)", "component", "migrate", "path", jsonPath, "error", err)
	}
	return nil
}

// migrateInvitesJSON reads invites.json and inserts invites + redemptions.
func migrateInvitesJSON(ctx context.Context, db *sql.DB, jsonPath string) error {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		slog.Warn("invites migration: cannot read file, will retry next startup", "component", "migrate", "path", jsonPath, "error", err)
		return nil
	}

	var invites []peligrosa.Invite
	if err := json.Unmarshal(data, &invites); err != nil {
		slog.Warn("invites migration: corrupt JSON, renaming to .corrupt", "component", "migrate", "path", jsonPath, "error", err)
		if renameErr := os.Rename(jsonPath, jsonPath+".corrupt"); renameErr != nil {
			slog.Warn("invites migration: could not rename corrupt file", "component", "migrate", "path", jsonPath, "error", renameErr)
		}
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	done, err := alreadyMigrated(ctx, tx, jsonPath)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	for _, inv := range invites {
		var expiresAt interface{}
		if inv.ExpiresAt != nil {
			expiresAt = inv.ExpiresAt.UTC().Format(time.RFC3339)
		}
		var maxUses interface{}
		if inv.MaxUses != nil {
			maxUses = *inv.MaxUses
		}
		revoked := 0
		if inv.Revoked {
			revoked = 1
		}

		_, err := tx.ExecContext(ctx,
			`INSERT INTO invites (token, label, created_at, created_by, expires_at, max_uses, uses, revoked)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(token) DO NOTHING`,
			inv.Token, inv.Label, inv.CreatedAt.UTC().Format(time.RFC3339),
			inv.CreatedBy, expiresAt, maxUses, inv.Uses, revoked,
		)
		if err != nil {
			return fmt.Errorf("insert invite %q: %w", truncToken(inv.Token), err)
		}

		for _, r := range inv.RedeemedBy {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO redemptions (invite_token, username, jellyfin_id, redeemed_at) VALUES (?, ?, ?, ?)`,
				inv.Token, r.Username, r.JellyfinID, r.RedeemedAt.UTC().Format(time.RFC3339),
			)
			if err != nil {
				return fmt.Errorf("insert redemption token=%q user=%q: %w", truncToken(inv.Token), r.Username, err)
			}
		}
	}

	if err := recordMigrated(ctx, tx, jsonPath); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	slog.Info("invites migration complete", "component", "migrate", "count", len(invites))
	if err := markMigrated(jsonPath); err != nil {
		slog.Warn("invites migration: could not rename migrated file (cosmetic)", "component", "migrate", "path", jsonPath, "error", err)
	}
	return nil
}

// migrateRequestsJSON reads requests.json and inserts requests + events.
func migrateRequestsJSON(ctx context.Context, db *sql.DB, jsonPath string) error {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		slog.Warn("requests migration: cannot read file, will retry next startup", "component", "migrate", "path", jsonPath, "error", err)
		return nil
	}

	var requests []*peligrosa.MediaRequest
	if err := json.Unmarshal(data, &requests); err != nil {
		slog.Warn("requests migration: corrupt JSON, renaming to .corrupt", "component", "migrate", "path", jsonPath, "error", err)
		if renameErr := os.Rename(jsonPath, jsonPath+".corrupt"); renameErr != nil {
			slog.Warn("requests migration: could not rename corrupt file", "component", "migrate", "path", jsonPath, "error", renameErr)
		}
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	done, err := alreadyMigrated(ctx, tx, jsonPath)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	for _, req := range requests {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO requests (id, type, tmdb_id, tvdb_id, title, year, poster,
			                       requested_by, state, reason, arr_id, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO NOTHING`,
			req.ID, req.Type, req.TmdbID, req.TvdbID, req.Title, req.Year, req.Poster,
			req.RequestedBy, string(req.State), req.Reason, req.ArrID,
			req.CreatedAt.UTC().Format(time.RFC3339),
			req.UpdatedAt.UTC().Format(time.RFC3339),
		)
		if err != nil {
			return fmt.Errorf("insert request %q: %w", req.ID, err)
		}

		for _, ev := range req.History {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO request_events (request_id, at, state, actor, note) VALUES (?, ?, ?, ?, ?)`,
				req.ID, ev.At.UTC().Format(time.RFC3339Nano), string(ev.State), ev.Actor, ev.Note,
			)
			if err != nil {
				return fmt.Errorf("insert event request_id=%q: %w", req.ID, err)
			}
		}
	}

	if err := recordMigrated(ctx, tx, jsonPath); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	slog.Info("requests migration complete", "component", "migrate", "count", len(requests))
	if err := markMigrated(jsonPath); err != nil {
		slog.Warn("requests migration: could not rename migrated file (cosmetic)", "component", "migrate", "path", jsonPath, "error", err)
	}
	return nil
}
