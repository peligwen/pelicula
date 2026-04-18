// Package sessions provides a typed data-access store for the sessions and
// rate_limits tables. Session persistence lets authenticated sessions survive
// process restarts. Rate-limit tracking records failed login attempts per IP.
package sessions

import (
	"context"
	"database/sql"
	"time"

	"pelicula-api/internal/repo/dbutil"
)

// Session holds a single row from the sessions table.
type Session struct {
	Token     string
	Username  string
	Role      string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Store wraps a *sql.DB and provides named methods for sessions and rate_limits
// table access. SQLite handles concurrency via the single-writer connection;
// no additional mutex is needed here.
type Store struct {
	db *sql.DB
}

// New returns a Store backed by db.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// ── Session methods ───────────────────────────────────────────────────────────

// Create inserts a new session row. Uses INSERT OR REPLACE so a re-login with
// the same token (unlikely but safe) is idempotent.
func (s *Store) Create(ctx context.Context, token, username, role string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO sessions (token, username, role, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		token, username, role,
		dbutil.FormatTime(time.Now()),
		dbutil.FormatTime(expiresAt),
	)
	return err
}

// Lookup returns the Session for token. Returns (nil, nil) when the token is
// not found. Expired sessions are returned as-is — the caller decides whether
// to accept or reject based on ExpiresAt.
func (s *Store) Lookup(ctx context.Context, token string) (*Session, error) {
	var sess Session
	var createdStr, expiresStr string
	err := s.db.QueryRowContext(ctx,
		`SELECT token, username, role, created_at, expires_at FROM sessions WHERE token = ?`,
		token,
	).Scan(&sess.Token, &sess.Username, &sess.Role, &createdStr, &expiresStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if sess.CreatedAt, err = dbutil.ParseTime(createdStr); err != nil {
		return nil, err
	}
	if sess.ExpiresAt, err = dbutil.ParseTime(expiresStr); err != nil {
		return nil, err
	}
	return &sess, nil
}

// LookupActive returns the non-expired sessions from the database. Used on
// startup to reload sessions into the in-memory map.
func (s *Store) LookupActive(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT token, username, role, created_at, expires_at
		 FROM sessions WHERE expires_at > ?`,
		dbutil.FormatTime(time.Now()),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Session
	for rows.Next() {
		var sess Session
		var createdStr, expiresStr string
		if err := rows.Scan(&sess.Token, &sess.Username, &sess.Role, &createdStr, &expiresStr); err != nil {
			continue
		}
		var parseErr error
		if sess.CreatedAt, parseErr = dbutil.ParseTime(createdStr); parseErr != nil {
			continue
		}
		if sess.ExpiresAt, parseErr = dbutil.ParseTime(expiresStr); parseErr != nil {
			continue
		}
		result = append(result, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// Rotate replaces oldToken with newToken, updating the expiry. The old row is
// deleted and a new one inserted atomically in a transaction so the session
// cannot be observed in a half-replaced state.
func (s *Store) Rotate(ctx context.Context, oldToken, newToken string, expiresAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	// Read username and role from the old token row.
	var username, role string
	if err := tx.QueryRowContext(ctx,
		`SELECT username, role FROM sessions WHERE token = ?`, oldToken,
	).Scan(&username, &role); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sessions WHERE token = ?`, oldToken,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sessions (token, username, role, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		newToken, username, role,
		dbutil.FormatTime(time.Now()),
		dbutil.FormatTime(expiresAt),
	); err != nil {
		return err
	}
	return tx.Commit()
}

// Revoke deletes the session row identified by token. No-ops silently if the
// token is not present.
func (s *Store) Revoke(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE token = ?`, token,
	)
	return err
}

// RevokeByUsername deletes all sessions for the given username. Used when an
// admin revokes a user's access.
func (s *Store) RevokeByUsername(ctx context.Context, username string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE username = ?`, username,
	)
	return err
}

// PruneExpired deletes all sessions whose expires_at is in the past.
func (s *Store) PruneExpired(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at <= ?`,
		dbutil.FormatTime(time.Now()),
	)
	return err
}

// ── Rate-limit methods ────────────────────────────────────────────────────────

// RateLimitUpsert increments the fail_count for the given IP within the
// current window. If no row exists, or if the existing window_start is before
// the window boundary, it resets to a fresh count of 1. Returns the updated
// count after the upsert.
//
// The upsert is a single SQL statement (no extra round-trips) to keep the
// hot path fast.
func (s *Store) RateLimitUpsert(ctx context.Context, ip string, window time.Time) (int, error) {
	windowStr := dbutil.FormatTime(window)
	nowStr := dbutil.FormatTime(time.Now())

	// window_start stores the timestamp of the first failure in the current
	// window (not the boundary). The boundary (windowStr) is used only to
	// decide whether to reset: if the stored window_start is older than the
	// boundary, the window has expired.
	//
	// Single statement:
	// - If no row: insert with fail_count=1 and window_start=now.
	// - If the stored window_start is still within the current window: increment.
	// - If the stored window_start is older than the window boundary: reset to 1, window_start=now.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO rate_limits (ip, fail_count, window_start) VALUES (?, 1, ?)
		ON CONFLICT(ip) DO UPDATE SET
			fail_count   = CASE WHEN window_start >= ?
			                    THEN fail_count + 1
			                    ELSE 1
			               END,
			window_start = CASE WHEN window_start >= ?
			                    THEN window_start
			                    ELSE ?
			               END`,
		ip, nowStr, windowStr, windowStr, nowStr,
	)
	if err != nil {
		return 0, err
	}

	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT fail_count FROM rate_limits WHERE ip = ?`, ip,
	).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// RateLimitCount returns the current fail_count for the given IP within the
// active window. Returns 0 when no row exists or the stored window_start is
// older than the window boundary (i.e. the window has expired and the count
// would reset on the next failure). This is a read-only query — it does not
// modify the table.
func (s *Store) RateLimitCount(ctx context.Context, ip string, window time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT fail_count FROM rate_limits WHERE ip = ? AND window_start >= ?`,
		ip, dbutil.FormatTime(window),
	).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return count, nil
}

// PruneRateLimit removes rate-limit entries whose window_start is before the
// given cutoff. Called periodically to prevent unbounded table growth.
func (s *Store) PruneRateLimit(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM rate_limits WHERE window_start < ?`,
		dbutil.FormatTime(before),
	)
	return err
}
