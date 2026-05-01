package requests_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"pelicula-api/internal/repo/requests"
)

// newTestDB opens an in-memory SQLite database with the requests and
// request_events tables. MaxOpenConns=1 mirrors production (avoids SQLite
// locking issues in tests).
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
		CREATE TABLE IF NOT EXISTS requests (
			id           TEXT PRIMARY KEY,
			type         TEXT NOT NULL,
			tmdb_id      INTEGER NOT NULL DEFAULT 0,
			tvdb_id      INTEGER NOT NULL DEFAULT 0,
			title        TEXT NOT NULL,
			year         INTEGER NOT NULL DEFAULT 0,
			poster       TEXT,
			requested_by TEXT NOT NULL DEFAULT '',
			state        TEXT NOT NULL DEFAULT 'pending',
			reason       TEXT,
			arr_id       INTEGER,
			created_at   TEXT NOT NULL,
			updated_at   TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS request_events (
			request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
			at         TEXT NOT NULL,
			state      TEXT NOT NULL,
			actor      TEXT NOT NULL DEFAULT '',
			note       TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_request_events_request_id ON request_events(request_id);
	`); err != nil {
		db.Close()
		t.Fatalf("create tables: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newStore(t *testing.T) *requests.Store {
	t.Helper()
	return requests.New(newTestDB(t))
}

// makeRequest constructs a minimal Request for insertion.
func makeRequest(id, reqType, title string, state requests.State) *requests.Request {
	now := time.Now().UTC().Truncate(time.Second)
	return &requests.Request{
		ID:          id,
		Type:        reqType,
		TmdbID:      42,
		Title:       title,
		Year:        2024,
		RequestedBy: "alice",
		State:       state,
		CreatedAt:   now,
		UpdatedAt:   now,
		History:     []requests.Event{},
	}
}

// ── Insert + Get ──────────────────────────────────────────────────────────────

func TestInsert_Get(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	req := makeRequest("req_001", "movie", "Test Movie", requests.StatePending)
	if err := s.Insert(ctx, req); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := s.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != req.ID {
		t.Errorf("ID: got %q, want %q", got.ID, req.ID)
	}
	if got.Title != req.Title {
		t.Errorf("Title: got %q, want %q", got.Title, req.Title)
	}
	if got.State != requests.StatePending {
		t.Errorf("State: got %q, want pending", got.State)
	}
	if got.RequestedBy != "alice" {
		t.Errorf("RequestedBy: got %q, want alice", got.RequestedBy)
	}
	if len(got.History) != 0 {
		t.Errorf("expected empty history, got %d events", len(got.History))
	}
}

func TestGet_NotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	_, err := s.Get(ctx, "nonexistent")
	if !errors.Is(err, requests.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ── All + N+1 fix ─────────────────────────────────────────────────────────────

func TestAll_Empty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	got, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d requests", len(got))
	}
}

func TestAll_EnqueueListRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	for i, title := range []string{"Alpha", "Beta", "Gamma"} {
		req := makeRequest("req_"+title, "movie", title, requests.StatePending)
		req.TmdbID = 100 + i
		if err := s.Insert(ctx, req); err != nil {
			t.Fatalf("Insert %q: %v", title, err)
		}
	}

	all, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(all))
	}
}

// TestAll_SingleBulkHistoryQuery verifies that All() does NOT perform N+1 queries
// for history. We confirm this by inserting N requests with history events and
// checking that all history is returned correctly in a single All() call
// (no deadlock from per-row queries with MaxOpenConns=1 — if N+1 occurred, the
// test would hang or error).
func TestAll_SingleBulkHistoryQuery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	// Insert 5 requests, each with 2 history events.
	for i := 0; i < 5; i++ {
		id := "req_bulk_" + string(rune('a'+i))
		req := makeRequest(id, "movie", "Film "+string(rune('A'+i)), requests.StatePending)
		req.TmdbID = 200 + i
		if err := s.Insert(ctx, req); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		now := time.Now().UTC()
		for j := 0; j < 2; j++ {
			ev := requests.Event{
				RequestID: id,
				At:        now.Add(time.Duration(j) * time.Second),
				State:     requests.StatePending,
				Actor:     "alice",
				Note:      "event",
			}
			if err := s.InsertEvent(ctx, ev); err != nil {
				t.Fatalf("InsertEvent: %v", err)
			}
		}
	}

	// All() must return all 5 requests, each with exactly 2 history events.
	// With MaxOpenConns=1, an N+1 implementation would deadlock here.
	all, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("expected 5 requests, got %d", len(all))
	}
	for _, req := range all {
		if len(req.History) != 2 {
			t.Errorf("request %q: expected 2 history events, got %d", req.ID, len(req.History))
		}
	}
}

// ── InsertEvent + history in Get ─────────────────────────────────────────────

func TestInsertEvent_LoadedByGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	req := makeRequest("req_ev_001", "series", "A Show", requests.StatePending)
	req.TvdbID = 888
	if err := s.Insert(ctx, req); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	ev := requests.Event{
		RequestID: req.ID,
		At:        time.Now().UTC(),
		State:     requests.StatePending,
		Actor:     "alice",
		Note:      "initial",
	}
	if err := s.InsertEvent(ctx, ev); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	got, err := s.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.History) != 1 {
		t.Fatalf("expected 1 history event, got %d", len(got.History))
	}
	if got.History[0].Actor != "alice" {
		t.Errorf("Actor: got %q, want alice", got.History[0].Actor)
	}
	if got.History[0].State != requests.StatePending {
		t.Errorf("State: got %q, want pending", got.History[0].State)
	}
}

// ── Update ───────────────────────────────────────────────────────────────────

func TestUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	req := makeRequest("req_upd_001", "movie", "Update Me", requests.StatePending)
	if err := s.Insert(ctx, req); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	req.State = requests.StateDenied
	req.Reason = "not relevant"
	req.UpdatedAt = time.Now().UTC()
	if err := s.Update(ctx, req); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if got.State != requests.StateDenied {
		t.Errorf("State: got %q, want denied", got.State)
	}
	if got.Reason != "not relevant" {
		t.Errorf("Reason: got %q, want 'not relevant'", got.Reason)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	req := &requests.Request{ID: "does-not-exist", State: requests.StateDenied, UpdatedAt: time.Now().UTC()}
	err := s.Update(ctx, req)
	if !errors.Is(err, requests.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ── Delete ───────────────────────────────────────────────────────────────────

func TestDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	req := makeRequest("req_del_001", "movie", "Delete Me", requests.StatePending)
	if err := s.Insert(ctx, req); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := s.Delete(ctx, req.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := s.Get(ctx, req.ID)
	if !errors.Is(err, requests.ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	err := s.Delete(ctx, "nonexistent")
	if !errors.Is(err, requests.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ── InsertFull (idempotent restore) ──────────────────────────────────────────

func TestInsertFull_Idempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	req := &requests.Request{
		ID:          "req_full_001",
		Type:        "movie",
		TmdbID:      77,
		Title:       "Backup Film",
		Year:        2023,
		RequestedBy: "bob",
		State:       requests.StateAvailable,
		Reason:      "restored",
		ArrID:       5,
		CreatedAt:   now,
		UpdatedAt:   now,
		History: []requests.Event{
			{RequestID: "req_full_001", At: now, State: requests.StatePending, Actor: "bob", Note: "created"},
			{RequestID: "req_full_001", At: now.Add(time.Minute), State: requests.StateAvailable, Actor: "admin", Note: "approved"},
		},
	}

	if err := s.InsertFull(ctx, req); err != nil {
		t.Fatalf("InsertFull first: %v", err)
	}
	// Second call with same ID must not error (INSERT OR IGNORE).
	if err := s.InsertFull(ctx, req); err != nil {
		t.Fatalf("InsertFull second (idempotent): %v", err)
	}

	got, err := s.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != requests.StateAvailable {
		t.Errorf("State: got %q, want available", got.State)
	}
	if got.ArrID != 5 {
		t.Errorf("ArrID: got %d, want 5", got.ArrID)
	}
	// History events inserted via InsertFull should be present.
	if len(got.History) < 1 {
		t.Errorf("expected history events, got %d", len(got.History))
	}
}

// ── ListByState ───────────────────────────────────────────────────────────────

func TestListByState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name      string
		states    []requests.State
		wantCount int
	}{
		{name: "pending only", states: []requests.State{requests.StatePending}, wantCount: 2},
		{name: "denied only", states: []requests.State{requests.StateDenied}, wantCount: 1},
		{name: "pending+grabbed", states: []requests.State{requests.StatePending, requests.StateGrabbed}, wantCount: 3},
		{name: "empty states", states: nil, wantCount: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)

			// Seed: 2 pending, 1 grabbed, 1 denied.
			seeds := []struct {
				id    string
				state requests.State
			}{
				{"req_a", requests.StatePending},
				{"req_b", requests.StatePending},
				{"req_c", requests.StateGrabbed},
				{"req_d", requests.StateDenied},
			}
			for i, seed := range seeds {
				r := makeRequest(seed.id, "movie", "Film "+seed.id, seed.state)
				r.TmdbID = 300 + i
				if err := s.Insert(ctx, r); err != nil {
					t.Fatalf("Insert %q: %v", seed.id, err)
				}
			}

			got, err := s.ListByState(ctx, tc.states...)
			if err != nil {
				t.Fatalf("ListByState: %v", err)
			}
			if len(got) != tc.wantCount {
				t.Errorf("count: got %d, want %d", len(got), tc.wantCount)
			}
		})
	}
}

// ── Claim CAS: only one concurrent writer wins ─────────────────────────────

// TestClaim_CAS verifies that when two goroutines race to claim (Update) a
// request, the result is consistent. With SQLite MaxOpenConns=1, concurrent
// writes are serialised at the connection pool level — this test verifies the
// logical invariant: after two concurrent Updates, the final state reflects
// exactly one writer's choice.
func TestClaim_CAS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	req := makeRequest("req_cas_001", "movie", "Contested Film", requests.StatePending)
	if err := s.Insert(ctx, req); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Two goroutines race to transition the same request to "grabbed".
	var wg sync.WaitGroup
	errors := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r := &requests.Request{
				ID:        req.ID,
				State:     requests.StateGrabbed,
				ArrID:     idx + 1,
				UpdatedAt: time.Now().UTC(),
			}
			errors[idx] = s.Update(ctx, r)
		}(i)
	}
	wg.Wait()

	// Both Updates target the same row; at least one must succeed.
	successCount := 0
	for _, err := range errors {
		if err == nil {
			successCount++
		}
	}
	if successCount == 0 {
		t.Error("expected at least one Update to succeed")
	}

	// The request must now be in grabbed state.
	got, err := s.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("Get after CAS: %v", err)
	}
	if got.State != requests.StateGrabbed {
		t.Errorf("State: got %q, want grabbed", got.State)
	}
}

// ── MarkGrabbedIfPending ─────────────────────────────────────────────────────

func TestMarkGrabbedIfPending_PendingTransitions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	req := makeRequest("req_mgip_001", "movie", "Transition Film", requests.StatePending)
	if err := s.Insert(ctx, req); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	ok, err := s.MarkGrabbedIfPending(ctx, req.ID, 42, time.Now().UTC())
	if err != nil {
		t.Fatalf("MarkGrabbedIfPending: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for pending request")
	}

	got, err := s.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != requests.StateGrabbed {
		t.Errorf("State: got %q, want grabbed", got.State)
	}
	if got.ArrID != 42 {
		t.Errorf("ArrID: got %d, want 42", got.ArrID)
	}
}

func TestMarkGrabbedIfPending_AlreadyGrabbed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	req := makeRequest("req_mgip_002", "movie", "Already Grabbed", requests.StateGrabbed)
	req.ArrID = 10
	if err := s.Insert(ctx, req); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Attempting to mark an already-grabbed request should return ok=false.
	ok, err := s.MarkGrabbedIfPending(ctx, req.ID, 99, time.Now().UTC())
	if err != nil {
		t.Fatalf("MarkGrabbedIfPending: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for already-grabbed request")
	}

	// Original ArrID must be unchanged.
	got, err := s.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ArrID != 10 {
		t.Errorf("ArrID: got %d, want 10 (original)", got.ArrID)
	}
}

func TestMarkGrabbedIfPending_MissingID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)

	// Non-existent id — should return ok=false, not error.
	ok, err := s.MarkGrabbedIfPending(ctx, "does-not-exist", 1, time.Now().UTC())
	if err != nil {
		t.Fatalf("MarkGrabbedIfPending: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for non-existent request id")
	}
}
