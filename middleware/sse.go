package main

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// SSEMessage is a single Server-Sent Events frame.
type SSEMessage struct {
	Event string
	Data  []byte
	ID    string
}

// sseClient is a connected SSE subscriber.
type sseClient struct {
	events chan SSEMessage
	done   chan struct{}
}

// SSEHub manages all connected SSE clients and fans out broadcast messages.
type SSEHub struct {
	mu      sync.RWMutex
	clients map[*sseClient]struct{}
	nextID  uint64
}

// NewSSEHub returns an initialized SSEHub.
func NewSSEHub() *SSEHub {
	return &SSEHub{
		clients: make(map[*sseClient]struct{}),
	}
}

// register adds c to the hub. Must be called before the client starts reading.
func (h *SSEHub) register(c *sseClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
}

// unregister removes c from the hub and signals it to stop.
func (h *SSEHub) unregister(c *sseClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
	close(c.done)
}

// Broadcast sends msg to every registered client. The send is non-blocking;
// if a client's buffer (capacity 16) is full the message is dropped for that
// client rather than blocking the broadcaster.
func (h *SSEHub) Broadcast(msg SSEMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.events <- msg:
		default:
			// client too slow — drop rather than block
		}
	}
}

// nextEventID atomically increments the hub's counter and returns it as a
// decimal string suitable for an SSE "id" field.
func (h *SSEHub) nextEventID() string {
	id := atomic.AddUint64(&h.nextID, 1)
	return fmt.Sprintf("%d", id)
}

// HandleSSE is an http.HandlerFunc that upgrades the connection to an
// SSE stream for the lifetime of the request.
func (h *SSEHub) HandleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		// ResponseController.Flush returns an error if the underlying
		// ResponseWriter doesn't implement http.Flusher.
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	c := &sseClient{
		events: make(chan SSEMessage, 16),
		done:   make(chan struct{}),
	}
	h.register(c)
	defer h.unregister(c)

	// Flush initial headers so the client sees the 200 immediately.
	_ = rc.Flush()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case msg := <-c.events:
			fmt.Fprintf(w, "event: %s\ndata: %s\nid: %s\n\n", msg.Event, msg.Data, msg.ID)
			_ = rc.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			_ = rc.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
