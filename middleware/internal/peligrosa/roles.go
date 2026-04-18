// Peligrosa: roles store for jellyfin auth mode.
// Maps Jellyfin user ID → Pelicula role. No passwords stored — Jellyfin is
// the authority. See ../../docs/PELIGROSA.md for the trust model.
package peligrosa

import (
	"database/sql"

	"pelicula-api/internal/repo/roles"
)

// RolesEntry maps a Jellyfin user ID to a Pelicula role.
type RolesEntry struct {
	JellyfinID string   `json:"jellyfin_id"`
	Username   string   `json:"username"`
	Role       UserRole `json:"role"`
}

// RolesFile is kept for JSON migration compatibility.
// Used by migrate_json.go in the main package to deserialize the legacy roles.json.
type RolesFile struct {
	Version int          `json:"version"`
	Users   []RolesEntry `json:"users"`
}

// RolesStore wraps the typed repo/roles store and adapts its string-typed API
// to the UserRole domain type used throughout peligrosa.
// SQLite handles concurrency; no additional mutex is needed.
type RolesStore struct {
	store *roles.Store
}

// NewRolesStore creates a RolesStore backed by db.
func NewRolesStore(db *sql.DB) *RolesStore {
	return &RolesStore{store: roles.New(db)}
}

// IsEmpty reports whether the store has no user entries.
func (rs *RolesStore) IsEmpty() bool {
	return rs.store.IsEmpty()
}

// Lookup returns the stored role for the given Jellyfin user ID.
func (rs *RolesStore) Lookup(jellyfinID string) (UserRole, bool) {
	role, ok := rs.store.Lookup(jellyfinID)
	if !ok {
		return "", false
	}
	return UserRole(role), true
}

// Upsert sets the role for a Jellyfin user ID, creating the entry if absent.
// Also refreshes the stored display name.
func (rs *RolesStore) Upsert(jellyfinID, username string, role UserRole) error {
	return rs.store.Upsert(jellyfinID, username, string(role))
}

// All returns a snapshot of all role entries.
func (rs *RolesStore) All() []RolesEntry {
	entries := rs.store.All()
	result := make([]RolesEntry, len(entries))
	for i, e := range entries {
		result[i] = RolesEntry{
			JellyfinID: e.JellyfinID,
			Username:   e.Username,
			Role:       UserRole(e.Role),
		}
	}
	return result
}

// Delete removes the role entry for jellyfinID. No-ops silently if the ID is
// not in the table.
func (rs *RolesStore) Delete(jellyfinID string) error {
	return rs.store.Delete(jellyfinID)
}
