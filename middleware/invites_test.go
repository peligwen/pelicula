package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestInviteStore creates an InviteStore backed by a test database.
// A JellyfinClient is built from the global services so that tests using
// newFakeJellyfin + resetServices get a correctly-wired client.
func newTestInviteStore(t *testing.T) *InviteStore {
	t.Helper()
	db := testDB(t)
	jc := NewJellyfinHTTPClient(http.DefaultClient, services)
	return NewInviteStore(db, jc)
}

// setInviteStore replaces the package-level inviteStore for the duration of a test.
func setInviteStore(t *testing.T, s *InviteStore) {
	t.Helper()
	orig := inviteStore
	inviteStore = s
	t.Cleanup(func() { inviteStore = orig })
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
	inv, err := s.CreateInvite("alice", "For Bob", &exp, &maxUses)
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
	inv, err := s1.CreateInvite("admin", "saved label", &exp, &maxUses)
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
	inv, _ := s.CreateInvite("admin", "", &past, &maxUses)

	state, found := s.CheckInvite(inv.Token)
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
	inv, _ := s.CreateInvite("admin", "", nil, &two)

	// Directly set uses=2 in the DB to simulate exhaustion.
	if _, err := s.db.Exec(`UPDATE invites SET uses = 2 WHERE token = ?`, inv.Token); err != nil {
		t.Fatalf("failed to set uses: %v", err)
	}

	state, _ := s.CheckInvite(inv.Token)
	if state != "exhausted" {
		t.Errorf("expected exhausted, got %q", state)
	}
}

func TestRevokeAndDelete(t *testing.T) {
	s := newTestInviteStore(t)
	maxUses := 1
	inv, _ := s.CreateInvite("admin", "", nil, &maxUses)

	if err := s.Revoke(inv.Token); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	state, _ := s.CheckInvite(inv.Token)
	if state != "revoked" {
		t.Errorf("expected revoked, got %q", state)
	}

	if err := s.Delete(inv.Token); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, found := s.CheckInvite(inv.Token); found {
		t.Error("invite still present after delete")
	}

	// Not-found errors
	if err := s.Revoke("notexist" + strings.Repeat("a", 36)); !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found, got %v", err)
	}
}

// ── Redeem race test ─────────────────────────────────────────────────────────

func TestRedeemRace(t *testing.T) {
	// Wire a fake Jellyfin that accepts any user creation with a unique ID per call.
	_ = newFakeJellyfin(t, func(mux *http.ServeMux) {
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
	resetServices(t)

	s := newTestInviteStore(t)
	setInviteStore(t, s)

	maxUses := 1
	inv, _ := s.CreateInvite("admin", "", nil, &maxUses)

	// Launch two concurrent redemptions — with slot reservation exactly one must succeed.
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = s.Redeem(inv.Token, fmt.Sprintf("user%d", idx), "pw123456")
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

func inviteAdminRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	var rb *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rb = bytes.NewReader(b)
	} else {
		rb = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, rb)
	r.Header.Set("Content-Type", "application/json")
	// Inject admin session
	a := newTestAuth("jellyfin")
	tok := insertSession(a, "admin", RoleAdmin, time.Now().Add(time.Hour))
	orig := authMiddleware
	authMiddleware = a
	t.Cleanup(func() { authMiddleware = orig })
	addSessionCookie(r, tok)
	return r
}

func TestHandleInvitesCreateAndList(t *testing.T) {
	s := newTestInviteStore(t)
	setInviteStore(t, s)

	// POST /api/pelicula/invites
	body := map[string]any{"label": "Test invite", "expires_in_hours": 24, "max_uses": 1}
	r := inviteAdminRequest(t, http.MethodPost, "/api/pelicula/invites", body)
	w := httptest.NewRecorder()
	handleInvites(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created Invite
	json.Unmarshal(w.Body.Bytes(), &created)
	if !validInviteToken(created.Token) {
		t.Errorf("bad token in response: %q", created.Token)
	}

	// GET /api/pelicula/invites
	r2 := inviteAdminRequest(t, http.MethodGet, "/api/pelicula/invites", nil)
	w2 := httptest.NewRecorder()
	handleInvites(w2, r2)
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
	setInviteStore(t, s)
	maxUses := 1
	inv, _ := s.CreateInvite("admin", "", nil, &maxUses)

	// Valid token
	r := httptest.NewRequest(http.MethodGet, "/api/pelicula/invites/"+inv.Token+"/check", nil)
	w := httptest.NewRecorder()
	handleInviteOp(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("check active: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var data map[string]any
	json.Unmarshal(w.Body.Bytes(), &data)
	if data["valid"] != true {
		t.Errorf("expected valid:true, got %v", data)
	}

	// Revoked token → 410
	s.Revoke(inv.Token)
	r2 := httptest.NewRequest(http.MethodGet, "/api/pelicula/invites/"+inv.Token+"/check", nil)
	w2 := httptest.NewRecorder()
	handleInviteOp(w2, r2)
	if w2.Code != http.StatusGone {
		t.Errorf("check revoked: expected 410, got %d", w2.Code)
	}

	// Malformed token → 400
	r3 := httptest.NewRequest(http.MethodGet, "/api/pelicula/invites/badtoken/check", nil)
	w3 := httptest.NewRecorder()
	handleInviteOp(w3, r3)
	if w3.Code != http.StatusBadRequest {
		t.Errorf("bad token: expected 400, got %d", w3.Code)
	}
}

func TestHandleInviteRedeem(t *testing.T) {
	_ = newFakeJellyfin(t, func(mux *http.ServeMux) {
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
	resetServices(t)

	s := newTestInviteStore(t)
	setInviteStore(t, s)

	maxUses := 2
	inv, _ := s.CreateInvite("admin", "", nil, &maxUses)

	doRedeem := func(username, password string) *httptest.ResponseRecorder {
		body := map[string]string{"username": username, "password": password}
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/api/pelicula/invites/"+inv.Token+"/redeem", bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handleInviteOp(w, r)
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

func TestHandleInviteOffMode(t *testing.T) {
	s := newTestInviteStore(t)
	setInviteStore(t, s)
	orig := authMiddleware
	authMiddleware = newTestAuth("off")
	t.Cleanup(func() { authMiddleware = orig })

	r := httptest.NewRequest(http.MethodPost, "/api/pelicula/invites", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleInvites(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("off mode: expected 403, got %d", w.Code)
	}
}
