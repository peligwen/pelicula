// Package sse implements the Server-Sent Events hub and polling logic for
// the pelicula-api dashboard real-time updates.
package sse

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Message is a single Server-Sent Events frame.
type Message struct {
	Event string
	// Data must contain valid JSON (no raw newlines). Multi-line data is split
	// into multiple SSE "data:" lines per spec, but callers should avoid
	// embedding literal newlines — use compact JSON instead.
	Data []byte
	ID   string
}

// client is a connected SSE subscriber.
type client struct {
	events    chan Message
	done      chan struct{}
	closeOnce sync.Once
}

// closeDone closes the done channel exactly once.
func (c *client) closeDone() {
	c.closeOnce.Do(func() { close(c.done) })
}

// Hub manages all connected SSE clients and fans out broadcast messages.
type Hub struct {
	mu      sync.RWMutex
	clients map[*client]struct{}
	nextID  uint64
}

// NewHub returns an initialized Hub.
func NewHub() *Hub {
	return &Hub{
		clients: make(map[*client]struct{}),
	}
}

// register adds c to the hub. Must be called before the client starts reading.
func (h *Hub) register(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
}

// unregister removes c from the hub and signals it to stop.
func (h *Hub) unregister(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
	c.closeDone()
}

// Broadcast sends msg to every registered client. The send is non-blocking;
// if a client's buffer (capacity 16) is full the message is dropped for that
// client rather than blocking the broadcaster. If msg.ID is empty, a
// monotonically increasing ID is auto-assigned via nextEventID.
func (h *Hub) Broadcast(msg Message) {
	if msg.ID == "" {
		msg.ID = h.nextEventID()
	}
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
func (h *Hub) nextEventID() string {
	id := atomic.AddUint64(&h.nextID, 1)
	return strconv.FormatUint(id, 10)
}

// HandleSSE is an http.HandlerFunc that upgrades the connection to an
// SSE stream for the lifetime of the request.
func (h *Hub) HandleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Set SSE headers before any write so they are transmitted with the 200.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Probe flush capability; ResponseController.Flush returns an error if the
	// underlying ResponseWriter doesn't implement http.Flusher.
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	c := &client{
		events: make(chan Message, 16),
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
			// Write SSE frame, handling multi-line data per spec.
			dataStr := strings.ReplaceAll(string(msg.Data), "\n", "\ndata: ")
			fmt.Fprintf(w, "event: %s\ndata: %s\nid: %s\n\n", msg.Event, dataStr, msg.ID)
			_ = rc.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			_ = rc.Flush()
		case <-c.done:
			return
		case <-r.Context().Done():
			return
		}
	}
}
