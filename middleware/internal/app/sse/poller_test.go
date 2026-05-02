package sse

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// stubSvc is a minimal ServiceQuerier for tests.
type stubSvc struct {
	qbtHandler http.Handler
}

func (s *stubSvc) CheckHealth() map[string]string { return map[string]string{} }
func (s *stubSvc) IsWired() bool                  { return false }
func (s *stubSvc) QbtGet(path string) ([]byte, error) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	s.qbtHandler.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		return nil, &httpStatusError{Code: rec.Code, URL: path}
	}
	return rec.Body.Bytes(), nil
}

// newTestClient registers a buffered client against hub and returns it.
// The caller must defer hub.unregister(c) to clean up.
func newTestClient(hub *Hub, cap int) *client {
	c := &client{
		events: make(chan Message, cap),
		done:   make(chan struct{}),
	}
	hub.register(c)
	return c
}

// drainEvents collects all buffered events from c into a slice of event names.
func drainEvents(c *client) []string {
	var names []string
	for len(c.events) > 0 {
		msg := <-c.events
		names = append(names, msg.Event)
	}
	return names
}

// TestPollerBroadcastsOnChange verifies that:
//  1. The first pollOnce broadcasts events for all successfully-fetched types.
//  2. The second pollOnce with identical data broadcasts nothing.
//  3. After data changes on the server, the next pollOnce broadcasts again.
func TestPollerBroadcastsOnChange(t *testing.T) {
	t.Parallel()

	var storageUsed atomic.Int64

	procula := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/procula/jobs":
			w.Write([]byte(`[]`))
		case "/api/procula/storage":
			json.NewEncoder(w).Encode(map[string]any{
				"used":  storageUsed.Load(),
				"total": 100,
			})
		case "/api/procula/notifications":
			w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer procula.Close()

	qbtMux := http.NewServeMux()
	qbtMux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})
	qbtMux.HandleFunc("/api/v2/transfer/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"dl_info_speed":0,"up_info_speed":0}`))
	})

	hub := NewHub()
	svc := &stubSvc{qbtHandler: qbtMux}
	poller := NewPoller(hub, svc, procula.URL, func(_ context.Context, name string, tail int, ts bool) ([]byte, error) {
		return []byte{}, nil
	})
	// Restrict allowed containers to avoid spinning up real docker calls.
	poller.allowedContainers = map[string]bool{}

	c := newTestClient(hub, 64)
	defer hub.unregister(c)

	ctx := context.Background()

	// ── First poll: all event types are new → expect broadcasts ──────────────
	poller.pollOnce(ctx)

	firstCount := len(c.events)
	if firstCount == 0 {
		t.Fatal("expected at least one broadcast on first pollOnce, got 0")
	}
	drainEvents(c)

	// ── Second poll: same data → expect no broadcasts ─────────────────────────
	poller.pollOnce(ctx)

	if got := drainEvents(c); len(got) != 0 {
		t.Errorf("unexpected broadcast(s) on second pollOnce for unchanged data: events=%v", got)
	}

	// ── Third poll: storage changed → at least one broadcast ─────────────────
	storageUsed.Store(42)
	poller.pollOnce(ctx)

	got := drainEvents(c)
	if len(got) == 0 {
		t.Error("expected at least one broadcast after storage data changed, got 0")
	}
	found := false
	for _, ev := range got {
		if ev == "storage" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a 'storage' broadcast after data change, got events: %v", got)
	}
}

