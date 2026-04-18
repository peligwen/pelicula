// Package invites provides a typed data-access store for the invites and
// redemptions tables. Business logic (Jellyfin user creation, token validation,
// HTTP handlers) lives in internal/peligrosa; this layer owns all SQL.
package invites

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"pelicula-api/internal/repo/dbutil"
)

// Invite holds the DB columns for a single row in the invites table plus its
// associated redemptions. It is distinct from peligrosa.Invite (which adds
// computed fields and JSON tags) to avoid an import cycle.
type Invite struct {
	Token     string
	Label     string
	CreatedAt time.Time
	CreatedBy string
	ExpiresAt *time.Time
	MaxUses   *int
	Uses      int
	Revoked   bool
}

// Redemption holds one row from the redemptions table.
type Redemption struct {
	Username   string
	JellyfinID string
	RedeemedAt time.Time
}

// InviteWithRedemptions combines an invite row with its audit records.
type InviteWithRedemptions struct {
	Invite
	RedeemedBy []Redemption
}

// ErrNotFound is returned when a token does not exist in the invites table.
var ErrNotFound = errors.New("invite not found")

// Store wraps a *sql.DB and provides named methods for invites/redemptions table
// access. SQLite handles concurrency; no additional mutex is needed.
type Store struct {
	db *sql.DB
}

// New returns a Store backed by db.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying *sql.DB. Callers that need direct DB access for
// test setup (e.g. seeding rows) may use this.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Get returns the invite row for token, including its redemptions.
// Returns ErrNotFound if the token does not exist.
func (s *Store) Get(ctx context.Context, token string) (InviteWithRedemptions, error) {
	var inv Invite
	var createdAt string
	var expiresAt sql.NullString
	var maxUses sql.NullInt64
	var revoked int

	err := s.db.QueryRowContext(ctx,
		`SELECT token, label, created_at, created_by, expires_at, max_uses, uses, revoked
		 FROM invites WHERE token = ?`, token,
	).Scan(&inv.Token, &inv.Label, &createdAt, &inv.CreatedBy,
		&expiresAt, &maxUses, &inv.Uses, &revoked)
	if err == sql.ErrNoRows {
		return InviteWithRedemptions{}, ErrNotFound
	}
	if err != nil {
		return InviteWithRedemptions{}, err
	}
	inv.Revoked = revoked != 0
	if t, parseErr := dbutil.ParseTime(createdAt); parseErr == nil {
		inv.CreatedAt = t
	}
	if expiresAt.Valid {
		if t, parseErr := dbutil.ParseTime(expiresAt.String); parseErr == nil {
			inv.ExpiresAt = &t
		}
	}
	if maxUses.Valid {
		n := int(maxUses.Int64)
		inv.MaxUses = &n
	}

	redemptions, err := s.loadRedemptions(ctx, token)
	if err != nil {
		// Non-fatal: invite data is valid even if redemptions can't be loaded.
		return InviteWithRedemptions{Invite: inv}, nil
	}
	return InviteWithRedemptions{Invite: inv, RedeemedBy: redemptions}, nil
}

