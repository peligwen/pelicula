package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ── roles ────────────────────────────────────────────────────────────────────

// GetRole returns the stored role for username. ok is false when no row
// exists (callers then derive a default from Jellyfin's admin flag).
func (s *Store) GetRole(ctx context.Context, username string) (role Role, ok bool, err error) {
	var r string
	err = s.db.QueryRowContext(ctx, `SELECT role FROM roles WHERE username = ?`, username).Scan(&r)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return Role(r), true, nil
}

// SetRole upserts username's role and updates any live sessions to match.
func (s *Store) SetRole(ctx context.Context, username string, role Role) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO roles (username, role, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(username) DO UPDATE SET role = excluded.role, updated_at = excluded.updated_at`,
		username, string(role), ts(now())); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET role = ? WHERE username = ?`, string(role), username); err != nil {
		return err
	}
	return tx.Commit()
}

// ListRoles returns every explicitly assigned role.
func (s *Store) ListRoles(ctx context.Context) (map[string]Role, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT username, role FROM roles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Role{}
	for rows.Next() {
		var u, r string
		if err := rows.Scan(&u, &r); err != nil {
			return nil, err
		}
		out[u] = Role(r)
	}
	return out, rows.Err()
}

// DeleteRole removes username's role row and all of their sessions.
func (s *Store) DeleteRole(ctx context.Context, username string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM roles WHERE username = ?`, username); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE username = ?`, username); err != nil {
		return err
	}
	return tx.Commit()
}

// ── sessions ─────────────────────────────────────────────────────────────────

// Session is a logged-in browser session. Token is the cookie value.
type Session struct {
	Token          string
	Username       string
	Role           Role
	JellyfinUserID string
	JellyfinToken  string
	ExpiresAt      time.Time
	CreatedAt      time.Time
}

// CreateSession inserts sess. CreatedAt is set if zero.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (token, username, role, jellyfin_user_id, jellyfin_token, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sess.Token, sess.Username, string(sess.Role), sess.JellyfinUserID, sess.JellyfinToken,
		ts(sess.ExpiresAt), ts(sess.CreatedAt))
	return err
}

// GetSession returns the live session for token, or nil when it is missing or
// expired. Expired rows are deleted on read.
func (s *Store) GetSession(ctx context.Context, token string) (*Session, error) {
	var sess Session
	var role, exp, created string
	err := s.db.QueryRowContext(ctx, `
		SELECT token, username, role, jellyfin_user_id, jellyfin_token, expires_at, created_at
		FROM sessions WHERE token = ?`, token).
		Scan(&sess.Token, &sess.Username, &role, &sess.JellyfinUserID, &sess.JellyfinToken, &exp, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sess.Role = Role(role)
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

// DeleteSessionsForUser logs a user out everywhere.
func (s *Store) DeleteSessionsForUser(ctx context.Context, username string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE username = ?`, username)
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

// ── invites ──────────────────────────────────────────────────────────────────

// Invite is a one-time registration code that grants a role.
type Invite struct {
	Code      string
	Role      Role
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedBy    string
	UsedAt    *time.Time
}

// Used reports whether the invite has been redeemed.
func (i Invite) Used() bool { return i.UsedAt != nil }

// Expired reports whether the invite is past its expiry at t.
func (i Invite) Expired(t time.Time) bool { return !i.ExpiresAt.After(t) }

// ErrInviteUnavailable is returned when redeeming a used, expired or unknown invite.
var ErrInviteUnavailable = errors.New("invite is not available")

// CreateInvite inserts inv. CreatedAt is set if zero.
func (s *Store) CreateInvite(ctx context.Context, inv Invite) error {
	if inv.CreatedAt.IsZero() {
		inv.CreatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO invites (code, role, created_by, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		inv.Code, string(inv.Role), inv.CreatedBy, ts(inv.CreatedAt), ts(inv.ExpiresAt))
	return err
}

func scanInvite(sc interface{ Scan(...any) error }) (*Invite, error) {
	var inv Invite
	var role, created, exp string
	var usedAt sql.NullString
	if err := sc.Scan(&inv.Code, &role, &inv.CreatedBy, &created, &exp, &inv.UsedBy, &usedAt); err != nil {
		return nil, err
	}
	inv.Role = Role(role)
	inv.CreatedAt = parseTS(created)
	inv.ExpiresAt = parseTS(exp)
	inv.UsedAt = nullTS(usedAt)
	return &inv, nil
}

const inviteCols = `code, role, created_by, created_at, expires_at, used_by, used_at`

// GetInvite returns the invite for code or nil when unknown.
func (s *Store) GetInvite(ctx context.Context, code string) (*Invite, error) {
	inv, err := scanInvite(s.db.QueryRowContext(ctx, `SELECT `+inviteCols+` FROM invites WHERE code = ?`, code))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return inv, err
}

// ListInvites returns all invites, newest first.
func (s *Store) ListInvites(ctx context.Context) ([]Invite, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+inviteCols+` FROM invites ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *inv)
	}
	return out, rows.Err()
}

// UseInvite atomically marks code as redeemed by username. It fails with
// ErrInviteUnavailable when the code is unknown, already used, or expired.
func (s *Store) UseInvite(ctx context.Context, code, username string) error {
	t := now()
	res, err := s.db.ExecContext(ctx, `
		UPDATE invites SET used_by = ?, used_at = ?
		WHERE code = ? AND used_at IS NULL AND expires_at > ?`,
		username, ts(t), code, ts(t))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrInviteUnavailable
	}
	return nil
}

// DeleteInvite revokes an invite.
func (s *Store) DeleteInvite(ctx context.Context, code string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM invites WHERE code = ?`, code)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
