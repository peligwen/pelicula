package sessions_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"pelicula-api/internal/repo/sessions"
)

// newTestDB opens an in-memory SQLite database with the sessions and
// rate_limits tables. MaxOpenConns=1 mirrors production behaviour.
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
	if _, err := db.Exec(`
		CREATE TABLE sessions (
			token      TEXT PRIMARY KEY,
			username   TEXT NOT NULL,
			role       TEXT NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		);
		CREATE TABLE rate_limits (
			ip           TEXT PRIMARY KEY,
			fail_count   INTEGER NOT NULL DEFAULT 0,
			window_start TEXT NOT NULL
		);
	`); err != nil {
		db.Close()
		t.Fatalf("create tables: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// ── Create + Lookup round-trip ────────────────────────────────────────────────

func TestCreate_Lookup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := sessions.New(newTestDB(t))

	expiry := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	if err := s.Create(ctx, "tok-abc", "alice", "admin", expiry); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Lookup(ctx, "tok-abc")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil {
		t.Fatal("Lookup returned nil, want session")
	}
	if got.Token != "tok-abc" {
		t.Errorf("Token: got %q, want tok-abc", got.Token)
	}
	if got.Username != "alice" {
		t.Errorf("Username: got %q, want alice", got.Username)
	}
	if got.Role != "admin" {
		t.Errorf("Role: got %q, want admin", got.Role)
	}
	if !got.ExpiresAt.Truncate(time.Second).Equal(expiry) {
		t.Errorf("ExpiresAt: got %v, want %v", got.ExpiresAt, expiry)
	}
}

func TestLookup_NotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := sessions.New(newTestDB(t))

	got, err := s.Lookup(ctx, "no-such-token")
	if err != nil {
		t.Fatalf("Lookup: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("Lookup for missing token: got %+v, want nil", got)
	}
}

// ── LookupActive ─────────────────────────────────────────────────────────────

func TestLookupActive_FiltersExpired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := sessions.New(newTestDB(t))

	if err := s.Create(ctx, "tok-valid", "alice", "viewer", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Create valid: %v", err)
	}
	if err := s.Create(ctx, "tok-expired", "bob", "viewer", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("Create expired: %v", err)
	}

	active, err := s.LookupActive(ctx)
	if err != nil {
		t.Fatalf("LookupActive: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("LookupActive: got %d sessions, want 1", len(active))
	}
	if active[0].Token != "tok-valid" {
		t.Errorf("active session token: got %q, want tok-valid", active[0].Token)
	}
}

// ── Rotate ────────────────────────────────────────────────────────────────────

func TestRotate_OldGoneNewWorks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := sessions.New(newTestDB(t))

	oldExpiry := time.Now().Add(time.Hour)
	if err := s.Create(ctx, "tok-old", "alice", "manager", oldExpiry); err != nil {
		t.Fatalf("Create: %v", err)
	}

	newExpiry := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	if err := s.Rotate(ctx, "tok-old", "tok-new", newExpiry); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// Old token must be gone.
	old, err := s.Lookup(ctx, "tok-old")
	if err != nil {
		t.Fatalf("Lookup old: %v", err)
	}
	if old != nil {
		t.Error("old token still present after rotate")
	}

	// New token must work.
	got, err := s.Lookup(ctx, "tok-new")
	if err != nil {
		t.Fatalf("Lookup new: %v", err)
	}
	if got == nil {
		t.Fatal("new token not found after rotate")
	}
	if got.Username != "alice" {
		t.Errorf("Username after rotate: got %q, want alice", got.Username)
	}
	if got.Role != "manager" {
		t.Errorf("Role after rotate: got %q, want manager", got.Role)
	}
	if !got.ExpiresAt.Truncate(time.Second).Equal(newExpiry) {
		t.Errorf("ExpiresAt after rotate: got %v, want %v", got.ExpiresAt, newExpiry)
	}
}

func TestRotate_UnknownTokenErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := sessions.New(newTestDB(t))

	err := s.Rotate(ctx, "tok-missing", "tok-new", time.Now().Add(time.Hour))
	if err == nil {
		t.Error("Rotate with unknown old token: expected error, got nil")
	}
}

