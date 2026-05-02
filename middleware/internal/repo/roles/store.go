// Package roles provides a typed data-access store for the roles table.
// It maps Jellyfin user IDs to Pelicula role strings. Role values are stored
// and returned as plain strings so this package does not import peligrosa
// (which would create an import cycle).
package roles

import (
	"context"
	"database/sql"
	"log/slog"
)

// Entry holds a single row from the roles table.
type Entry struct {
	JellyfinID string
	Username   string
	Role       string
}

// Store wraps a *sql.DB and provides named methods for roles table access.
// SQLite handles concurrency; no additional mutex is needed.
type Store struct {
	db *sql.DB
}

// New returns a Store backed by db.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// IsEmpty reports whether the roles table has no rows. On query failure it
// logs a warning and returns true (treating an unreadable store as empty is
// the safe default — callers use this to gate first-admin registration).
func (s *Store) IsEmpty(ctx context.Context) bool {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM roles`).Scan(&count); err != nil {
		slog.Warn("roles: IsEmpty count failed", "component", "roles", "error", err)
		return true
	}
	return count == 0
}

// Lookup returns the stored role string for the given Jellyfin user ID.
// Returns ("", false) if not found or on error.
func (s *Store) Lookup(ctx context.Context, jellyfinID string) (string, bool) {
	var role string
	err := s.db.QueryRowContext(ctx,
		`SELECT role FROM roles WHERE jellyfin_id = ?`, jellyfinID,
	).Scan(&role)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		return "", false
	}
	return role, true
}

// Upsert sets the role for a Jellyfin user ID, creating the entry if absent.
// Also refreshes the stored display name.
func (s *Store) Upsert(ctx context.Context, jellyfinID, username, role string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO roles (jellyfin_id, username, role) VALUES (?, ?, ?)
		 ON CONFLICT(jellyfin_id) DO UPDATE SET username=excluded.username, role=excluded.role`,
		jellyfinID, username, role,
	)
	return err
}

// All returns a snapshot of all role entries ordered by username. Scan errors
// are propagated; the caller receives a partial result and the first scan error.
func (s *Store) All(ctx context.Context) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT jellyfin_id, username, role FROM roles ORDER BY username`)
	if err != nil {
		return []Entry{}, err
	}
	defer rows.Close()

	var result []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.JellyfinID, &e.Username, &e.Role); err != nil {
			return result, err
		}
		result = append(result, e)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if result == nil {
		return []Entry{}, nil
	}
	return result, nil
}

// Delete removes the role entry for jellyfinID. No-ops silently if the ID is
// not in the table.
func (s *Store) Delete(ctx context.Context, jellyfinID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM roles WHERE jellyfin_id = ?`, jellyfinID)
	return err
}
