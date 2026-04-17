// Package repo provides typed data-access stores for pelicula-api.
// Callers in peligrosa/ still use their own SQL for now; this layer exists so
// future phases can migrate them one store at a time without big-bang rewrites.
package repo

import (
	"context"
	"database/sql"
	"time"

	"pelicula-api/internal/repo/dbutil"
)

// Session is a persisted authenticated-user session.
type Session struct {
	Token     string
	Username  string
	Role      string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// SessionStore persists sessions in SQLite.
type SessionStore struct{ db *sql.DB }

// NewSessionStore creates a SessionStore backed by db.
func NewSessionStore(db *sql.DB) *SessionStore { return &SessionStore{db: db} }

// Upsert inserts or replaces a session row.
func (s *SessionStore) Upsert(ctx context.Context, sess Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO sessions (token, username, role, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		sess.Token, sess.Username, sess.Role,
		dbutil.FormatTime(sess.CreatedAt),
		dbutil.FormatTime(sess.ExpiresAt),
	)
	return err
}

// Delete removes a session by token.
func (s *SessionStore) Delete(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// DeleteExpired removes all sessions whose expires_at is at or before now.
func (s *SessionStore) DeleteExpired(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at <= ?`,
		dbutil.FormatTime(now),
	)
	return err
}

// ListActive returns all sessions that have not yet expired.
func (s *SessionStore) ListActive(ctx context.Context, now time.Time) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT token, username, role, created_at, expires_at FROM sessions WHERE expires_at > ?`,
		dbutil.FormatTime(now),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var sess Session
		var createdAtStr, expiresAtStr string
		if err := rows.Scan(&sess.Token, &sess.Username, &sess.Role, &createdAtStr, &expiresAtStr); err != nil {
			continue
		}
		if t, err := dbutil.ParseTime(createdAtStr); err == nil {
			sess.CreatedAt = t
		}
		if t, err := dbutil.ParseTime(expiresAtStr); err == nil {
			sess.ExpiresAt = t
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}
