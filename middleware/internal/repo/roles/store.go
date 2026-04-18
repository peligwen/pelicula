// Package roles provides a typed data-access store for the roles table.
// It maps Jellyfin user IDs to Pelicula role strings. Role values are stored
// and returned as plain strings so this package does not import peligrosa
// (which would create an import cycle).
package roles

import (
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

// IsEmpty reports whether the roles table has no rows.
func (s *Store) IsEmpty() bool {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM roles`).Scan(&count); err != nil {
		return true
	}
	return count == 0
}

// Lookup returns the stored role string for the given Jellyfin user ID.
// Returns ("", false) if not found or on error.
func (s *Store) Lookup(jellyfinID string) (string, bool) {
	var role string
	err := s.db.QueryRow(
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
func (s *Store) Upsert(jellyfinID, username, role string) error {
	_, err := s.db.Exec(
		`INSERT INTO roles (jellyfin_id, username, role) VALUES (?, ?, ?)
		 ON CONFLICT(jellyfin_id) DO UPDATE SET username=excluded.username, role=excluded.role`,
		jellyfinID, username, role,
	)
	return err
}

// All returns a snapshot of all role entries ordered by username.
func (s *Store) All() []Entry {
	rows, err := s.db.Query(`SELECT jellyfin_id, username, role FROM roles ORDER BY username`)
	if err != nil {
		return []Entry{}
	}
	defer rows.Close()

	var result []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.JellyfinID, &e.Username, &e.Role); err != nil {
			continue
		}
		result = append(result, e)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("roles: All rows iteration error", "component", "roles", "error", err)
	}
	if result == nil {
		return []Entry{}
	}
	return result
}

// Delete removes the role entry for jellyfinID. No-ops silently if the ID is
// not in the table.
func (s *Store) Delete(jellyfinID string) error {
	_, err := s.db.Exec(`DELETE FROM roles WHERE jellyfin_id = ?`, jellyfinID)
	return err
}
