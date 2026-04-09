// migrate_json.go — one-time migration from legacy JSON flat files to SQLite.
// Each function is idempotent: if the JSON file doesn't exist it returns nil.
// On success the JSON file is renamed to <path>.migrated so it won't be
// re-processed on the next restart.
package main

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// migrateAllJSON orchestrates all JSON → SQLite migrations.
// configDir is the base directory that contains the pelicula sub-directory
// (e.g. "/config/pelicula").
func migrateAllJSON(db *sql.DB, configDir string) {
	migrateRolesJSON(db, filepath.Join(configDir, "roles.json"))
	migrateInvitesJSON(db, filepath.Join(configDir, "invites.json"))
	migrateRequestsJSON(db, filepath.Join(configDir, "requests.json"))
	migrateDismissedJSON(db, filepath.Join(configDir, "dismissed.json"))
}

// markMigrated renames path → path.migrated so it will not be re-processed.
func markMigrated(path string) {
	if err := os.Rename(path, path+".migrated"); err != nil {
		slog.Warn("could not rename migrated JSON file", "component", "migrate", "path", path, "error", err)
	}
}

// migrateRolesJSON reads roles.json and inserts any new entries into the roles table.
func migrateRolesJSON(db *sql.DB, jsonPath string) {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		// Non-NotExist error (permissions, I/O): log and retry next startup.
		slog.Warn("roles migration: cannot read file, will retry next startup", "component", "migrate", "path", jsonPath, "error", err)
		return
	}

	var f rolesFile
	if err := json.Unmarshal(data, &f); err != nil {
		slog.Warn("roles migration: corrupt JSON, renaming to .corrupt", "component", "migrate", "path", jsonPath, "error", err)
		if renameErr := os.Rename(jsonPath, jsonPath+".corrupt"); renameErr != nil {
			slog.Warn("roles migration: could not rename corrupt file", "component", "migrate", "path", jsonPath, "error", renameErr)
		}
		return
	}

	for _, u := range f.Users {
		_, err := db.Exec(
			`INSERT INTO roles (jellyfin_id, username, role) VALUES (?, ?, ?)
			 ON CONFLICT(jellyfin_id) DO NOTHING`,
			u.JellyfinID, u.Username, string(u.Role),
		)
		if err != nil {
			slog.Warn("roles migration: failed to insert entry",
				"component", "migrate", "jellyfin_id", u.JellyfinID, "error", err)
		}
	}

	slog.Info("roles migration complete", "component", "migrate", "count", len(f.Users))
	markMigrated(jsonPath)
}

// migrateInvitesJSON reads invites.json and inserts invites + redemptions.
func migrateInvitesJSON(db *sql.DB, jsonPath string) {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		// Non-NotExist error: log and retry next startup.
		slog.Warn("invites migration: cannot read file, will retry next startup", "component", "migrate", "path", jsonPath, "error", err)
		return
	}

	var invites []Invite
	if err := json.Unmarshal(data, &invites); err != nil {
		slog.Warn("invites migration: corrupt JSON, renaming to .corrupt", "component", "migrate", "path", jsonPath, "error", err)
		if renameErr := os.Rename(jsonPath, jsonPath+".corrupt"); renameErr != nil {
			slog.Warn("invites migration: could not rename corrupt file", "component", "migrate", "path", jsonPath, "error", renameErr)
		}
		return
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

		_, err := db.Exec(
			`INSERT INTO invites (token, label, created_at, created_by, expires_at, max_uses, uses, revoked)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(token) DO NOTHING`,
			inv.Token, inv.Label, inv.CreatedAt.UTC().Format(time.RFC3339),
			inv.CreatedBy, expiresAt, maxUses, inv.Uses, revoked,
		)
		if err != nil {
			slog.Warn("invites migration: failed to insert invite",
				"component", "migrate", "token", inv.Token[:8]+"…", "error", err)
			continue
		}

		for _, r := range inv.RedeemedBy {
			_, err := db.Exec(
				`INSERT INTO redemptions (invite_token, username, jellyfin_id, redeemed_at) VALUES (?, ?, ?, ?)`,
				inv.Token, r.Username, r.JellyfinID, r.RedeemedAt.UTC().Format(time.RFC3339),
			)
			if err != nil {
				slog.Warn("invites migration: failed to insert redemption",
					"component", "migrate", "token", inv.Token[:8]+"…", "user", r.Username, "error", err)
			}
		}
	}

	slog.Info("invites migration complete", "component", "migrate", "count", len(invites))
	markMigrated(jsonPath)
}

// migrateRequestsJSON reads requests.json and inserts requests + events.
func migrateRequestsJSON(db *sql.DB, jsonPath string) {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		// Non-NotExist error: log and retry next startup.
		slog.Warn("requests migration: cannot read file, will retry next startup", "component", "migrate", "path", jsonPath, "error", err)
		return
	}

	var requests []*MediaRequest
	if err := json.Unmarshal(data, &requests); err != nil {
		slog.Warn("requests migration: corrupt JSON, renaming to .corrupt", "component", "migrate", "path", jsonPath, "error", err)
		if renameErr := os.Rename(jsonPath, jsonPath+".corrupt"); renameErr != nil {
			slog.Warn("requests migration: could not rename corrupt file", "component", "migrate", "path", jsonPath, "error", renameErr)
		}
		return
	}

	for _, req := range requests {
		_, err := db.Exec(
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
			slog.Warn("requests migration: failed to insert request",
				"component", "migrate", "id", req.ID, "error", err)
			continue
		}

		for _, ev := range req.History {
			_, err := db.Exec(
				`INSERT INTO request_events (request_id, at, state, actor, note) VALUES (?, ?, ?, ?, ?)`,
				req.ID, ev.At.UTC().Format(time.RFC3339Nano), string(ev.State), ev.Actor, ev.Note,
			)
			if err != nil {
				slog.Warn("requests migration: failed to insert event",
					"component", "migrate", "request_id", req.ID, "error", err)
			}
		}
	}

	slog.Info("requests migration complete", "component", "migrate", "count", len(requests))
	markMigrated(jsonPath)
}

// migrateDismissedJSON reads dismissed.json and inserts job IDs.
func migrateDismissedJSON(db *sql.DB, jsonPath string) {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		// Non-NotExist error: log and retry next startup.
		slog.Warn("dismissed migration: cannot read file, will retry next startup", "component", "migrate", "path", jsonPath, "error", err)
		return
	}

	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		slog.Warn("dismissed migration: corrupt JSON, renaming to .corrupt", "component", "migrate", "path", jsonPath, "error", err)
		if renameErr := os.Rename(jsonPath, jsonPath+".corrupt"); renameErr != nil {
			slog.Warn("dismissed migration: could not rename corrupt file", "component", "migrate", "path", jsonPath, "error", renameErr)
		}
		return
	}

	for _, id := range ids {
		if _, err := db.Exec(`INSERT OR IGNORE INTO dismissed_jobs (job_id) VALUES (?)`, id); err != nil {
			slog.Warn("dismissed migration: failed to insert job_id",
				"component", "migrate", "job_id", id, "error", err)
		}
	}

	slog.Info("dismissed migration complete", "component", "migrate", "count", len(ids))
	markMigrated(jsonPath)
}
