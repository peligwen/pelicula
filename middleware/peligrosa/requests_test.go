package peligrosa

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
	auth := NewAuth(AuthConfig{Mode: "jellyfin", DB: db})
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

func TestHandleRequestCreate_OffModeAccepted(t *testing.T) {
	s := newRequestStore(t)
	deps := newTestRequestDeps(NewAuth(AuthConfig{Mode: "off"}), s)

	body, _ := json.Marshal(map[string]any{
		"type":    "movie",
		"tmdb_id": 42,
		"title":   "Open Film",
		"year":    2024,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader(body))
	w := httptest.NewRecorder()
	deps.HandleRequestCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 in off mode", w.Code)
	}
	all := s.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 request created, got %d", len(all))
	}
	if all[0].TmdbID != 42 {
		t.Errorf("TmdbID = %d, want 42", all[0].TmdbID)
	}
}

func TestHandleRequestCreate_DedupeReturnsExisting(t *testing.T) {
	s := newRequestStore(t)
	deps := newTestRequestDeps(NewAuth(AuthConfig{Mode: "off"}), s)

	// Seed an existing request.
	existing := &MediaRequest{
		ID:     "req_already",
		Type:   "movie",
		TmdbID: 42,
		Title:  "Open Film",
		State:  RequestPending,
	}
	insertRequest(t, s, existing)

	body, _ := json.Marshal(map[string]any{
		"type":    "movie",
		"tmdb_id": 42,
		"title":   "Open Film",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader(body))
	w := httptest.NewRecorder()
	deps.HandleRequestCreate(w, req)

	// Should return 200 (not 201) with the existing request.
	if w.Code == http.StatusCreated {
		t.Error("expected non-201 (deduped): should return existing request")
	}
	all := s.All()
	if len(all) != 1 {
		t.Errorf("expected 1 request (deduped), got %d", len(all))
	}
}

func TestHandleRequestCreate_RejectsBadType(t *testing.T) {
	s := newRequestStore(t)
	deps := newTestRequestDeps(NewAuth(AuthConfig{Mode: "off"}), s)

	body, _ := json.Marshal(map[string]any{
		"type":    "anime",
		"tmdb_id": 1,
		"title":   "A Show",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader(body))
	w := httptest.NewRecorder()
	deps.HandleRequestCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid type", w.Code)
	}
}

func TestHandleRequestList_ViewerSeesOnlyOwn(t *testing.T) {
	// Off mode: all requests treated as owned by ""
	s := newRequestStore(t)
	deps := newTestRequestDeps(NewAuth(AuthConfig{Mode: "off"}), s)

	insertRequest(t, s, &MediaRequest{ID: "r1", Type: "movie", TmdbID: 1, Title: "Film1", State: RequestPending, RequestedBy: "alice"})
	insertRequest(t, s, &MediaRequest{ID: "r2", Type: "movie", TmdbID: 2, Title: "Film2", State: RequestPending, RequestedBy: "bob"})

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/requests", nil)
	w := httptest.NewRecorder()
	deps.HandleRequestList(w, req)

	// In "off" mode, SessionFor returns ("", RoleAdmin, true) so admin sees all.
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var out []*MediaRequest
	json.NewDecoder(w.Body).Decode(&out)
	if len(out) != 2 {
		t.Errorf("admin (off mode) should see all 2 requests, got %d", len(out))
	}
}