// TestPollerTriggerImmediate verifies that TriggerImmediate always broadcasts
// even when the data has not changed (bypasses hash check).
func TestPollerTriggerImmediate(t *testing.T) {
	t.Parallel()

	const fixedStorage = `{"used":0,"total":100}`

	procula := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/procula/storage" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(fixedStorage))
			return
		}
		http.NotFound(w, r)
	}))
	defer procula.Close()

	hub := NewHub()
	svc := &stubSvc{qbtHandler: http.NewServeMux()}
	poller := NewPoller(hub, svc, procula.URL, func(_ context.Context, name string, tail int, ts bool) ([]byte, error) {
		return []byte{}, nil
	})

	c := newTestClient(hub, 64)
	defer hub.unregister(c)

	ctx := context.Background()

	// Pre-seed the hash so a regular poll would suppress this event.
	seededHash := sha256.Sum256([]byte(fixedStorage))
	poller.mu.Lock()
	poller.hashes["storage"] = seededHash
	poller.mu.Unlock()

	// TriggerImmediate must broadcast regardless of stored hash.
	poller.TriggerImmediate(ctx, "storage")

	if len(c.events) == 0 {
		t.Fatal("TriggerImmediate should broadcast unconditionally, got 0 events")
	}

	msg := <-c.events
	if msg.Event != "storage" {
		t.Errorf("expected event=storage, got %q", msg.Event)
	}

	// Validate the payload is valid JSON.
	var payload map[string]any
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Errorf("TriggerImmediate broadcast data is not valid JSON: %v", err)
	}

	// Second call — must still broadcast even though hash is now current.
	poller.TriggerImmediate(ctx, "storage")

	if len(c.events) == 0 {
		t.Error("second TriggerImmediate should also broadcast, got 0 events")
	}
}

// TestFetchLogsTimestampedAndSorted verifies that fetchLogs passes timestamps=true,
// parses RFC3339 prefixes, and returns entries sorted newest-first.
func TestFetchLogsTimestampedAndSorted(t *testing.T) {
	t.Parallel()

	hub := NewHub()
	svc := &stubSvc{qbtHandler: http.NewServeMux()}
	poller := NewPoller(hub, svc, "", func(_ context.Context, name string, tail int, ts bool) ([]byte, error) {
		if !ts {
			t.Errorf("fetchLogs should pass timestamps=true, got false for %q", name)
		}
		switch name {
		case "sonarr":
			return []byte("2024-01-15T12:34:06.000000000Z sonarr newer\n2024-01-15T12:34:04.000000000Z sonarr older\n"), nil
		case "radarr":
			return []byte("2024-01-15T12:34:05.000000000Z radarr middle\n"), nil
		}
		return []byte{}, nil
	})
	// Only test against two services to keep the test hermetic.
	poller.allowedContainers = map[string]bool{"sonarr": true, "radarr": true}

	data, err := poller.fetchLogs(context.Background())
	if err != nil {
		t.Fatalf("fetchLogs error: %v", err)
	}
	var resp struct {
		Entries []LogEntry `json:"entries"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(resp.Entries))
	}
	// Newest first: sonarr newer (T+6) > radarr middle (T+5) > sonarr older (T+4)
	if resp.Entries[0].Line != "sonarr newer" {
		t.Errorf("first entry: got %q, want %q", resp.Entries[0].Line, "sonarr newer")
	}
	if resp.Entries[1].Line != "radarr middle" {
		t.Errorf("second entry: got %q, want %q", resp.Entries[1].Line, "radarr middle")
	}
}

// TestPollerUnknownEventType verifies that TriggerImmediate with an
// unrecognized event type does not panic and sends nothing.
func TestPollerUnknownEventType(t *testing.T) {
	t.Parallel()

	hub := NewHub()
	svc := &stubSvc{qbtHandler: http.NewServeMux()}
	poller := NewPoller(hub, svc, "", func(_ context.Context, name string, tail int, ts bool) ([]byte, error) {
		return []byte{}, nil
	})

	c := newTestClient(hub, 4)
	defer hub.unregister(c)

	// Must not panic.
	poller.TriggerImmediate(context.Background(), "nonexistent_event_type")

	if len(c.events) != 0 {
		t.Errorf("expected 0 broadcasts for unknown event type, got %d", len(c.events))
	}
}