// loadRedemptions returns the audit records for a token ordered by redeemed_at.
func (s *Store) loadRedemptions(ctx context.Context, token string) ([]Redemption, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT username, jellyfin_id, redeemed_at FROM redemptions WHERE invite_token = ? ORDER BY redeemed_at`,
		token,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Redemption
	for rows.Next() {
		var r Redemption
		var redeemedAt string
		if err := rows.Scan(&r.Username, &r.JellyfinID, &redeemedAt); err != nil {
			continue
		}
		if t, parseErr := dbutil.ParseTime(redeemedAt); parseErr == nil {
			r.RedeemedAt = t
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ListTokens returns all invite tokens ordered by created_at DESC.
// The caller must close the cursor by iterating to completion; this returns
// a plain slice to avoid holding a cursor open while issuing per-invite queries
// (SQLite MaxOpenConns=1 would deadlock).
func (s *Store) ListTokens(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT token FROM invites ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var tok string
		if err := rows.Scan(&tok); err == nil {
			tokens = append(tokens, tok)
		}
	}
	return tokens, rows.Err()
}

// Create inserts a new invite row. The caller is responsible for generating the
// token and setting all fields; no defaults are applied here.
func (s *Store) Create(ctx context.Context, inv Invite) error {
	var expiresAtStr interface{}
	if inv.ExpiresAt != nil {
		expiresAtStr = dbutil.FormatTime(*inv.ExpiresAt)
	}
	var maxUsesVal interface{}
	if inv.MaxUses != nil {
		maxUsesVal = *inv.MaxUses
	}
	revokedInt := 0
	if inv.Revoked {
		revokedInt = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO invites (token, label, created_at, created_by, expires_at, max_uses, uses, revoked)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		inv.Token, inv.Label, dbutil.FormatTime(inv.CreatedAt),
		inv.CreatedBy, expiresAtStr, maxUsesVal, inv.Uses, revokedInt,
	)
	return err
}

// InsertFull inserts an invite row from a backup export, preserving all fields
// including the token and timestamps. Silently succeeds if the token already
// exists (idempotent restore via INSERT OR IGNORE).
func (s *Store) InsertFull(ctx context.Context, inv Invite) error {
	var expiresAtStr interface{}
	if inv.ExpiresAt != nil {
		expiresAtStr = dbutil.FormatTime(*inv.ExpiresAt)
	}
	var maxUsesVal interface{}
	if inv.MaxUses != nil {
		maxUsesVal = *inv.MaxUses
	}
	revokedInt := 0
	if inv.Revoked {
		revokedInt = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO invites (token, label, created_at, created_by, expires_at, max_uses, uses, revoked)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		inv.Token, inv.Label, dbutil.FormatTime(inv.CreatedAt),
		inv.CreatedBy, expiresAtStr, maxUsesVal, inv.Uses, revokedInt,
	)
	return err
}

// Revoke marks an invite as revoked. Returns ErrNotFound if the token does not
// exist.
func (s *Store) Revoke(ctx context.Context, token string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE invites SET revoked = 1 WHERE token = ?`, token)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete hard-deletes an invite row. Returns ErrNotFound if the token does not
// exist.
func (s *Store) Delete(ctx context.Context, token string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM invites WHERE token = ?`, token)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReserveSlot opens a transaction, validates that the invite is active, and
// pre-increments uses to atomically reserve a redemption slot. It returns the
// raw invite row (so the caller can check its state) and an open *sql.Tx that
// the caller must Commit or Rollback.
//
// On error the transaction is always rolled back and nil is returned.
//
// This is the first phase of the two-phase redemption protocol: reserve a slot
// here → create the Jellyfin user outside the tx → call InsertRedemption.
func (s *Store) ReserveSlot(ctx context.Context, token string) (Invite, *sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Invite{}, nil, err
	}

	var inv Invite
	var createdAt string
	var expiresAt sql.NullString
	var maxUses sql.NullInt64
	var revoked int

	err = tx.QueryRowContext(ctx,
		`SELECT token, label, created_at, created_by, expires_at, max_uses, uses, revoked
		 FROM invites WHERE token = ?`, token,
	).Scan(&inv.Token, &inv.Label, &createdAt, &inv.CreatedBy,
		&expiresAt, &maxUses, &inv.Uses, &revoked)
	if err == sql.ErrNoRows {
		tx.Rollback() //nolint:errcheck
		return Invite{}, nil, ErrNotFound
	}
	if err != nil {
		tx.Rollback() //nolint:errcheck
		return Invite{}, nil, err
	}
	inv.Revoked = revoked != 0
	if t, parseErr := dbutil.ParseTime(createdAt); parseErr == nil {
		inv.CreatedAt = t
	}
	if expiresAt.Valid {
		if t, parseErr := dbutil.ParseTime(expiresAt.String); parseErr == nil {
			inv.ExpiresAt = &t
		}
	}
	if maxUses.Valid {
		n := int(maxUses.Int64)
		inv.MaxUses = &n
	}

	if _, err := tx.ExecContext(ctx, `UPDATE invites SET uses = uses + 1 WHERE token = ?`, token); err != nil {
		tx.Rollback() //nolint:errcheck
		return Invite{}, nil, err
	}

	return inv, tx, nil
}

// ReleaseSlot decrements uses by 1. Called as a compensating action when the
// Jellyfin user creation fails after a slot was reserved.
func (s *Store) ReleaseSlot(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE invites SET uses = uses - 1 WHERE token = ?`, token)
	return err
}

// InsertRedemption records a successful invite redemption in the redemptions
// table. Called as the third phase of the redemption protocol, inside a new
// transaction managed by the caller.
func (s *Store) InsertRedemption(ctx context.Context, token, username, jellyfinID string, redeemedAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO redemptions (invite_token, username, jellyfin_id, redeemed_at) VALUES (?, ?, ?, ?)`,
		token, username, jellyfinID, dbutil.FormatTime(redeemedAt),
	)
	if err != nil {
		tx.Rollback() //nolint:errcheck
		return err
	}
	return tx.Commit()
}
