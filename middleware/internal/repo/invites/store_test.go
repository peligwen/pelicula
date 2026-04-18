package invites_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"pelicula-api/internal/repo/invites"
)

// newTestDB opens an in-memory SQLite database with the invites and redemptions
// tables. MaxOpenConns=1 mirrors production (avoids SQLite locking issues in tests).
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		t.Fatalf("WAL mode: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		db.Close()
		t.Fatalf("foreign_keys: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE invites (
			token      TEXT PRIMARY KEY,
			label      TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			created_by TEXT NOT NULL DEFAULT '',
			expires_at TEXT,
			max_uses   INTEGER,
			uses       INTEGER NOT NULL DEFAULT 0,
			revoked    INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE redemptions (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			invite_token TEXT NOT NULL REFERENCES invites(token) ON DELETE CASCADE,
			username     TEXT NOT NULL,
			jellyfin_id  TEXT NOT NULL,
			redeemed_at  TEXT NOT NULL
		);
	`); err != nil {
		db.Close()
		t.Fatalf("create tables: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// makeInvite constructs a minimal Invite for insertion.
func makeInvite(token string, uses int, maxUses *int, expiresAt *time.Time, revoked bool) invites.Invite {
	one := 1
	if maxUses == nil {
		maxUses = &one
	}
	return invites.Invite{
		Token:     token,
		Label:     "test-label",
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		CreatedBy: "admin",
		ExpiresAt: expiresAt,
		MaxUses:   maxUses,
		Uses:      uses,
		Revoked:   revoked,
	}
}

func token(n int) string {
	// 43-char URL-safe base64: use padding-free fixed strings
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	b := make([]byte, 43)
	for i := range b {
		b[i] = alpha[(n+i)%len(alpha)]
	}
	return string(b)
}

// ── Create + Get ─────────────────────────────────────────────────────────────

func TestCreate_Get(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := invites.New(newTestDB(t))

	exp := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	maxUses := 3
	inv := makeInvite(token(0), 0, &maxUses, &exp, false)

	if err := s.Create(ctx, inv); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get(ctx, inv.Token)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Token != inv.Token {
		t.Errorf("Token: got %q, want %q", got.Token, inv.Token)
	}
	if got.Label != inv.Label {
		t.Errorf("Label: got %q, want %q", got.Label, inv.Label)
	}
	if got.CreatedBy != "admin" {
		t.Errorf("CreatedBy: got %q, want admin", got.CreatedBy)
	}
	if got.MaxUses == nil || *got.MaxUses != 3 {
		t.Errorf("MaxUses: got %v, want 3", got.MaxUses)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Truncate(time.Second).Equal(exp) {
		t.Errorf("ExpiresAt: got %v, want %v", got.ExpiresAt, exp)
	}
	if got.Revoked {
		t.Error("Revoked should be false")
	}
	if len(got.RedeemedBy) != 0 {
		t.Errorf("expected no redemptions, got %d", len(got.RedeemedBy))
	}
}

func TestGet_NotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := invites.New(newTestDB(t))

	_, err := s.Get(ctx, token(99))
	if !errors.Is(err, invites.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ── ListTokens ───────────────────────────────────────────────────────────────

func TestListTokens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name   string
		tokens []string
	}{
		{name: "empty", tokens: nil},
		{name: "one", tokens: []string{token(1)}},
		{name: "three", tokens: []string{token(2), token(3), token(4)}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := invites.New(newTestDB(t))
			for _, tok := range tc.tokens {
				if err := s.Create(ctx, makeInvite(tok, 0, nil, nil, false)); err != nil {
					t.Fatalf("Create %q: %v", tok, err)
				}
			}
			got, err := s.ListTokens(ctx)
			if err != nil {
				t.Fatalf("ListTokens: %v", err)
			}
			if len(got) != len(tc.tokens) {
				t.Errorf("len = %d, want %d", len(got), len(tc.tokens))
			}
		})
	}
}

// ── InsertFull ───────────────────────────────────────────────────────────────

func TestInsertFull_Idempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := invites.New(newTestDB(t))

	inv := makeInvite(token(10), 2, nil, nil, true)

	if err := s.InsertFull(ctx, inv); err != nil {
		t.Fatalf("InsertFull first: %v", err)
	}
	// Second call with same token must not error (INSERT OR IGNORE).
	if err := s.InsertFull(ctx, inv); err != nil {
		t.Fatalf("InsertFull second (idempotent): %v", err)
	}

	got, err := s.Get(ctx, inv.Token)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Uses != 2 {
		t.Errorf("Uses: got %d, want 2", got.Uses)
	}
	if !got.Revoked {
		t.Error("Revoked should be true (preserved from backup)")
	}
}

// ── Revoke ───────────────────────────────────────────────────────────────────

func TestRevoke(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := invites.New(newTestDB(t))

	tok := token(20)
	if err := s.Create(ctx, makeInvite(tok, 0, nil, nil, false)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.Revoke(ctx, tok); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	got, err := s.Get(ctx, tok)
	if err != nil {
		t.Fatalf("Get after revoke: %v", err)
	}
	if !got.Revoked {
		t.Error("Revoked should be true after Revoke")
	}
}

func TestRevoke_NotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := invites.New(newTestDB(t))

	err := s.Revoke(ctx, token(21))
	if !errors.Is(err, invites.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ── Delete ───────────────────────────────────────────────────────────────────

func TestDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := invites.New(newTestDB(t))

	tok := token(30)
	if err := s.Create(ctx, makeInvite(tok, 0, nil, nil, false)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.Delete(ctx, tok); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := s.Get(ctx, tok)
	if !errors.Is(err, invites.ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := invites.New(newTestDB(t))

	err := s.Delete(ctx, token(31))
	if !errors.Is(err, invites.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ── ReserveSlot + ReleaseSlot + InsertRedemption ─────────────────────────────

func TestReserveSlot_Basic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := invites.New(newTestDB(t))

	maxUses := 2
	tok := token(40)
	if err := s.Create(ctx, makeInvite(tok, 0, &maxUses, nil, false)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	inv, tx, err := s.ReserveSlot(ctx, tok)
	if err != nil {
		t.Fatalf("ReserveSlot: %v", err)
	}
	if inv.Token != tok {
		t.Errorf("Token: got %q, want %q", inv.Token, tok)
	}
	// Commit the slot reservation.
	if err := tx.Commit(); err != nil {
		t.Fatalf("tx.Commit: %v", err)
	}

	// Uses should now be 1 in the DB.
	got, _ := s.Get(ctx, tok)
	if got.Uses != 1 {
		t.Errorf("Uses after reserve: got %d, want 1", got.Uses)
	}
}

func TestReserveSlot_NotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := invites.New(newTestDB(t))

	_, _, err := s.ReserveSlot(ctx, token(41))
	if !errors.Is(err, invites.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestReleaseSlot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := invites.New(newTestDB(t))

	tok := token(50)
	if err := s.Create(ctx, makeInvite(tok, 0, nil, nil, false)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, tx, err := s.ReserveSlot(ctx, tok)
	if err != nil {
		t.Fatalf("ReserveSlot: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("tx.Commit: %v", err)
	}

	// Simulate Jellyfin failure: release the slot.
	if err := s.ReleaseSlot(ctx, tok); err != nil {
		t.Fatalf("ReleaseSlot: %v", err)
	}

	got, _ := s.Get(ctx, tok)
	if got.Uses != 0 {
		t.Errorf("Uses after release: got %d, want 0", got.Uses)
	}
}

func TestInsertRedemption(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := invites.New(newTestDB(t))

	tok := token(60)
	if err := s.Create(ctx, makeInvite(tok, 0, nil, nil, false)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	redeemedAt := time.Now().UTC().Truncate(time.Second)
	if err := s.InsertRedemption(ctx, tok, "alice", "jf-id-001", redeemedAt); err != nil {
		t.Fatalf("InsertRedemption: %v", err)
	}

	got, err := s.Get(ctx, tok)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.RedeemedBy) != 1 {
		t.Fatalf("expected 1 redemption, got %d", len(got.RedeemedBy))
	}
	r := got.RedeemedBy[0]
	if r.Username != "alice" {
		t.Errorf("Username: got %q, want alice", r.Username)
	}
	if r.JellyfinID != "jf-id-001" {
		t.Errorf("JellyfinID: got %q, want jf-id-001", r.JellyfinID)
	}
	if !r.RedeemedAt.Truncate(time.Second).Equal(redeemedAt) {
		t.Errorf("RedeemedAt: got %v, want %v", r.RedeemedAt, redeemedAt)
	}
}

// ── CAS sequential exhaustion ─────────────────────────────────────────────────

// TestReserveSlot_SequentialExhaustion verifies the logical invariant of the
// two-phase slot reservation protocol: with max_uses=1, exactly one caller
// succeeds and the second is correctly rejected.
//
// Despite the goroutine wrapper this is a sequential test, not a concurrent
// one. SQLite's single-writer constraint means MaxOpenConns=1 serialises both
// goroutines at the connection pool — each ReserveSlot transaction completes
// before the next begins. True concurrent transaction testing requires WAL mode
// with multiple connections open simultaneously; that is tested separately in
// integration. Here we validate only the logical ordering invariant: the second
// caller sees uses=1 (committed by the first) and therefore rolls back.
func TestReserveSlot_SequentialExhaustion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := invites.New(newTestDB(t))

	maxUses := 1
	tok := token(70)
	if err := s.Create(ctx, makeInvite(tok, 0, &maxUses, nil, false)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// reserveAndCommit simulates the full slot-reservation protocol:
	// ReserveSlot → check active → commit (active) or rollback (exhausted).
	// Returns true if the slot was successfully taken.
	reserveAndCommit := func() (bool, error) {
		inv, tx, err := s.ReserveSlot(ctx, tok)
		if err != nil {
			return false, err
		}
		// Replicate the active-state check peligrosa does.
		exhausted := inv.MaxUses != nil && inv.Uses >= *inv.MaxUses
		if exhausted || inv.Revoked {
			tx.Rollback() //nolint:errcheck
			return false, nil
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
	}

	results := make([]bool, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = reserveAndCommit()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	successes := 0
	for _, ok := range results {
		if ok {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("expected exactly 1 successful reservation, got %d (results: %v)", successes, results)
	}

	// uses must be exactly 1 in the DB.
	got, err := s.Get(ctx, tok)
	if err != nil {
		t.Fatalf("Get after race: %v", err)
	}
	if got.Uses != 1 {
		t.Errorf("uses = %d after race; expected 1", got.Uses)
	}
}

// TestReserveSlot_ExhaustionGate tests the full protocol: reserve → check state
// → proceed or rollback. With max_uses=1, the second reserver must see uses=1
// at read time (after the first committed) and therefore the state check blocks it.
func TestReserveSlot_ExhaustionGate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := invites.New(newTestDB(t))

	maxUses := 1
	tok := token(80)
	if err := s.Create(ctx, makeInvite(tok, 0, &maxUses, nil, false)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// First reserver: get the slot (uses was 0 when read) → commit.
	inv1, tx1, err := s.ReserveSlot(ctx, tok)
	if err != nil {
		t.Fatalf("first ReserveSlot: %v", err)
	}
	// Invite appears active (uses=0 at read time, max_uses=1).
	if inv1.Uses != 0 {
		t.Errorf("first reserver: Uses at read = %d, want 0", inv1.Uses)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatalf("tx1.Commit: %v", err)
	}

	// Second reserver: uses=1 at read time, max_uses=1 → should be exhausted.
	inv2, tx2, err := s.ReserveSlot(ctx, tok)
	if err != nil {
		t.Fatalf("second ReserveSlot: %v", err)
	}
	defer tx2.Rollback() //nolint:errcheck
	// The returned invite row reflects uses=1 at read time.
	if inv2.Uses != 1 {
		t.Errorf("second reserver: Uses at read = %d, want 1", inv2.Uses)
	}
	// The caller (peligrosa) would check inv2.isActive() — here we replicate that:
	exhausted := inv2.MaxUses != nil && inv2.Uses >= *inv2.MaxUses
	if !exhausted {
		t.Error("second reserver should see exhausted invite (uses >= max_uses)")
	}
}
