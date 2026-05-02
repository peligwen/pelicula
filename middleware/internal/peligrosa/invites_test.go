package peligrosa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"pelicula-api/clients"
)

// newTestInviteStore creates an InviteStore backed by a test database.
func newTestInviteStore(t *testing.T) *InviteStore {
	t.Helper()
	db := testDB(t)
	return NewInviteStore(db, nil)
}

// newTestInviteStoreWithClient creates an InviteStore backed by a test database,
// wired to the given JellyfinClient (for tests that exercise Redeem / CreateUser).
func newTestInviteStoreWithClient(t *testing.T, jc clients.JellyfinClient) *InviteStore {
	t.Helper()
	db := testDB(t)
	return NewInviteStore(db, jc)
}

// newTestDeps builds a Deps from a given auth and invite store,
// using a nil RequestStore (sufficient for invite-only tests).
func newTestDeps(auth *Auth, invites *InviteStore) *Deps {
	return &Deps{Auth: auth, Invites: invites}
}

// ── Token helpers ────────────────────────────────────────────────────────────

func TestValidInviteToken(t *testing.T) {
	// Valid: 43 URL-safe base64 chars
	tok := generateInviteToken()
	if len(tok) != 43 {
		t.Errorf("expected 43-char token, got %d: %q", len(tok), tok)
	}
	if !validInviteToken(tok) {
		t.Errorf("generated token rejected: %q", tok)
	}
	// Invalid lengths
	for _, bad := range []string{"", "short", strings.Repeat("a", 42), strings.Repeat("a", 44)} {
		if validInviteToken(bad) {
			t.Errorf("expected invalid: %q", bad)
		}
	}
	// Invalid chars
	if validInviteToken(strings.Repeat("a", 42) + "/") {
		t.Error("token with / should be invalid")
	}
}

// ── Store lifecycle ──────────────────────────────────────────────────────────

func TestCreateAndList(t *testing.T) {
	s := newTestInviteStore(t)
	if got := s.ListInvites(); len(got) != 0 {
		t.Fatalf("expected empty list, got %d", len(got))
	}

	exp := time.Now().Add(24 * time.Hour)
	maxUses := 3
	inv, err := s.CreateInvite(context.Background(), "alice", "For Bob", &exp, &maxUses)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if inv.Token == "" || !validInviteToken(inv.Token) {
		t.Errorf("bad token: %q", inv.Token)
	}
	if inv.Label != "For Bob" || inv.CreatedBy != "alice" || inv.Uses != 0 {
		t.Errorf("unexpected invite: %+v", inv)
	}

	list := s.ListInvites()
	if len(list) != 1 {
		t.Fatalf("expected 1 invite, got %d", len(list))
	}
	if list[0].State != "active" {
		t.Errorf("expected active, got %q", list[0].State)
	}
}

func TestPersistence(t *testing.T) {
	// Both stores share the same DB — verify data written by s1 is visible via s2.
	db := testDB(t)
	s1 := NewInviteStore(db, nil)
	maxUses := 3
	exp := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	inv, err := s1.CreateInvite(context.Background(), "admin", "saved label", &exp, &maxUses)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Second store reading same DB
	s2 := NewInviteStore(db, nil)
	list := s2.ListInvites()
	if len(list) != 1 {
		t.Fatalf("invite not persisted: got %d", len(list))
	}
	got := list[0].Invite
	if got.Token != inv.Token {
		t.Errorf("Token: got %q, want %q", got.Token, inv.Token)
	}
	if got.Label != "saved label" {
		t.Errorf("Label: got %q, want %q", got.Label, "saved label")
	}
	if got.MaxUses == nil || *got.MaxUses != 3 {
		t.Errorf("MaxUses: got %v, want 3", got.MaxUses)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Truncate(time.Second).Equal(exp) {
		t.Errorf("ExpiresAt: got %v, want %v", got.ExpiresAt, exp)
	}
}

func TestExpiryState(t *testing.T) {
	s := newTestInviteStore(t)
	past := time.Now().Add(-time.Hour)
	maxUses := 5
	inv, _ := s.CreateInvite(context.Background(), "admin", "", &past, &maxUses)

	state, found := s.CheckInvite(context.Background(), inv.Token)
	if !found {
		t.Fatal("invite not found")
	}
	if state != "expired" {
		t.Errorf("expected expired, got %q", state)
	}
}

func TestMaxUsesExhausted(t *testing.T) {
	s := newTestInviteStore(t)
	two := 2
	inv, _ := s.CreateInvite(context.Background(), "admin", "", nil, &two)

	// Directly set uses=2 in the DB to simulate exhaustion.
	if _, err := s.db().Exec(`UPDATE invites SET uses = 2 WHERE token = ?`, inv.Token); err != nil {
		t.Fatalf("failed to set uses: %v", err)
	}

	state, _ := s.CheckInvite(context.Background(), inv.Token)
	if state != "exhausted" {
		t.Errorf("expected exhausted, got %q", state)
	}
}

func TestRevokeAndDelete(t *testing.T) {
	s := newTestInviteStore(t)
	maxUses := 1
	inv, _ := s.CreateInvite(context.Background(), "admin", "", nil, &maxUses)

	if err := s.Revoke(context.Background(), inv.Token); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	state, _ := s.CheckInvite(context.Background(), inv.Token)
	if state != "revoked" {
		t.Errorf("expected revoked, got %q", state)
	}

	if err := s.Delete(context.Background(), inv.Token); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, found := s.CheckInvite(context.Background(), inv.Token); found {
		t.Error("invite still present after delete")
	}

	// Not-found errors
	if err := s.Revoke(context.Background(), "notexist"+strings.Repeat("a", 36)); !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found, got %v", err)
	}
}