// ── Revoke ────────────────────────────────────────────────────────────────────

func TestRevoke(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := sessions.New(newTestDB(t))

	if err := s.Create(ctx, "tok-abc", "alice", "viewer", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.Revoke(ctx, "tok-abc"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	got, err := s.Lookup(ctx, "tok-abc")
	if err != nil {
		t.Fatalf("Lookup after revoke: %v", err)
	}
	if got != nil {
		t.Error("session still present after revoke")
	}
}

func TestRevoke_NoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := sessions.New(newTestDB(t))

	// Revoking a non-existent token must not error.
	if err := s.Revoke(ctx, "tok-nonexistent"); err != nil {
		t.Fatalf("Revoke non-existent: unexpected error: %v", err)
	}
}

// ── RevokeByUsername ──────────────────────────────────────────────────────────

func TestRevokeByUsername(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := sessions.New(newTestDB(t))

	if err := s.Create(ctx, "tok-a1", "alice", "viewer", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Create a1: %v", err)
	}
	if err := s.Create(ctx, "tok-a2", "alice", "viewer", time.Now().Add(2*time.Hour)); err != nil {
		t.Fatalf("Create a2: %v", err)
	}
	if err := s.Create(ctx, "tok-b1", "bob", "viewer", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Create b1: %v", err)
	}

	if err := s.RevokeByUsername(ctx, "alice"); err != nil {
		t.Fatalf("RevokeByUsername: %v", err)
	}

	// Both alice sessions must be gone.
	for _, tok := range []string{"tok-a1", "tok-a2"} {
		got, err := s.Lookup(ctx, tok)
		if err != nil {
			t.Fatalf("Lookup %s: %v", tok, err)
		}
		if got != nil {
			t.Errorf("alice session %q still present after RevokeByUsername", tok)
		}
	}

	// Bob's session must be intact.
	got, err := s.Lookup(ctx, "tok-b1")
	if err != nil {
		t.Fatalf("Lookup bob: %v", err)
	}
	if got == nil {
		t.Error("bob's session removed — should not have been affected")
	}
}

// ── PruneExpired ─────────────────────────────────────────────────────────────

func TestPruneExpired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := sessions.New(newTestDB(t))

	if err := s.Create(ctx, "tok-fresh", "alice", "viewer", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Create fresh: %v", err)
	}
	if err := s.Create(ctx, "tok-stale1", "bob", "viewer", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("Create stale1: %v", err)
	}
	if err := s.Create(ctx, "tok-stale2", "carol", "admin", time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatalf("Create stale2: %v", err)
	}

	if err := s.PruneExpired(ctx); err != nil {
		t.Fatalf("PruneExpired: %v", err)
	}

	// Fresh session must survive.
	got, err := s.Lookup(ctx, "tok-fresh")
	if err != nil {
		t.Fatalf("Lookup fresh: %v", err)
	}
	if got == nil {
		t.Error("fresh session was pruned — should have survived")
	}

	// Stale sessions must be gone.
	for _, tok := range []string{"tok-stale1", "tok-stale2"} {
		got, err := s.Lookup(ctx, tok)
		if err != nil {
			t.Fatalf("Lookup %s: %v", tok, err)
		}
		if got != nil {
			t.Errorf("stale session %q still present after prune", tok)
		}
	}
}

// ── RateLimitUpsert ──────────────────────────────────────────────────────────

func TestRateLimitUpsert_Increments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := sessions.New(newTestDB(t))

	window := time.Now().Add(-5 * time.Minute)
	ip := "1.2.3.4"

	// First call: insert with count=1.
	count, err := s.RateLimitUpsert(ctx, ip, window)
	if err != nil {
		t.Fatalf("RateLimitUpsert first: %v", err)
	}
	if count != 1 {
		t.Errorf("first call: count = %d, want 1", count)
	}

	// Second call: must increment to 2.
	count, err = s.RateLimitUpsert(ctx, ip, window)
	if err != nil {
		t.Fatalf("RateLimitUpsert second: %v", err)
	}
	if count != 2 {
		t.Errorf("second call: count = %d, want 2", count)
	}

	// Third call.
	count, err = s.RateLimitUpsert(ctx, ip, window)
	if err != nil {
		t.Fatalf("RateLimitUpsert third: %v", err)
	}
	if count != 3 {
		t.Errorf("third call: count = %d, want 3", count)
	}
}

