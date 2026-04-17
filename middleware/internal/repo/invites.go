package repo

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"pelicula-api/internal/repo/dbutil"
)

// ErrInviteNotFound is returned by invite methods when the token does not exist.
var ErrInviteNotFound = errors.New("invite not found")

// Invite is the persisted form of an invite token.
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

// Redemption records a single successful use of an invite.
type Redemption struct {
	InviteToken string
	Username    string
	JellyfinID  string
	RedeemedAt  time.Time
}

// InviteStore persists invite tokens in SQLite.
type InviteStore struct{ db *sql.DB }

// NewInviteStore creates an InviteStore backed by db.
func NewInviteStore(db *sql.DB) *InviteStore { return &InviteStore{db: db} }

// Create inserts a new invite row.
func (s *InviteStore) Create(ctx context.Context, inv Invite) error {
	var expiresAt any
	if inv.ExpiresAt != nil {
		expiresAt = dbutil.FormatTime(*inv.ExpiresAt)
	}
	var maxUses any
	if inv.MaxUses != nil {
		maxUses = *inv.MaxUses
	}
	revoked := 0
	if inv.Revoked {
		revoked = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO invites (token, label, created_at, created_by, expires_at, max_uses, uses, revoked)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		inv.Token, inv.Label,
		dbutil.FormatTime(inv.CreatedAt),
		inv.CreatedBy,
		expiresAt, maxUses, inv.Uses, revoked,
	)
	return err
}

// InsertOrIgnore inserts an invite row, silently succeeding if the token already exists.
// Used by the backup restore path.
func (s *InviteStore) InsertOrIgnore(ctx context.Context, inv Invite) error {
	var expiresAt any
	if inv.ExpiresAt != nil {
		expiresAt = dbutil.FormatTime(*inv.ExpiresAt)
	}
	var maxUses any
	if inv.MaxUses != nil {
		maxUses = *inv.MaxUses
	}
	revoked := 0
	if inv.Revoked {
		revoked = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO invites (token, label, created_at, created_by, expires_at, max_uses, uses, revoked)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		inv.Token, inv.Label,
		dbutil.FormatTime(inv.CreatedAt),
		inv.CreatedBy,
		expiresAt, maxUses, inv.Uses, revoked,
	)
	return err
}

// Lookup returns the invite for token, or ErrInviteNotFound.
func (s *InviteStore) Lookup(ctx context.Context, token string) (*Invite, error) {
	var inv Invite
	var createdAtStr string
	var expiresAtStr sql.NullString
	var maxUses sql.NullInt64
	var revoked int

	err := s.db.QueryRowContext(ctx,
		`SELECT token, label, created_at, created_by, expires_at, max_uses, uses, revoked
		 FROM invites WHERE token = ?`, token,
	).Scan(&inv.Token, &inv.Label, &createdAtStr, &inv.CreatedBy,
		&expiresAtStr, &maxUses, &inv.Uses, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInviteNotFound
	}
	if err != nil {
		return nil, err
	}
	inv.Revoked = revoked != 0
	if t, err := dbutil.ParseTime(createdAtStr); err == nil {
		inv.CreatedAt = t
	}
	if expiresAtStr.Valid {
		if t, err := dbutil.ParseTime(expiresAtStr.String); err == nil {
			inv.ExpiresAt = &t
		}
	}
	if maxUses.Valid {
		n := int(maxUses.Int64)
		inv.MaxUses = &n
	}
	return &inv, nil
}

// ListTokens returns all invite tokens ordered by created_at DESC.
func (s *InviteStore) ListTokens(ctx context.Context) ([]string, error) {
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

// IncrementUses atomically increments the uses counter for token.
func (s *InviteStore) IncrementUses(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE invites SET uses = uses + 1 WHERE token = ?`, token)
	return err
}

// DecrementUses atomically decrements the uses counter (slot release on failure).
func (s *InviteStore) DecrementUses(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE invites SET uses = uses - 1 WHERE token = ?`, token)
	return err
}

// Revoke marks an invite as revoked. Returns ErrInviteNotFound if the token does not exist.
func (s *InviteStore) Revoke(ctx context.Context, token string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE invites SET revoked = 1 WHERE token = ?`, token)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrInviteNotFound
	}
	return nil
}

// Delete hard-deletes an invite record. Returns ErrInviteNotFound if absent.
func (s *InviteStore) Delete(ctx context.Context, token string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM invites WHERE token = ?`, token)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrInviteNotFound
	}
	return nil
}

// InsertRedemption records a single invite redemption.
func (s *InviteStore) InsertRedemption(ctx context.Context, r Redemption) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO redemptions (invite_token, username, jellyfin_id, redeemed_at)
		 VALUES (?, ?, ?, ?)`,
		r.InviteToken, r.Username, r.JellyfinID,
		dbutil.FormatTime(r.RedeemedAt),
	)
	return err
}

// ListRedemptions returns all redemptions for token, ordered by redeemed_at.
func (s *InviteStore) ListRedemptions(ctx context.Context, token string) ([]Redemption, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT username, jellyfin_id, redeemed_at FROM redemptions
		 WHERE invite_token = ? ORDER BY redeemed_at`,
		token,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Redemption
	for rows.Next() {
		var r Redemption
		var redeemedAtStr string
		if err := rows.Scan(&r.Username, &r.JellyfinID, &redeemedAtStr); err != nil {
			continue
		}
		if t, err := dbutil.ParseTime(redeemedAtStr); err == nil {
			r.RedeemedAt = t
		}
		r.InviteToken = token
		out = append(out, r)
	}
	return out, rows.Err()
}
