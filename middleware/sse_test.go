package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSSEHubBroadcastFanOut verifies that a broadcast reaches all registered clients.
func TestSSEHubBroadcastFanOut(t *testing.T) {
	hub := NewSSEHub()

	clients := make([]*sseClient, 3)
	for i := range clients {
		c := &sseClient{
			events: make(chan SSEMessage, 16),
			done:   make(chan struct{}),
		}
		hub.register(c)
		clients[i] = c
	}

	msg := SSEMessage{Event: "test", Data: []byte(`{"hello":"world"}`), ID: "1"}
	hub.Broadcast(msg)

	for i, c := range clients {
		select {
		case got := <-c.events:
			if got.Event != msg.Event {
				t.Errorf("client %d: expected event %q, got %q", i, msg.Event, got.Event)
			}
			if string(got.Data) != string(msg.Data) {
				t.Errorf("client %d: expected data %q, got %q", i, msg.Data, got.Data)
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("client %d: timed out waiting for broadcast", i)
		}
	}
}

// TestSSEHubBroadcastDropsWhenFull verifies that broadcasting to a full client
// channel does not block.
func TestSSEHubBroadcastDropsWhenFull(t *testing.T) {
	hub := NewSSEHub()

	// Register a client with a buffer of 16 and fill it completely.
	c := &sseClient{
		events: make(chan SSEMessage, 16),
		done:   make(chan struct{}),
	}
	hub.register(c)

	filler := SSEMessage{Event: "fill", Data: []byte("x"), ID: "0"}
	for i := 0; i < 16; i++ {
		c.events <- filler
	}

	// Now broadcast should not block even though the channel is full.
	done := make(chan struct{})
	go func() {
		hub.Broadcast(SSEMessage{Event: "overflow", Data: []byte("y"), ID: "17"})
		close(done)
	}()

	select {
	case <-done:
		// good — completed without blocking
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Broadcast blocked on a full client channel")
	}
}

// TestSSEHubUnregisterCleansUp verifies that unregistering a client removes it
// from the hub and closes its done channel.
func TestSSEHubUnregisterCleansUp(t *testing.T) {
	hub := NewSSEHub()

	c := &sseClient{
		events: make(chan SSEMessage, 16),
		done:   make(chan struct{}),
	}
	hub.register(c)

	hub.mu.RLock()
	_, present := hub.clients[c]
	hub.mu.RUnlock()
	if !present {
		t.Fatal("client not present in hub after register")
	}

	hub.unregister(c)

	hub.mu.RLock()
	_, present = hub.clients[c]
	hub.mu.RUnlock()
	if present {
		t.Fatal("client still present in hub after unregister")
	}

	select {
	case <-c.done:
		// good — channel was closed
	default:
		t.Fatal("done channel was not closed after unregister")
	}
}

// TestSSEHubHandleSSE_WireFormat verifies the SSE wire format written by HandleSSE.
func TestSSEHubHandleSSE_WireFormat(t *testing.T) {
	hub := NewSSEHub()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	// HandleSSE blocks until the context is cancelled. Run it in a goroutine.
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		hub.HandleSSE(rec, req)
	}()

	// Wait for the client to be registered before broadcasting.
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		hub.mu.RLock()
		n := len(hub.clients)
		hub.mu.RUnlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for client to register")
		}
		time.Sleep(5 * time.Millisecond)
	}

	msg := SSEMessage{
		Event: "download",
		Data:  []byte(`{"id":42}`),
		ID:    "99",
	}
	hub.Broadcast(msg)

	// Give HandleSSE time to write the frame, then cancel the context.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-handlerDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("HandleSSE did not return after context cancellation")
	}

	body := rec.Body.String()
	expected := "event: download\ndata: {\"id\":42}\nid: 99\n\n"
	if !strings.Contains(body, expected) {
		t.Errorf("SSE wire format mismatch\nwant substring: %q\ngot body:       %q", expected, body)
	}
}