// ── Redeem race test ─────────────────────────────────────────────────────────

func TestRedeemRace(t *testing.T) {
	// Wire a fake Jellyfin that accepts any user creation with a unique ID per call.
	jc := newFakeJellyfinClient(t, func(mux *http.ServeMux) {
		var counter sync.Mutex
		n := 0
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			counter.Lock()
			n++
			id := n
			counter.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"Id":"00000000-0000-0000-0000-%012d"}`, id)
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})

	s := newTestInviteStoreWithClient(t, jc)

	maxUses := 1
	inv, _ := s.CreateInvite(context.Background(), "admin", "", nil, &maxUses)

	// Launch two concurrent redemptions — with slot reservation exactly one must succeed.
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = s.Redeem(context.Background(), inv.Token, fmt.Sprintf("user%d", idx), "pw123456")
		}(i)
	}
	wg.Wait()

	// Exactly one must succeed, one must fail with ErrInviteNotActive.
	successes := 0
	for _, err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrInviteNotActive) {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Errorf("expected exactly 1 success, got %d (errors: %v, %v)", successes, results[0], results[1])
	}

	// Invite must show exactly 1 use and exactly 1 audit record.
	list := s.ListInvites()
	if len(list) == 0 {
		t.Fatal("invite disappeared")
	}
	if list[0].Uses != 1 {
		t.Errorf("expected Uses=1, got %d", list[0].Uses)
	}
	if len(list[0].RedeemedBy) != 1 {
		t.Errorf("expected 1 RedeemedBy entry, got %d", len(list[0].RedeemedBy))
	}
	if list[0].RedeemedBy[0].JellyfinID == "" {
		t.Error("RedeemedBy[0].JellyfinID should be populated")
	}
}

// ── HTTP handler tests ───────────────────────────────────────────────────────

func TestHandleInvitesCreateAndList(t *testing.T) {
	s := newTestInviteStore(t)

	// Build a shared auth instance with an admin session.
	a := newTestAuth()
	tok := insertSession(a, "admin", RoleAdmin, time.Now().Add(time.Hour))
	deps := newTestDeps(a, s)

	makeReq := func(method string, body any) *http.Request {
		var rb *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rb = bytes.NewReader(b)
		} else {
			rb = bytes.NewReader(nil)
		}
		r := httptest.NewRequest(method, "/api/pelicula/invites", rb)
		r.Header.Set("Content-Type", "application/json")
		addSessionCookie(r, tok)
		return r
	}

	// POST /api/pelicula/invites
	body := map[string]any{"label": "Test invite", "expires_in_hours": 24, "max_uses": 1}
	r := makeReq(http.MethodPost, body)
	w := httptest.NewRecorder()
	deps.HandleInvites(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created Invite
	json.Unmarshal(w.Body.Bytes(), &created)
	if !validInviteToken(created.Token) {
		t.Errorf("bad token in response: %q", created.Token)
	}

	// GET /api/pelicula/invites
	r2 := makeReq(http.MethodGet, nil)
	w2 := httptest.NewRecorder()
	deps.HandleInvites(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", w2.Code)
	}
	var list []InviteWithState
	json.Unmarshal(w2.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Errorf("expected 1 invite, got %d", len(list))
	}
	if list[0].State != "active" {
		t.Errorf("expected active, got %q", list[0].State)
	}
}

func TestHandleInviteCheck(t *testing.T) {
	s := newTestInviteStore(t)
	maxUses := 1
	inv, _ := s.CreateInvite(context.Background(), "admin", "", nil, &maxUses)
	deps := newTestDeps(newTestAuth(), s)

	// Valid token
	r := httptest.NewRequest(http.MethodGet, "/api/pelicula/invites/"+inv.Token+"/check", nil)
	w := httptest.NewRecorder()
	deps.HandleInviteOp(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("check active: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var data map[string]any
	json.Unmarshal(w.Body.Bytes(), &data)
	if data["valid"] != true {
		t.Errorf("expected valid:true, got %v", data)
	}

	// Revoked token → 410
	s.Revoke(context.Background(), inv.Token)
	r2 := httptest.NewRequest(http.MethodGet, "/api/pelicula/invites/"+inv.Token+"/check", nil)
	w2 := httptest.NewRecorder()
	deps.HandleInviteOp(w2, r2)
	if w2.Code != http.StatusGone {
		t.Errorf("check revoked: expected 410, got %d", w2.Code)
	}

	// Malformed token → 400
	r3 := httptest.NewRequest(http.MethodGet, "/api/pelicula/invites/badtoken/check", nil)
	w3 := httptest.NewRecorder()
	deps.HandleInviteOp(w3, r3)
	if w3.Code != http.StatusBadRequest {
		t.Errorf("bad token: expected 400, got %d", w3.Code)
	}
}

// TestHandleInvites_LoopbackCreator verifies that the loopback auto-session path
// (gate 1+2+3 triple) sets created_by = "(loopback)" when creating an invite.
// This exercises the Task 8 migration from a.getSession → a.SessionFor in HandleInvites.
func TestHandleInvites_LoopbackCreator(t *testing.T) {
	withTrustedCIDR(t, "172.17.0.0/16")

	s := newTestInviteStore(t)
	// Auth with no session — loopback path must supply the identity.
	a := newTestAuth()
	deps := newTestDeps(a, s)

	body, _ := json.Marshal(map[string]any{"label": "loopback-test", "max_uses": 1})
	r := httptest.NewRequest(http.MethodPost, "/api/pelicula/invites", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	loopbackTriple(r)

	w := httptest.NewRecorder()
	deps.HandleInvites(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created Invite
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if created.CreatedBy != "(loopback)" {
		t.Errorf("created_by = %q, want (loopback)", created.CreatedBy)
	}
	if !validInviteToken(created.Token) {
		t.Errorf("token is invalid: %q", created.Token)
	}
}

func TestHandleInviteRedeem(t *testing.T) {
	jc := newFakeJellyfinClient(t, func(mux *http.ServeMux) {
		var mu sync.Mutex
		names := map[string]bool{}
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			name, _ := req["Name"].(string)
			mu.Lock()
			defer mu.Unlock()
			if names[name] {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"Message":"A user with the name already exists."}`))
				return
			}
			names[name] = true
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"Id":"00000000-0000-0000-0000-000000000001"}`)
		})
		mux.HandleFunc("/Users/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})

	s := newTestInviteStoreWithClient(t, jc)

	maxUses := 2
	inv, _ := s.CreateInvite(context.Background(), "admin", "", nil, &maxUses)
	deps := newTestDeps(newTestAuth(), s)

	doRedeem := func(username, password string) *httptest.ResponseRecorder {
		body := map[string]string{"username": username, "password": password}
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/api/pelicula/invites/"+inv.Token+"/redeem", bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		deps.HandleInviteOp(w, r)
		return w
	}

	// Successful redemption
	w := doRedeem("alice", "hunter12")
	if w.Code != http.StatusOK {
		t.Fatalf("redeem: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Username taken → 409
	w2 := doRedeem("alice", "hunter12")
	if w2.Code != http.StatusConflict {
		t.Errorf("duplicate name: expected 409, got %d: %s", w2.Code, w2.Body.String())
	}
	var errData map[string]string
	json.Unmarshal(w2.Body.Bytes(), &errData)
	if errData["code"] != "username_taken" {
		t.Errorf("expected code=username_taken, got %v", errData)
	}

	// Second valid user (uses count is 1 after first, max is 2)
	w3 := doRedeem("bob", "hunter12")
	if w3.Code != http.StatusOK {
		t.Errorf("second redeem: expected 200, got %d: %s", w3.Code, w3.Body.String())
	}

	// Exhausted now → 410
	w4 := doRedeem("charlie", "hunter12")
	if w4.Code != http.StatusGone {
		t.Errorf("exhausted: expected 410, got %d: %s", w4.Code, w4.Body.String())
	}

	// Validate uses count
	list := s.ListInvites()
	if list[0].Uses != 2 {
		t.Errorf("expected 2 uses, got %d", list[0].Uses)
	}
}

// ── Rate-limit tests ─────────────────────────────────────────────────────────

// newTestAuthWithRateLimit creates an Auth backed by a real sessionsStore so
// that isRateLimited / recordFailure work in tests.
func newTestAuthWithRateLimit(t *testing.T) *Auth {
	t.Helper()
	return newTestJellyfinAuth(t, nil, nil)
}

// setRemoteIP configures a request so that httputil.ClientIP returns ip.
// It places ip in X-Real-IP and sets RemoteAddr to a docker-bridge address
// that falls within the default TrustedUpstreamCIDR (172.16.0.0/12).
func setRemoteIP(r *http.Request, ip string) {
	r.RemoteAddr = "172.17.0.1:12345"
	r.Header.Set("X-Real-IP", ip)
}

// TestInviteCheck_RateLimit verifies that /check returns 429 after
// rateLimitThreshold probes with non-existent tokens from the same IP.
func TestInviteCheck_RateLimit(t *testing.T) {
	a := newTestAuthWithRateLimit(t)
	s := newTestInviteStore(t)
	deps := newTestDeps(a, s)

	clientIP := "9.8.7.6"

	// Issue rateLimitThreshold+1 GET /check requests with random tokens that
	// are absent from the DB. Each one should record a failure.
	// The (threshold+1)th request must be rate-limited.
	var lastCode int
	for i := 0; i < rateLimitThreshold+1; i++ {
		tok := generateInviteToken()
		r := httptest.NewRequest(http.MethodGet, "/api/pelicula/invites/"+tok+"/check", nil)
		setRemoteIP(r, clientIP)
		w := httptest.NewRecorder()
		deps.HandleInviteOp(w, r)
		lastCode = w.Code
	}

	if lastCode != http.StatusTooManyRequests {
		t.Errorf("expected 429 after %d not-found probes, got %d", rateLimitThreshold+1, lastCode)
	}
}

// TestInviteRedeem_NotFound_Counts verifies that POST /redeem against
// non-existent tokens increments the rate-limit counter, and that the
// (threshold+1)th attempt from the same IP returns 429.
func TestInviteRedeem_NotFound_Counts(t *testing.T) {
	jc := newFakeJellyfinClient(t, func(mux *http.ServeMux) {
		// This Jellyfin won't be reached — the token is not found before CreateUser.
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"Id":"00000000-0000-0000-0000-000000000001"}`)
		})
	})

	a := newTestAuthWithRateLimit(t)
	s := newTestInviteStoreWithClient(t, jc)
	deps := newTestDeps(a, s)

	clientIP := "5.5.5.5"

	doRedeem := func(token string) int {
		body := map[string]string{"username": "alice", "password": "hunter12"}
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/api/pelicula/invites/"+token+"/redeem", bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		setRemoteIP(r, clientIP)
		w := httptest.NewRecorder()
		deps.HandleInviteOp(w, r)
		return w.Code
	}

	var lastCode int
	for i := 0; i < rateLimitThreshold+1; i++ {
		lastCode = doRedeem(generateInviteToken())
	}

	if lastCode != http.StatusTooManyRequests {
		t.Errorf("expected 429 after %d not-found redeem attempts, got %d", rateLimitThreshold+1, lastCode)
	}
}

