package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EventType is the type of a pipeline event.
type EventType string

const (
	EventValidationPassed   EventType = "validation_passed"
	EventValidationFailed   EventType = "validation_failed"
	EventDualSubDone        EventType = "dualsub_done"
	EventDualSubFailed      EventType = "dualsub_failed"
	EventTranscodeStarted   EventType = "transcode_started"
	EventTranscodeDone      EventType = "transcode_done"
	EventTranscodeFailed    EventType = "transcode_failed"
	EventCatalogRefreshed   EventType = "catalog_refreshed"
	EventJobCancelled       EventType = "job_cancelled"
	EventJobRetried         EventType = "job_retried"
	EventReleaseBlocklisted EventType = "release_blocklisted"
)

// PipelineEvent is a single entry in the append-only event log.
type PipelineEvent struct {
	ID        string         `json:"id"`
	Timestamp time.Time      `json:"timestamp"`
	Type      EventType      `json:"type"`
	JobID     string         `json:"job_id"`
	Title     string         `json:"title"`
	Year      int            `json:"year,omitempty"`
	MediaType string         `json:"media_type,omitempty"` // "movie" or "episode"
	Stage     string         `json:"stage,omitempty"`
	Duration  float64        `json:"duration_s,omitempty"` // wall-clock seconds for the stage
	Details   map[string]any `json:"details,omitempty"`
	Message   string         `json:"message"`
}

// EventLog is an append-only JSONL event log stored at $CONFIG_DIR/procula/events.jsonl.
// Rotation: when the file exceeds maxEventLogBytes it is renamed to events.jsonl.1.
type EventLog struct {
	path string
	mu   sync.Mutex
}

const maxEventLogBytes = 5 * 1024 * 1024 // 5 MB

// NewEventLog creates an EventLog that writes to $configDir/procula/events.jsonl.
// The directory is created if missing.
func NewEventLog(configDir string) (*EventLog, error) {
	dir := filepath.Join(configDir, "procula")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create event log dir: %w", err)
	}
	return &EventLog{path: filepath.Join(dir, "events.jsonl")}, nil
}

// Append serialises evt as a JSON line and appends it to the log file.
// Rotation is attempted if the file exceeds maxEventLogBytes.
func (el *EventLog) Append(evt PipelineEvent) {
	if evt.ID == "" {
		evt.ID = fmt.Sprintf("evt_%d_%s", time.Now().UnixNano(), randStr(6))
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now().UTC()
	}

	line, err := json.Marshal(evt)
	if err != nil {
		slog.Error("failed to marshal event", "component", "events", "error", err)
		return
	}
	line = append(line, '\n')

	el.mu.Lock()
	defer el.mu.Unlock()

	el.maybeRotate()

	f, err := os.OpenFile(el.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		slog.Error("failed to open event log", "component", "events", "error", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		slog.Error("failed to write event", "component", "events", "error", err)
	}
}

// maybeRotate renames events.jsonl → events.jsonl.1 when the file is too large.
// Caller must hold el.mu.
func (el *EventLog) maybeRotate() {
	fi, err := os.Stat(el.path)
	if err != nil || fi.Size() < maxEventLogBytes {
		return
	}
	backup := el.path + ".1"
	if err := os.Rename(el.path, backup); err != nil {
		slog.Warn("event log rotation failed", "component", "events", "error", err)
	}
}

// EventFilter carries optional query filters for Read.
type EventFilter struct {
	Type string // empty = all types
}

// Read returns up to limit events ending at (offset+limit-1) in reverse-chronological
// order (newest first). If both the primary log and the .1 backup exist, they are
// merged transparently so callers can paginate across rotations.
// Returns the events slice and the total number of matching lines available.
func (el *EventLog) Read(limit, offset int, f EventFilter) ([]PipelineEvent, int) {
	el.mu.Lock()
	defer el.mu.Unlock()

	lines := el.readLines(el.path)
	lines = append(lines, el.readLines(el.path+".1")...)

	// Filter
	if f.Type != "" {
		filtered := lines[:0]
		for _, l := range lines {
			if strings.Contains(l, `"`+f.Type+`"`) {
				filtered = append(filtered, l)
			}
		}
		lines = filtered
	}

	total := len(lines)

	// Reverse (newest first)
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}

	// Apply offset + limit
	if offset >= len(lines) {
		return []PipelineEvent{}, total
	}
	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}
	lines = lines[offset:end]

	out := make([]PipelineEvent, 0, len(lines))
	for _, l := range lines {
		var evt PipelineEvent
		if err := json.Unmarshal([]byte(l), &evt); err == nil {
			out = append(out, evt)
		}
	}
	return out, total
}

// readLines reads all non-empty lines from a file. Missing files return nil.
func (el *EventLog) readLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256*1024), 256*1024)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// global event log, initialised in main().
var eventLog *EventLog

// emitEvent is a convenience wrapper that no-ops when eventLog is nil
// (e.g. during unit tests that don't set up a config dir).
func emitEvent(evt PipelineEvent) {
	if eventLog == nil {
		return
	}
	eventLog.Append(evt)
}

// handleListEvents serves GET /api/procula/events?limit=&offset=&type=
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	limit := 100
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	filter := EventFilter{Type: r.URL.Query().Get("type")}
	events, total := eventLog.Read(limit, offset, filter)
	writeJSON(w, map[string]any{
		"events": events,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}
