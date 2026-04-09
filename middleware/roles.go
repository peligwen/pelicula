// Peligrosa: roles store for jellyfin auth mode.
// Maps Jellyfin user ID → Pelicula role. No passwords stored — Jellyfin is
// the authority. See ../PELIGROSA.md for the trust model.
package main

import (
	"database/sql"
	"log/slog"
)

// RolesEntry maps a Jellyfin user ID to a Pelicula role.
type RolesEntry struct {
	JellyfinID string   `json:"jellyfin_id"`
	Username   string   `json:"username"`
	Role       UserRole `json:"role"`
}

// rolesFile is kept for JSON migration compatibility.
type rolesFile struct {
	Version int          `json:"version"`
	Users   []RolesEntry `json:"users"`
}

// RolesStore persists the Jellyfin user ID → Pelicula role mapping in SQLite.
// SQLite handles concurrency; no additional mutex is needed.
type RolesStore struct {
	db *sql.DB
}

// NewRolesStore creates a RolesStore backed by db.
func NewRolesStore(db *sql.DB) *RolesStore {
	return &RolesStore{db: db}
}

// IsEmpty reports whether the store has no user entries.
func (rs *RolesStore) IsEmpty() bool {
	var count int
	if err := rs.db.QueryRow(`SELECT COUNT(*) FROM roles`).Scan(&count); err != nil {
		return true
	}
	return count == 0
}

// Lookup returns the stored role for the given Jellyfin user ID.
func (rs *RolesStore) Lookup(jellyfinID string) (UserRole, bool) {
	var role UserRole
	err := rs.db.QueryRow(
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
func (rs *RolesStore) Upsert(jellyfinID, username string, role UserRole) error {
	_, err := rs.db.Exec(
		`INSERT INTO roles (jellyfin_id, username, role) VALUES (?, ?, ?)
		 ON CONFLICT(jellyfin_id) DO UPDATE SET username=excluded.username, role=excluded.role`,
		jellyfinID, username, string(role),
	)
	return err
}

// All returns a snapshot of all role entries.
func (rs *RolesStore) All() []RolesEntry {
	rows, err := rs.db.Query(`SELECT jellyfin_id, username, role FROM roles ORDER BY username`)
	if err != nil {
		return []RolesEntry{}
	}
	defer rows.Close()

	var result []RolesEntry
	for rows.Next() {
		var e RolesEntry
		if err := rows.Scan(&e.JellyfinID, &e.Username, &e.Role); err != nil {
			continue
		}
		result = append(result, e)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("roles: All rows iteration error", "component", "roles", "error", err)
	}
	if result == nil {
		return []RolesEntry{}
	}
	return result
}
