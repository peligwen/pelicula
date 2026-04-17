package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestSSEPollerBroadcastsOnChange verifies that:
//  1. The first pollOnce broadcasts events for all successfully-fetched types.
//  2. The second pollOnce with identical data broadcasts nothing.
//  3. After data changes on the server, the next pollOnce broadcasts again.
func TestSSEPollerBroadcastsOnChange(t *testing.T) {
	// NOTE: mutates package globals (proculaURL, qbtBaseURL) — cannot use t.Parallel()
	// Use an atomic so the HTTP handler can read an updated value.
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

	qbt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/torrents/info":
			w.Write([]byte(`[]`))
		case "/api/v2/transfer/info":
			w.Write([]byte(`{"dl_info_speed":0,"up_info_speed":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer qbt.Close()

	origProculaURL := proculaURL
	origQbtBaseURL := qbtBaseURL
	proculaURL = procula.URL
	qbtBaseURL = qbt.URL
	t.Cleanup(func() {
		proculaURL = origProculaURL
		qbtBaseURL = origQbtBaseURL
	})

	hub := NewSSEHub()
	svc := &ServiceClients{client: &http.Client{}}
	poller := NewSSEPoller(hub, svc)

	// Register a buffered client so we can inspect received messages.
	c := &sseClient{
		events: make(chan SSEMessage, 64),
		done:   make(chan struct{}),
	}
	hub.register(c)
	defer hub.unregister(c)

	ctx := context.Background()

	// ── First poll: all event types are new → expect broadcasts ──────────────
	poller.pollOnce(ctx)

	firstCount := len(c.events)
	if firstCount == 0 {
		t.Fatal("expected at least one broadcast on first pollOnce, got 0")
	}

	// Drain the channel.
	for len(c.events) > 0 {
		<-c.events
	}

	// ── Second poll: same data → expect no broadcasts ─────────────────────────
	poller.pollOnce(ctx)

	secondCount := len(c.events)
	if secondCount != 0 {
		got := make([]string, 0, secondCount)
		for len(c.events) > 0 {
			msg := <-c.events
			got = append(got, msg.Event)
		}
		t.Errorf("unexpected broadcast(s) on second pollOnce for unchanged data: events=%v", got)
	}

	// ── Third poll: storage changed → at least one broadcast ─────────────────
	storageUsed.Store(42)
	poller.pollOnce(ctx)

	thirdCount := len(c.events)
	if thirdCount == 0 {
		t.Error("expected at least one broadcast after storage data changed, got 0")
	}

	// Verify we got a "storage" event.
	got := make([]string, 0, thirdCount)
	for len(c.events) > 0 {
		msg := <-c.events
		got = append(got, msg.Event)
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

// TestSSEPollerTriggerImmediate verifies that TriggerImmediate always broadcasts
// even when the data has not changed (bypasses hash check).
func TestSSEPollerTriggerImmediate(t *testing.T) {
	// NOTE: mutates package globals (proculaURL, qbtBaseURL) — cannot use t.Parallel()
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

	origProculaURL := proculaURL
	proculaURL = procula.URL
	t.Cleanup(func() { proculaURL = origProculaURL })

	hub := NewSSEHub()
	svc := &ServiceClients{client: &http.Client{}}
	poller := NewSSEPoller(hub, svc)

	// Register a test client.
	c := &sseClient{
		events: make(chan SSEMessage, 64),
		done:   make(chan struct{}),
	}
	hub.register(c)
	defer hub.unregister(c)

	ctx := context.Background()

	// Pre-seed the hash so a regular poll would suppress this event.
	seededHash := sha256.Sum256([]byte(fixedStorage))
	poller.mu.Lock()
	poller.hashes["storage"] = seededHash
	poller.mu.Unlock()

	// TriggerImmediate must broadcast regardless of stored hash.
	// Both sends are synchronous via buffered channel; check immediately.
	poller.TriggerImmediate(ctx, "storage")

	if len(c.events) == 0 {
		t.Fatal("TriggerImmediate should broadcast unconditionally, got 0 events")
	}

	msg := <-c.events
	if msg.Event != "storage" {
		t.Errorf("expected event=storage, got %q", msg.Event)
	}

	// Validate the payload is the raw storage JSON.
	var payload map[string]any
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Errorf("TriggerImmediate broadcast data is not valid JSON: %v", err)
	}

	// Second call — must still broadcast even though hash is now current.
	// Both sends are synchronous via buffered channel; check immediately.
	poller.TriggerImmediate(ctx, "storage")

	if len(c.events) == 0 {
		t.Error("second TriggerImmediate should also broadcast, got 0 events")
	}
}

func TestFetchLogsTimestampedAndSorted(t *testing.T) {
	orig := dockerLogsFunc
	dockerLogsFunc = func(name string, tail int, ts bool) ([]byte, error) {
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
	}
	t.Cleanup(func() { dockerLogsFunc = orig })

	// Temporarily restrict allowedContainers to avoid needing all services
	origAC := allowedContainers
	allowedContainers = map[string]bool{"sonarr": true, "radarr": true}
	t.Cleanup(func() { allowedContainers = origAC })

	p := &SSEPoller{hub: NewSSEHub(), hashes: make(map[string][32]byte)}
	data, err := p.fetchLogs(context.Background())
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

// TestSSEPollerUnknownEventType verifies that TriggerImmediate with an
// unrecognized event type does not panic and sends nothing.
func TestSSEPollerUnknownEventType(t *testing.T) {
	hub := NewSSEHub()
	svc := &ServiceClients{client: &http.Client{}}
	poller := NewSSEPoller(hub, svc)

	c := &sseClient{
		events: make(chan SSEMessage, 4),
		done:   make(chan struct{}),
	}
	hub.register(c)
	defer hub.unregister(c)

	// Must not panic.
	poller.TriggerImmediate(context.Background(), "nonexistent_event_type")

	if len(c.events) != 0 {
		t.Errorf("expected 0 broadcasts for unknown event type, got %d", len(c.events))
	}
}