// TestInviteRedeem_InfraError_DoesNotCount verifies that Jellyfin infrastructure
// errors (5xx / network errors from CreateUser) do NOT increment the rate-limit
// counter, so a flapping backend cannot lock legitimate viewers off the
// redemption page.
func TestInviteRedeem_InfraError_DoesNotCount(t *testing.T) {
	jc := newFakeJellyfinClient(t, func(mux *http.ServeMux) {
		// Return 502 Bad Gateway — triggers the catch-all branch in HandleInviteRedeem.
		// 400 would be mistaken for username-taken; 502 is unambiguously infra.
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		})
	})

	a := newTestAuthWithRateLimit(t)
	s := newTestInviteStoreWithClient(t, jc)
	deps := newTestDeps(a, s)

	// Create a valid active invite with unlimited uses so it stays redeemable.
	inv, err := s.CreateInvite(context.Background(), "admin", "", nil, nil)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	clientIP := "3.3.3.3"

	doRedeem := func() int {
		body := map[string]string{"username": "alice", "password": "hunter12"}
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/api/pelicula/invites/"+inv.Token+"/redeem", bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		setRemoteIP(r, clientIP)
		w := httptest.NewRecorder()
		deps.HandleInviteOp(w, r)
		return w.Code
	}

	// Issue 2*rateLimitThreshold attempts — all should return 502, never 429.
	for i := 0; i < 2*rateLimitThreshold; i++ {
		code := doRedeem()
		if code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d: got 429 — infra errors must not trip the rate limiter", i+1)
		}
		if code != http.StatusBadGateway {
			t.Errorf("attempt %d: expected 502, got %d", i+1, code)
		}
	}
}

