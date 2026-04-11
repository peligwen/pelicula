package peligrosa

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pelicula-api/httputil"
)

// newRequestStore returns a RequestStore backed by a test database.
func newRequestStore(t *testing.T) *RequestStore {
	t.Helper()
	db := testDB(t)
	return NewRequestStore(db, &fakeFulfiller{})
}

// insertRequest is a test helper that inserts a MediaRequest directly into the DB.
func insertRequest(t *testing.T, s *RequestStore, req *MediaRequest) {
	t.Helper()
	now := req.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	updatedAt := req.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = now
	}
	_, err := s.db.Exec(
		`INSERT INTO requests (id, type, tmdb_id, tvdb_id, title, year, poster,
		                       requested_by, state, reason, arr_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.Type, req.TmdbID, req.TvdbID, req.Title, req.Year, req.Poster,
		req.RequestedBy, string(req.State), req.Reason, req.ArrID,
		now.Format(time.RFC3339Nano), updatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatalf("insertRequest: %v", err)
	}
	for _, ev := range req.History {
		s.insertEvent(req.ID, ev)
	}
}

// ── Store unit tests ─────────────────────────────────────────────────────────

func TestNewRequestStore_Empty(t *testing.T) {
	s := newRequestStore(t)
	if got := s.All(); len(got) != 0 {
		t.Errorf("expected empty store, got %d requests", len(got))
	}
}

func TestNewRequestStore_LoadsExistingData(t *testing.T) {
	// Use a shared DB so data is visible across two store instances.
	db := testDB(t)
	s1 := NewRequestStore(db, &fakeFulfiller{})

	req := &MediaRequest{
		ID:        "req_123_abc",
		Type:      "movie",
		TmdbID:    42,
		Title:     "Test Movie",
		State:     RequestPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	insertRequest(t, s1, req)

	s2 := NewRequestStore(db, &fakeFulfiller{})
	all := s2.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 request after load, got %d", len(all))
	}
	if all[0].TmdbID != 42 {
		t.Errorf("TmdbID = %d, want 42", all[0].TmdbID)
	}
}

func TestRequestStore_CreateAssignsID(t *testing.T) {
	s := newRequestStore(t)

	req := &MediaRequest{
		ID:        generateRequestID(),
		Type:      "movie",
		TmdbID:    100,
		Title:     "A Film",
		State:     RequestPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		History:   []RequestEvent{{At: time.Now(), State: RequestPending}},
	}
	insertRequest(t, s, req)

	all := s.All()
	if len(all) != 1 {
		t.Fatalf("want 1 request, got %d", len(all))
	}
	if all[0].ID == "" {
		t.Error("ID must not be empty")
	}
}

func TestRequestStore_DeduplicatesActiveRequests(t *testing.T) {
	s := newRequestStore(t)

	existing := &MediaRequest{
		ID:     "req_1_aabbcc",
		Type:   "movie",
		TmdbID: 55,
		Title:  "Dup Film",
		State:  RequestPending,
	}
	insertRequest(t, s, existing)

	// findActive should find the existing request.
	found := s.findActive("movie", 55, 0)
	if found == nil {
		t.Fatal("findActive returned nil for existing active request")
	}
	if found.ID != "req_1_aabbcc" {
		t.Errorf("ID = %q, want req_1_aabbcc", found.ID)
	}
}

func TestRequestStore_DeduplicateSkipsTerminal(t *testing.T) {
	s := newRequestStore(t)

	insertRequest(t, s, &MediaRequest{
		ID:     "req_old",
		Type:   "movie",
		TmdbID: 77,
		Title:  "Old Film",
		State:  RequestDenied, // terminal
	})

	found := s.findActive("movie", 77, 0)
	if found != nil {
		t.Error("findActive should return nil for terminal requests")
	}
}

func TestRequestStore_DenyTransition(t *testing.T) {
	s := newRequestStore(t)

	req := &MediaRequest{
		ID:     "req_deny_test",
		Type:   "series",
		TvdbID: 200,
		Title:  "A Show",
		State:  RequestPending,
	}
	insertRequest(t, s, req)

	// Simulate deny logic.
	r := s.get("req_deny_test")
	if r == nil {
		t.Fatal("request not found")
	}
	r.State = RequestDenied
	r.Reason = "wrong quality"
	r.UpdatedAt = time.Now()
	if err := s.updateRequest(r); err != nil {
		t.Fatal(err)
	}
	ev := RequestEvent{
		At:    r.UpdatedAt,
		State: RequestDenied,
		Actor: "admin",
		Note:  "wrong quality",
	}
	if err := s.insertEvent(r.ID, ev); err != nil {
		t.Fatal(err)
	}

	all := s.All()
	if all[0].State != RequestDenied {
		t.Errorf("State = %q, want denied", all[0].State)
	}
	if all[0].Reason != "wrong quality" {
		t.Errorf("Reason = %q, want 'wrong quality'", all[0].Reason)
	}
	if len(all[0].History) == 0 {
		t.Error("expected at least one history event after deny")
	}
}

func TestMarkRequestAvailable_FlipsGrabbedByTmdb(t *testing.T) {
	s := newRequestStore(t)

	insertRequest(t, s, &MediaRequest{
		ID:     "req_avail_movie",
		Type:   "movie",
		TmdbID: 999,
		Title:  "Ready Film",
		State:  RequestGrabbed,
	})

	s.MarkAvailable("movie", 999, 0, "Ready Film", nil)

	all := s.All()
	if all[0].State != RequestAvailable {
		t.Errorf("State = %q after MarkAvailable, want available", all[0].State)
	}
}

func TestMarkRequestAvailable_NoOpOnMiss(t *testing.T) {
	s := newRequestStore(t)

	// No requests — should not panic or error.
	s.MarkAvailable("movie", 12345, 0, "Not In Queue", nil)

	if got := s.All(); len(got) != 0 {
		t.Errorf("expected 0 requests, got %d", len(got))
	}
}

func TestMarkRequestAvailable_FlipsGrabbedByTvdb(t *testing.T) {
	s := newRequestStore(t)

	insertRequest(t, s, &MediaRequest{
		ID:     "req_avail_series",
		Type:   "series",
		TvdbID: 888,
		Title:  "Ready Show",
		State:  RequestGrabbed,
	})

	s.MarkAvailable("series", 0, 888, "Ready Show", nil)

	all := s.All()
	if all[0].State != RequestAvailable {
		t.Errorf("State = %q after MarkAvailable, want available", all[0].State)
	}
}

// ── HTTP handler tests ───────────────────────────────────────────────────────

// newTestRequestDeps builds a Deps for request handler tests.
func newTestRequestDeps(auth *Auth, rs *RequestStore) *Deps {
	return &Deps{Auth: auth, Requests: rs}
}

func TestHandleRequestCreate_RequiresAuth(t *testing.T) {
	// Set up a store and jellyfin-mode auth (no active sessions → 401).
	s := newRequestStore(t)
	db := testDB(t)
	auth := NewAuth(AuthConfig{DB: db})
	deps := newTestRequestDeps(auth, s)

	body, _ := json.Marshal(map[string]any{
		"type":    "movie",
		"tmdb_id": 1,
		"title":   "Test",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader(body))
	w := httptest.NewRecorder()
	deps.HandleRequestCreate(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when not authenticated in users mode", w.Code)
	}
}

// TestHandleRequestCreate_LoopbackRequester verifies that a loopback auto-session
// caller (no cookie, but trusted CIDR + loopback X-Real-IP + localhost Host) can
// create a request and that requested_by == "(loopback)".
func TestHandleRequestCreate_LoopbackRequester(t *testing.T) {
	old := httputil.TrustedUpstreamCIDR
	httputil.TrustedUpstreamCIDR = "172.17.0.0/16"
	t.Cleanup(func() { httputil.TrustedUpstreamCIDR = old })

	s := newRequestStore(t)
	auth := newTestAuth() // no sessions — loopback path must supply identity
	deps := newTestRequestDeps(auth, s)

	body, _ := json.Marshal(map[string]any{
		"type":    "movie",
		"tmdb_id": 42,
		"title":   "A Loopback Film",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "172.17.0.1:55123"
	req.Header.Set("X-Real-IP", "127.0.0.1")
	req.Host = "localhost"

	w := httptest.NewRecorder()
	deps.HandleRequestCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}

	all := s.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 request in store, got %d", len(all))
	}
	if all[0].RequestedBy != "(loopback)" {
		t.Errorf("requested_by = %q, want (loopback)", all[0].RequestedBy)
	}
}

// TestHandleRequestCreate_DedupeReturnsExisting verifies that a duplicate request
// for an active tmdb_id returns the existing record rather than creating a new one.
func TestHandleRequestCreate_DedupeReturnsExisting(t *testing.T) {
	s := newRequestStore(t)
	auth := newTestAuth()
	token := insertSession(auth, "alice", RoleViewer, time.Now().Add(time.Hour))
	deps := newTestRequestDeps(auth, s)

	// Pre-insert an existing pending request for tmdb_id=42.
	existing := &MediaRequest{
		ID:     "req_existing_aabbcc",
		Type:   "movie",
		TmdbID: 42,
		Title:  "Duplicate Film",
		State:  RequestPending,
	}
	insertRequest(t, s, existing)

	// POST a create for the same tmdb_id.
	body, _ := json.Marshal(map[string]any{
		"type":    "movie",
		"tmdb_id": 42,
		"title":   "Duplicate Film",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	addSessionCookie(req, token)

	w := httptest.NewRecorder()
	deps.HandleRequestCreate(w, req)

	// Should return the existing request (200, not 201).
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (existing returned)", w.Code)
	}

	var resp MediaRequest
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp.ID != "req_existing_aabbcc" {
		t.Errorf("id = %q, want req_existing_aabbcc (existing record)", resp.ID)
	}

	// Store must still have exactly one request.
	if got := len(s.All()); got != 1 {
		t.Errorf("store has %d requests, want 1", got)
	}
}

// TestHandleRequestCreate_RejectsBadType verifies that an invalid type returns 400.
func TestHandleRequestCreate_RejectsBadType(t *testing.T) {
	s := newRequestStore(t)
	auth := newTestAuth()
	token := insertSession(auth, "alice", RoleViewer, time.Now().Add(time.Hour))
	deps := newTestRequestDeps(auth, s)

	body, _ := json.Marshal(map[string]any{
		"type":    "invalid",
		"tmdb_id": 1,
		"title":   "Bad Type Film",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	addSessionCookie(req, token)

	w := httptest.NewRecorder()
	deps.HandleRequestCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid type", w.Code)
	}
}

// TestHandleRequestList_ViewerSeesOnlyOwn verifies that viewers see only their own
// requests while managers/admins see all requests.
func TestHandleRequestList_ViewerSeesOnlyOwn(t *testing.T) {
	s := newRequestStore(t)
	auth := newTestAuth()
	deps := newTestRequestDeps(auth, s)

	// Insert two requests with different owners.
	insertRequest(t, s, &MediaRequest{
		ID:          "req_alice_001",
		Type:        "movie",
		TmdbID:      10,
		Title:       "Alice's Film",
		State:       RequestPending,
		RequestedBy: "alice",
	})
	insertRequest(t, s, &MediaRequest{
		ID:          "req_bob_001",
		Type:        "movie",
		TmdbID:      20,
		Title:       "Bob's Film",
		State:       RequestPending,
		RequestedBy: "bob",
	})

	// Viewer alice should see only her own request.
	aliceTok := insertSession(auth, "alice", RoleViewer, time.Now().Add(time.Hour))
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/requests", nil)
	addSessionCookie(req, aliceTok)
	w := httptest.NewRecorder()
	deps.HandleRequestList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("alice list: status = %d, want 200", w.Code)
	}
	var aliceList []*MediaRequest
	if err := json.Unmarshal(w.Body.Bytes(), &aliceList); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(aliceList) != 1 {
		t.Errorf("alice sees %d requests, want 1", len(aliceList))
	} else if aliceList[0].ID != "req_alice_001" {
		t.Errorf("alice sees request %q, want req_alice_001", aliceList[0].ID)
	}

	// Admin sees both (admins see all requests regardless of owner).
	adminTok := insertSession(auth, "admin", RoleAdmin, time.Now().Add(time.Hour))
	req2 := httptest.NewRequest(http.MethodGet, "/api/pelicula/requests", nil)
	addSessionCookie(req2, adminTok)
	w2 := httptest.NewRecorder()
	deps.HandleRequestList(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("admin list: status = %d, want 200", w2.Code)
	}
	var adminList []*MediaRequest
	if err := json.Unmarshal(w2.Body.Bytes(), &adminList); err != nil {
		t.Fatalf("parse admin response: %v", err)
	}
	if len(adminList) != 2 {
		t.Errorf("admin sees %d requests, want 2", len(adminList))
	}
}
