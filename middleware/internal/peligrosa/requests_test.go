package peligrosa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pelicula-api/clients"
	reporeqs "pelicula-api/internal/repo/requests"
)

// newRequestStore returns a RequestStore backed by a test database.
func newRequestStore(t *testing.T) *RequestStore {
	t.Helper()
	db := testDB(t)
	return NewRequestStore(reporeqs.New(db), &fakeFulfiller{})
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
	seasonsText := ""
	if req.Seasons != nil {
		b, err := json.Marshal(req.Seasons)
		if err != nil {
			t.Fatalf("insertRequest: marshal seasons: %v", err)
		}
		seasonsText = string(b)
	}
	_, err := s.repo.DB().Exec(
		`INSERT INTO requests (id, type, tmdb_id, tvdb_id, title, year, poster,
		                       requested_by, state, reason, arr_id, created_at, updated_at, seasons)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.Type, req.TmdbID, req.TvdbID, req.Title, req.Year, req.Poster,
		req.RequestedBy, string(req.State), req.Reason, req.ArrID,
		now.Format(time.RFC3339Nano), updatedAt.Format(time.RFC3339Nano), seasonsText,
	)
	if err != nil {
		t.Fatalf("insertRequest: %v", err)
	}
	for _, ev := range req.History {
		s.insertEvent(context.Background(), req.ID, ev)
	}
}

// ── Store unit tests ─────────────────────────────────────────────────────────

func TestNewRequestStore_Empty(t *testing.T) {
	s := newRequestStore(t)
	if got := s.All(context.Background()); len(got) != 0 {
		t.Errorf("expected empty store, got %d requests", len(got))
	}
}

func TestNewRequestStore_LoadsExistingData(t *testing.T) {
	// Use a shared DB so data is visible across two store instances.
	db := testDB(t)
	s1 := NewRequestStore(reporeqs.New(db), &fakeFulfiller{})

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

	s2 := NewRequestStore(reporeqs.New(db), &fakeFulfiller{})
	all := s2.All(context.Background())
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

	all := s.All(context.Background())
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
	found := s.findActive(context.Background(), "movie", 55, 0)
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

	found := s.findActive(context.Background(), "movie", 77, 0)
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
	r := s.get(context.Background(), "req_deny_test")
	if r == nil {
		t.Fatal("request not found")
	}
	r.State = RequestDenied
	r.Reason = "wrong quality"
	r.UpdatedAt = time.Now()
	if err := s.updateRequest(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	ev := RequestEvent{
		At:    r.UpdatedAt,
		State: RequestDenied,
		Actor: "admin",
		Note:  "wrong quality",
	}
	if err := s.insertEvent(context.Background(), r.ID, ev); err != nil {
		t.Fatal(err)
	}

	all := s.All(context.Background())
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

	s.MarkAvailable(context.Background(), "movie", 999, 0, "Ready Film", nil)

	all := s.All(context.Background())
	if all[0].State != RequestAvailable {
		t.Errorf("State = %q after MarkAvailable, want available", all[0].State)
	}
}

func TestMarkRequestAvailable_NoOpOnMiss(t *testing.T) {
	s := newRequestStore(t)

	// No requests — should not panic or error.
	s.MarkAvailable(context.Background(), "movie", 12345, 0, "Not In Queue", nil)

	if got := s.All(context.Background()); len(got) != 0 {
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

	s.MarkAvailable(context.Background(), "series", 0, 888, "Ready Show", nil)

	all := s.All(context.Background())
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
	withTrustedCIDR(t, "172.17.0.0/16")

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
	loopbackTriple(req)

	w := httptest.NewRecorder()
	deps.HandleRequestCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}

	all := s.All(context.Background())
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
	if got := len(s.All(context.Background())); got != 1 {
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

// TestApprove_ConcurrentRace verifies that two concurrent approve requests for
// the same pending request result in exactly one successful 200 response and
// one 409 Conflict, and that the *arr fulfiller is called exactly once.
func TestApprove_ConcurrentRace(t *testing.T) {
	// Not parallel — this test exercises a global mutex; parallelism at the
	// test level would interfere with other tests using the same fulfiller type.

	var addMovieCalls atomic.Int32
	// fakeFulfiller with an atomic counter and a brief sleep to widen the race window.
	ff := &fakeFulfiller{
		addMovieFn: func(ctx context.Context, tmdbID, profileID int, rootPath string) (int, error) {
			addMovieCalls.Add(1)
			time.Sleep(5 * time.Millisecond) // widen race window
			return 7, nil                    // fixed arr_id
		},
	}

	db := testDB(t)
	rs := NewRequestStore(reporeqs.New(db), ff)

	// Insert one pending movie request.
	pending := &MediaRequest{
		ID:          "req_race_001",
		Type:        "movie",
		TmdbID:      100,
		Title:       "Race Film",
		State:       RequestPending,
		RequestedBy: "viewer",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	insertRequest(t, rs, pending)

	type result struct {
		status int
		body   string
	}

	results := make([]result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests/req_race_001/approve", nil)
			w := httptest.NewRecorder()
			rs.handleRequestApprove(w, req, "req_race_001", "admin", nil)
			results[idx] = result{status: w.Code, body: w.Body.String()}
		}(i)
	}
	wg.Wait()

	// Exactly one 200 and one 409.
	ok200, ok409 := 0, 0
	for _, r := range results {
		switch r.status {
		case http.StatusOK:
			ok200++
		case http.StatusConflict:
			ok409++
		default:
			t.Errorf("unexpected status %d: %s", r.status, r.body)
		}
	}
	if ok200 != 1 {
		t.Errorf("expected exactly 1 success (200), got %d", ok200)
	}
	if ok409 != 1 {
		t.Errorf("expected exactly 1 conflict (409), got %d", ok409)
	}

	// The fulfiller must have been called exactly once — no duplicate *arr entries.
	if n := addMovieCalls.Load(); n != 1 {
		t.Errorf("AddMovie called %d times, want exactly 1", n)
	}

	// The audit history must contain exactly one grabbed event.
	got, err := rs.repo.Get(t.Context(), "req_race_001")
	if err != nil {
		t.Fatalf("Get after race: %v", err)
	}
	grabbedCount := 0
	for _, ev := range got.History {
		if ev.State == reporeqs.StateGrabbed {
			grabbedCount++
		}
	}
	if grabbedCount != 1 {
		t.Errorf("grabbed events in history: got %d, want 1", grabbedCount)
	}
}

// ── Phase 2.1: season-level request create/approve ──────────────────────────

// TestHandleRequestCreate_SeriesWithSeasonsPersists verifies that a viewer's
// seasons selection is shape-normalized (deduped, sorted) and persists
// through to the stored request and the create response.
func TestHandleRequestCreate_SeriesWithSeasonsPersists(t *testing.T) {
	s := newRequestStore(t)
	auth := newTestAuth()
	token := insertSession(auth, "alice", RoleViewer, time.Now().Add(time.Hour))
	deps := newTestRequestDeps(auth, s)

	body, _ := json.Marshal(map[string]any{
		"type":    "series",
		"tvdb_id": 600,
		"title":   "Seasons Show",
		"seasons": []int{2, 1, 2}, // duplicate + unsorted, to prove normalize runs
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	addSessionCookie(req, token)

	w := httptest.NewRecorder()
	deps.HandleRequestCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	var resp MediaRequest
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(resp.Seasons) != 2 || resp.Seasons[0] != 1 || resp.Seasons[1] != 2 {
		t.Errorf("response Seasons = %v, want [1 2] (deduped, sorted)", resp.Seasons)
	}

	got, err := s.repo.Get(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Seasons) != 2 || got.Seasons[0] != 1 || got.Seasons[1] != 2 {
		t.Errorf("persisted Seasons = %v, want [1 2]", got.Seasons)
	}
}

// TestHandleRequestCreate_MovieWithSeasonsRejected verifies that seasons on a
// movie request is rejected with 400 — mirrors HandleSearchAdd's rule.
func TestHandleRequestCreate_MovieWithSeasonsRejected(t *testing.T) {
	s := newRequestStore(t)
	auth := newTestAuth()
	token := insertSession(auth, "alice", RoleViewer, time.Now().Add(time.Hour))
	deps := newTestRequestDeps(auth, s)

	body, _ := json.Marshal(map[string]any{
		"type":    "movie",
		"tmdb_id": 601,
		"title":   "Movie With Seasons",
		"seasons": []int{1},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	addSessionCookie(req, token)

	w := httptest.NewRecorder()
	deps.HandleRequestCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for seasons on a movie", w.Code)
	}
}

// TestHandleRequestCreate_EmptySeasonsArrayRejected verifies that a non-nil
// empty seasons array ("monitor nothing" has no meaning) is rejected with 400.
func TestHandleRequestCreate_EmptySeasonsArrayRejected(t *testing.T) {
	s := newRequestStore(t)
	auth := newTestAuth()
	token := insertSession(auth, "alice", RoleViewer, time.Now().Add(time.Hour))
	deps := newTestRequestDeps(auth, s)

	body, _ := json.Marshal(map[string]any{
		"type":    "series",
		"tvdb_id": 602,
		"title":   "Empty Seasons Show",
		"seasons": []int{},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	addSessionCookie(req, token)

	w := httptest.NewRecorder()
	deps.HandleRequestCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a non-nil empty seasons array", w.Code)
	}
}

// TestHandleRequestApprove_ThreadsStoredSeasonsToFulfiller verifies that when
// the approve body is empty (the common case — admin just clicks approve),
// the request's stored season scope is threaded to AddSeries and persisted.
func TestHandleRequestApprove_ThreadsStoredSeasonsToFulfiller(t *testing.T) {
	var gotSeasons []int
	ff := &fakeFulfiller{
		addSeriesFn: func(ctx context.Context, tvdbID, profileID int, rootPath string, seasons []int) (int, error) {
			gotSeasons = seasons
			return 42, nil
		},
	}
	db := testDB(t)
	rs := NewRequestStore(reporeqs.New(db), ff)

	insertRequest(t, rs, &MediaRequest{
		ID:      "req_approve_stored_seasons",
		Type:    "series",
		TvdbID:  500,
		Title:   "Stored Seasons Show",
		State:   RequestPending,
		Seasons: []int{1, 3},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests/req_approve_stored_seasons/approve", nil)
	w := httptest.NewRecorder()
	rs.handleRequestApprove(w, req, "req_approve_stored_seasons", "admin", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if len(gotSeasons) != 2 || gotSeasons[0] != 1 || gotSeasons[1] != 3 {
		t.Errorf("AddSeries seasons = %v, want [1 3] (the stored scope)", gotSeasons)
	}

	got, err := rs.repo.Get(context.Background(), "req_approve_stored_seasons")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Seasons) != 2 || got.Seasons[0] != 1 || got.Seasons[1] != 3 {
		t.Errorf("persisted Seasons = %v, want [1 3]", got.Seasons)
	}
}

// TestHandleRequestApprove_BodyOverrideWinsOverStored verifies that a
// non-empty approve-body seasons array overrides the request's stored scope.
func TestHandleRequestApprove_BodyOverrideWinsOverStored(t *testing.T) {
	var gotSeasons []int
	ff := &fakeFulfiller{
		addSeriesFn: func(ctx context.Context, tvdbID, profileID int, rootPath string, seasons []int) (int, error) {
			gotSeasons = seasons
			return 43, nil
		},
	}
	db := testDB(t)
	rs := NewRequestStore(reporeqs.New(db), ff)

	insertRequest(t, rs, &MediaRequest{
		ID:      "req_approve_override",
		Type:    "series",
		TvdbID:  501,
		Title:   "Override Show",
		State:   RequestPending,
		Seasons: []int{1},
	})

	body := bytes.NewReader([]byte(`{"seasons":[2,3]}`))
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests/req_approve_override/approve", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	rs.handleRequestApprove(w, req, "req_approve_override", "admin", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if len(gotSeasons) != 2 || gotSeasons[0] != 2 || gotSeasons[1] != 3 {
		t.Errorf("AddSeries seasons = %v, want [2 3] (the body override, not the stored [1])", gotSeasons)
	}

	got, err := rs.repo.Get(context.Background(), "req_approve_override")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Seasons) != 2 || got.Seasons[0] != 2 || got.Seasons[1] != 3 {
		t.Errorf("persisted Seasons = %v, want [2 3] (override wins)", got.Seasons)
	}
}

// TestHandleRequestApprove_EmptyArrayClearsToAll verifies that an explicit
// empty seasons array in the approve body clears any stored scope to "all
// seasons" (nil), both for the AddSeries call and the persisted row.
func TestHandleRequestApprove_EmptyArrayClearsToAll(t *testing.T) {
	var addSeriesCalled bool
	var gotSeasons []int
	ff := &fakeFulfiller{
		addSeriesFn: func(ctx context.Context, tvdbID, profileID int, rootPath string, seasons []int) (int, error) {
			addSeriesCalled = true
			gotSeasons = seasons
			return 44, nil
		},
	}
	db := testDB(t)
	rs := NewRequestStore(reporeqs.New(db), ff)

	insertRequest(t, rs, &MediaRequest{
		ID:      "req_approve_clear",
		Type:    "series",
		TvdbID:  502,
		Title:   "Clear Scope Show",
		State:   RequestPending,
		Seasons: []int{1},
	})

	body := bytes.NewReader([]byte(`{"seasons":[]}`))
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests/req_approve_clear/approve", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	rs.handleRequestApprove(w, req, "req_approve_clear", "admin", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !addSeriesCalled {
		t.Fatal("AddSeries was never called")
	}
	if gotSeasons != nil {
		t.Errorf("AddSeries seasons = %v, want nil (explicit [] clears to all seasons)", gotSeasons)
	}

	got, err := rs.repo.Get(context.Background(), "req_approve_clear")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Seasons != nil {
		t.Errorf("persisted Seasons = %v, want nil (cleared)", got.Seasons)
	}
}

// TestHandleRequestApprove_InvalidSeasonsSentinelMapsTo400AndStaysPending
// verifies that clients.ErrInvalidSeasons from the fulfiller maps to 400
// (distinct from the 502 used for upstream-unreachable errors) and leaves
// the request pending so the admin can fix the override and retry.
func TestHandleRequestApprove_InvalidSeasonsSentinelMapsTo400AndStaysPending(t *testing.T) {
	ff := &fakeFulfiller{
		addSeriesFn: func(ctx context.Context, tvdbID, profileID int, rootPath string, seasons []int) (int, error) {
			return 0, fmt.Errorf("%w: season(s) [9] not found for this series", clients.ErrInvalidSeasons)
		},
	}
	db := testDB(t)
	rs := NewRequestStore(reporeqs.New(db), ff)

	insertRequest(t, rs, &MediaRequest{
		ID:     "req_approve_invalid_seasons",
		Type:   "series",
		TvdbID: 503,
		Title:  "Invalid Seasons Show",
		State:  RequestPending,
	})

	body := bytes.NewReader([]byte(`{"seasons":[9]}`))
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests/req_approve_invalid_seasons/approve", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	rs.handleRequestApprove(w, req, "req_approve_invalid_seasons", "admin", nil)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not found") {
		t.Errorf("body = %q, want it to include the sentinel error text", w.Body.String())
	}

	got, err := rs.repo.Get(context.Background(), "req_approve_invalid_seasons")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != reporeqs.StatePending {
		t.Errorf("State = %q, want still pending after ErrInvalidSeasons", got.State)
	}
}