// ── Log-redaction tests ───────────────────────────────────────────────────────

// TestInviteLogs_DoNotContainTokenPrefix verifies that invite log lines never
// emit the first 8 characters of a token. This covers three sites in invites.go:
//   - HandleInvites POST (invite created)
//   - HandleInviteRevoke (invite revoked)
//   - Redeem ReleaseSlot failure (warn after Jellyfin error)
func TestInviteLogs_DoNotContainTokenPrefix(t *testing.T) {
	// Capture slog output to a buffer for the duration of this test.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Set up a fake Jellyfin that fails on CreateUser so we can exercise the
	// ReleaseSlot warn path in Redeem.
	jc := newFakeJellyfinClient(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Users/New", func(w http.ResponseWriter, r *http.Request) {
			// Return 502 — causes Redeem to call ReleaseSlot, which logs the warn.
			w.WriteHeader(http.StatusBadGateway)
		})
	})
	s := newTestInviteStoreWithClient(t, jc)

	a := newTestAuth()
	adminTok := insertSession(a, "admin", RoleAdmin, time.Now().Add(time.Hour))
	deps := newTestDeps(a, s)

	// ── Step 1: create an invite via the HTTP handler ──────────────────────────
	body, _ := json.Marshal(map[string]any{"label": "redact-test", "max_uses": 2})
	r := httptest.NewRequest(http.MethodPost, "/api/pelicula/invites", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	addSessionCookie(r, adminTok)
	w := httptest.NewRecorder()
	deps.HandleInvites(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create invite: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created Invite
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("parse created invite: %v", err)
	}
	invToken := created.Token

	// ── Step 2: trigger the Redeem ReleaseSlot warn path ─────────────────────
	// Redeem calls CreateUser which returns 502, so Redeem calls ReleaseSlot and
	// logs a Warn. That log must not contain the token prefix.
	_ = s.Redeem(context.Background(), invToken, "alice", "hunter12")

	// ── Step 3: revoke the invite via the HTTP handler ────────────────────────
	r2 := httptest.NewRequest(http.MethodPost, "/api/pelicula/invites/"+invToken+"/revoke", nil)
	addSessionCookie(r2, adminTok)
	w2 := httptest.NewRecorder()
	deps.HandleInviteOp(w2, r2)
	if w2.Code != http.StatusNoContent {
		t.Fatalf("revoke invite: expected 204, got %d: %s", w2.Code, w2.Body.String())
	}

	// ── Assert: no 8-char prefix of any token appears in captured log output ──
	logged := buf.Bytes()
	if len(invToken) < 8 {
		t.Fatalf("token too short for prefix check: %q", invToken)
	}
	prefix := invToken[:8]
	if bytes.Contains(logged, []byte(prefix)) {
		t.Errorf("log output contains token prefix %q — token material must not appear in logs\nlog output:\n%s",
			prefix, logged)
	}
}