func TestRateLimitUpsert_ResetsOnExpiredWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	s := sessions.New(db)

	ip := "5.6.7.8"

	// Directly insert a row with window_start set to 10 minutes ago to simulate
	// failures that occurred outside the current 5-minute window.
	oldTime := time.Now().Add(-10 * time.Minute).UTC()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO rate_limits (ip, fail_count, window_start) VALUES (?, 3, ?)`,
		ip, oldTime.Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("seed old row: %v", err)
	}

	// A new failure with a boundary that is after the stored window_start resets the counter.
	newWindow := time.Now().Add(-1 * time.Minute)
	count, err := s.RateLimitUpsert(ctx, ip, newWindow)
	if err != nil {
		t.Fatalf("RateLimitUpsert new window: %v", err)
	}
	if count != 1 {
		t.Errorf("count after window reset = %d, want 1", count)
	}
}

func TestRateLimitUpsert_IndependentIPs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := sessions.New(newTestDB(t))

	window := time.Now().Add(-5 * time.Minute)
	ip1, ip2 := "10.0.0.1", "10.0.0.2"

	for range 3 {
		if _, err := s.RateLimitUpsert(ctx, ip1, window); err != nil {
			t.Fatalf("ip1 upsert: %v", err)
		}
	}
	count, err := s.RateLimitUpsert(ctx, ip2, window)
	if err != nil {
		t.Fatalf("ip2 upsert: %v", err)
	}
	if count != 1 {
		t.Errorf("ip2 count = %d, want 1 (independent from ip1)", count)
	}
}

// ── PruneRateLimit ────────────────────────────────────────────────────────────

func TestPruneRateLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	s := sessions.New(db)

	// Directly insert rows with controlled timestamps to test pruning:
	//   old-ip: window_start 10 minutes ago (should be pruned by a 5-min cutoff)
	//   recent-ip: window_start 1 minute ago (should survive)
	oldStart := time.Now().Add(-10 * time.Minute).UTC()
	recentStart := time.Now().Add(-1 * time.Minute).UTC()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO rate_limits (ip, fail_count, window_start) VALUES (?, 1, ?)`,
		"old-ip", oldStart.Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("insert old-ip: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO rate_limits (ip, fail_count, window_start) VALUES (?, 1, ?)`,
		"recent-ip", recentStart.Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("insert recent-ip: %v", err)
	}

	// Prune entries older than 5 minutes ago.
	cutoff := time.Now().Add(-5 * time.Minute)
	if err := s.PruneRateLimit(ctx, cutoff); err != nil {
		t.Fatalf("PruneRateLimit: %v", err)
	}

	// old-ip must be gone — verify by upserting and getting count=1 (fresh insert).
	count, err := s.RateLimitUpsert(ctx, "old-ip", time.Now().Add(-1*time.Minute))
	if err != nil {
		t.Fatalf("upsert old-ip after prune: %v", err)
	}
	if count != 1 {
		t.Errorf("old-ip count after prune = %d, want 1 (row was pruned, re-inserted fresh)", count)
	}

	// recent-ip must still be present (not pruned) — upsert should increment to 2.
	// Use a boundary of 2 minutes to ensure recent-ip (inserted 1 min ago) is
	// clearly within the active window.
	count, err = s.RateLimitUpsert(ctx, "recent-ip", time.Now().Add(-2*time.Minute))
	if err != nil {
		t.Fatalf("upsert recent-ip after prune: %v", err)
	}
	if count != 2 {
		t.Errorf("recent-ip count after prune = %d, want 2 (row survived prune)", count)
	}
}
