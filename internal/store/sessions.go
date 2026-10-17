package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Session is a logged-in dashboard session. Token is the cookie value.
type Session struct {
	Token     string
	Username  string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// CreateSession inserts sess. CreatedAt is set if zero.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (token, username, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		sess.Token, sess.Username, ts(sess.ExpiresAt), ts(sess.CreatedAt))
	return err
}

// GetSession returns the live session for token, or nil when it is missing or
// expired. Expired rows are deleted on read.
func (s *Store) GetSession(ctx context.Context, token string) (*Session, error) {
	var sess Session
	var exp, created string
	err := s.db.QueryRowContext(ctx, `
		SELECT token, username, expires_at, created_at FROM sessions WHERE token = ?`, token).
		Scan(&sess.Token, &sess.Username, &exp, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sess.ExpiresAt = parseTS(exp)
	sess.CreatedAt = parseTS(created)
	if !sess.ExpiresAt.After(now()) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
		return nil, nil
	}
	return &sess, nil
}

// DeleteSession removes one session.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// PurgeExpiredSessions deletes expired rows and returns how many.
func (s *Store) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, ts(now()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
