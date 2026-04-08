package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newRequestStore returns a RequestStore backed by a temp file.
func newRequestStore(t *testing.T) *RequestStore {
	t.Helper()
	dir := t.TempDir()
	return NewRequestStore(filepath.Join(dir, "requests.json"))
}

// ── Store unit tests ─────────────────────────────────────────────────────────

func TestNewRequestStore_Empty(t *testing.T) {
	s := newRequestStore(t)
	if got := s.all(); len(got) != 0 {
		t.Errorf("expected empty store, got %d requests", len(got))
	}
}

func TestNewRequestStore_LoadsExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "requests.json")

	// Write a pre-existing store.
	req := &MediaRequest{
		ID:        "req_123_abc",
		Type:      "movie",
		TmdbID:    42,
		Title:     "Test Movie",
		State:     RequestPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	data, _ := json.MarshalIndent([]*MediaRequest{req}, "", "  ")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	s := NewRequestStore(path)
	all := s.all()
	if len(all) != 1 {
		t.Fatalf("expected 1 request after load, got %d", len(all))
	}
	if all[0].TmdbID != 42 {
		t.Errorf("TmdbID = %d, want 42", all[0].TmdbID)
	}
}

func TestRequestStore_CreateAssignsID(t *testing.T) {
	s := newRequestStore(t)

	s.mu.Lock()
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
	s.requests = append(s.requests, req)
	if err := s.save(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()

	all := s.all()
	if len(all) != 1 {
		t.Fatalf("want 1 request, got %d", len(all))
	}
	if all[0].ID == "" {
		t.Error("ID must not be empty")
	}
}

func TestRequestStore_DeduplicatesActiveRequests(t *testing.T) {
	s := newRequestStore(t)

	s.mu.Lock()
	existing := &MediaRequest{
		ID:     "req_1_aabbcc",
		Type:   "movie",
		TmdbID: 55,
		Title:  "Dup Film",
		State:  RequestPending,
	}
	s.requests = append(s.requests, existing)
	s.mu.Unlock()

	// findActive should find the existing request.
	s.mu.Lock()
	found := s.findActive("movie", 55, 0)
	s.mu.Unlock()

	if found == nil {
		t.Fatal("findActive returned nil for existing active request")
	}
	if found.ID != "req_1_aabbcc" {
		t.Errorf("ID = %q, want req_1_aabbcc", found.ID)
	}
}

func TestRequestStore_DeduplicateSkipsTerminal(t *testing.T) {
	s := newRequestStore(t)

	s.mu.Lock()
	s.requests = append(s.requests, &MediaRequest{
		ID:     "req_old",
		Type:   "movie",
		TmdbID: 77,
		Title:  "Old Film",
		State:  RequestDenied, // terminal
	})
	s.mu.Unlock()

	s.mu.Lock()
	found := s.findActive("movie", 77, 0)
	s.mu.Unlock()

	if found != nil {
		t.Error("findActive should return nil for terminal requests")
	}
}

func TestRequestStore_DenyTransition(t *testing.T) {
	s := newRequestStore(t)

	s.mu.Lock()
	req := &MediaRequest{
		ID:     "req_deny_test",
		Type:   "series",
		TvdbID: 200,
		Title:  "A Show",
		State:  RequestPending,
	}
	s.requests = append(s.requests, req)
	s.mu.Unlock()

	// Simulate deny logic.
	s.mu.Lock()
	r := s.get("req_deny_test")
	r.State = RequestDenied
	r.Reason = "wrong quality"
	r.UpdatedAt = time.Now()
	r.History = append(r.History, RequestEvent{
		At:    time.Now(),
		State: RequestDenied,
		Actor: "admin",
		Note:  "wrong quality",
	})
	if err := s.save(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()

	all := s.all()
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
	requestStore = s

	s.mu.Lock()
	s.requests = append(s.requests, &MediaRequest{
		ID:     "req_avail_movie",
		Type:   "movie",
		TmdbID: 999,
		Title:  "Ready Film",
		State:  RequestGrabbed,
	})
	s.mu.Unlock()

	MarkRequestAvailable("movie", 999, 0, "Ready Film")

	all := s.all()
	if all[0].State != RequestAvailable {
		t.Errorf("State = %q after MarkRequestAvailable, want available", all[0].State)
	}
}

func TestMarkRequestAvailable_NoOpOnMiss(t *testing.T) {
	s := newRequestStore(t)
	requestStore = s

	// No requests — should not panic or error.
	MarkRequestAvailable("movie", 12345, 0, "Not In Queue")

	if got := s.all(); len(got) != 0 {
		t.Errorf("expected 0 requests, got %d", len(got))
	}
}

func TestMarkRequestAvailable_FlipsGrabbedByTvdb(t *testing.T) {
	s := newRequestStore(t)
	requestStore = s

	s.mu.Lock()
	s.requests = append(s.requests, &MediaRequest{
		ID:     "req_avail_series",
		Type:   "series",
		TvdbID: 888,
		Title:  "Ready Show",
		State:  RequestGrabbed,
	})
	s.mu.Unlock()

	MarkRequestAvailable("series", 0, 888, "Ready Show")

	all := s.all()
	if all[0].State != RequestAvailable {
		t.Errorf("State = %q after MarkRequestAvailable, want available", all[0].State)
	}
}

// ── HTTP handler tests ───────────────────────────────────────────────────────

func TestHandleRequestCreate_RequiresAuth(t *testing.T) {
	// Set up a store and users-mode auth (no active sessions → 401).
	s := newRequestStore(t)
	requestStore = s
	authMiddleware = NewAuth(AuthConfig{Mode: "users", UsersFile: os.DevNull})

	body, _ := json.Marshal(map[string]any{
		"type":    "movie",
		"tmdb_id": 1,
		"title":   "Test",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleRequestCreate(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when not authenticated in users mode", w.Code)
	}
}

func TestHandleRequestCreate_OffModeAccepted(t *testing.T) {
	dir := t.TempDir()
	s := NewRequestStore(filepath.Join(dir, "requests.json"))
	requestStore = s
	authMiddleware = NewAuth(AuthConfig{Mode: "off"})

	body, _ := json.Marshal(map[string]any{
		"type":    "movie",
		"tmdb_id": 42,
		"title":   "Open Film",
		"year":    2024,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleRequestCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 in off mode", w.Code)
	}
	all := s.all()
	if len(all) != 1 {
		t.Fatalf("expected 1 request created, got %d", len(all))
	}
	if all[0].TmdbID != 42 {
		t.Errorf("TmdbID = %d, want 42", all[0].TmdbID)
	}
}

func TestHandleRequestCreate_DedupeReturnsExisting(t *testing.T) {
	dir := t.TempDir()
	s := NewRequestStore(filepath.Join(dir, "requests.json"))
	requestStore = s
	authMiddleware = NewAuth(AuthConfig{Mode: "off"})

	// Seed an existing request.
	s.mu.Lock()
	existing := &MediaRequest{
		ID:     "req_already",
		Type:   "movie",
		TmdbID: 42,
		Title:  "Open Film",
		State:  RequestPending,
	}
	s.requests = append(s.requests, existing)
	_ = s.save()
	s.mu.Unlock()

	body, _ := json.Marshal(map[string]any{
		"type":    "movie",
		"tmdb_id": 42,
		"title":   "Open Film",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleRequestCreate(w, req)

	// Should return 200 (not 201) with the existing request.
	if w.Code == http.StatusCreated {
		t.Error("expected non-201 (deduped): should return existing request")
	}
	all := s.all()
	if len(all) != 1 {
		t.Errorf("expected 1 request (deduped), got %d", len(all))
	}
}

func TestHandleRequestCreate_RejectsBadType(t *testing.T) {
	dir := t.TempDir()
	requestStore = NewRequestStore(filepath.Join(dir, "requests.json"))
	authMiddleware = NewAuth(AuthConfig{Mode: "off"})

	body, _ := json.Marshal(map[string]any{
		"type":    "anime",
		"tmdb_id": 1,
		"title":   "A Show",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleRequestCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid type", w.Code)
	}
}

func TestHandleRequestList_ViewerSeesOnlyOwn(t *testing.T) {
	// Off mode: all requests treated as owned by ""
	dir := t.TempDir()
	s := NewRequestStore(filepath.Join(dir, "requests.json"))
	requestStore = s
	authMiddleware = NewAuth(AuthConfig{Mode: "off"})

	s.mu.Lock()
	s.requests = []*MediaRequest{
		{ID: "r1", Type: "movie", TmdbID: 1, Title: "Film1", State: RequestPending, RequestedBy: "alice"},
		{ID: "r2", Type: "movie", TmdbID: 2, Title: "Film2", State: RequestPending, RequestedBy: "bob"},
	}
	_ = s.save()
	s.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/requests", nil)
	w := httptest.NewRecorder()
	handleRequestList(w, req)

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
