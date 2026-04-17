package repo

import (
	"context"
	"database/sql"
	"log/slog"
)

// RolesEntry maps a Jellyfin user ID to a Pelicula role.
type RolesEntry struct {
	JellyfinID string
	Username   string
	Role       string
}

// RolesStore persists the Jellyfin user ID → Pelicula role mapping in SQLite.
type RolesStore struct{ db *sql.DB }

// NewRolesStore creates a RolesStore backed by db.
func NewRolesStore(db *sql.DB) *RolesStore { return &RolesStore{db: db} }

// Lookup returns the stored role for the given Jellyfin user ID.
// Returns ("", false) if no entry exists.
func (s *RolesStore) Lookup(ctx context.Context, jellyfinID string) (string, bool) {
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

// Upsert sets the role and display name for a Jellyfin user ID.
func (s *RolesStore) Upsert(ctx context.Context, jellyfinID, username, role string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO roles (jellyfin_id, username, role) VALUES (?, ?, ?)
		 ON CONFLICT(jellyfin_id) DO UPDATE SET username=excluded.username, role=excluded.role`,
		jellyfinID, username, role,
	)
	return err
}

// Delete removes the role entry for jellyfinID. No-ops silently if absent.
func (s *RolesStore) Delete(ctx context.Context, jellyfinID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM roles WHERE jellyfin_id = ?`, jellyfinID)
	return err
}

// All returns a snapshot of all role entries ordered by username.
func (s *RolesStore) All(ctx context.Context) ([]RolesEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT jellyfin_id, username, role FROM roles ORDER BY username`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RolesEntry
	for rows.Next() {
		var e RolesEntry
		if err := rows.Scan(&e.JellyfinID, &e.Username, &e.Role); err != nil {
			continue
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("roles: All rows iteration error", "component", "repo/roles", "error", err)
		return out, err
	}
	return out, nil
}

// IsEmpty reports whether the roles table has no entries.
func (s *RolesStore) IsEmpty(ctx context.Context) bool {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM roles`).Scan(&count); err != nil {
		return true
	}
	return count == 0
}
