// Package catalog handles post-validation/transcode cataloging:
// notification feed (JSONL), pipeline event log, Jellyfin refresh,
// and external notifications (Apprise / direct webhook).
package catalog

import (
	"bufio"
	"bytes"
	cryptorand "crypto/rand"
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

	"procula/internal/queue"
)

// ── Notification feed ─────────────────────────────────────────────────────────

var feedMu sync.Mutex

// NotificationEvent is an entry in the dashboard notification feed.
type NotificationEvent struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"` // "content_ready", "validation_failed", "transcode_failed"
	Title     string    `json:"title"`
	Year      int       `json:"year,omitempty"`
	MediaType string    `json:"media_type"` // "movie" or "episode"
	Message   string    `json:"message"`
	Detail    string    `json:"detail,omitempty"` // error text for drawer; empty for content_ready
	JobID     string    `json:"job_id,omitempty"` // procula job ID; enables Retry action
}

const maxFeedEvents = 50

// NotificationConfig controls how Procula sends external notifications.
// Written to /config/procula/notifications.json by the dashboard Settings tab.
type NotificationConfig struct {
	Mode        string   `json:"mode"`         // "internal", "apprise", "direct"
	AppriseURLs []string `json:"apprise_urls"` // provider URLs passed to the Apprise container
	DirectURL   string   `json:"direct_url"`   // single webhook URL for "direct" mode
}

// CatalogEarly runs immediately after validation: tells Jellyfin about the new
// file so it becomes watchable right away, writes the "content ready"
// notification, and sends any configured external notification.
// The file is already in the library (hardlinked by *arr) before this runs.
func CatalogEarly(job *queue.Job, configDir, peliculaAPI string, el *EventLog) {
	slog.Info("cataloging job (early)", "component", "catalog", "job_id", job.ID, "title", job.Source.Title)

	// Trigger Jellyfin library refresh via pelicula-api
	if err := TriggerJellyfinRefresh(peliculaAPI); err != nil {
		slog.Warn("Jellyfin refresh failed (non-fatal)", "component", "catalog", "error", err)
	}

	// Write "content ready" notification to the dashboard feed
	event := buildEvent(job, "content_ready", contentReadyMessage(job), "")
	AppendToFeed(configDir, event)

	if el != nil {
		el.Append(PipelineEvent{
			Type:      EventCatalogRefreshed,
			JobID:     job.ID,
			Title:     job.Source.Title,
			Year:      job.Source.Year,
			MediaType: job.Source.Type,
			Stage:     string(queue.StageCatalog),
			Message:   contentReadyMessage(job),
		})
	}

	// Send external notification if configured
	cfg := LoadNotificationConfig(configDir)
	sendExternalNotification(cfg, event)
}

// CatalogLate runs after a transcoded sidecar has been written alongside the
// original. It triggers a second (silent) Jellyfin refresh so the alternate
// version appears in the version picker. No duplicate notification is emitted.
func CatalogLate(job *queue.Job, peliculaAPI string) {
	slog.Info("cataloging job (late refresh for sidecar)", "component", "catalog", "job_id", job.ID, "title", job.Source.Title)
	if err := TriggerJellyfinRefresh(peliculaAPI); err != nil {
		slog.Warn("late Jellyfin refresh failed (non-fatal)", "component", "catalog", "error", err)
	}
}

// WriteValidationFailedNotification writes a failed notification from the pipeline.
func WriteValidationFailedNotification(job *queue.Job, configDir, reason string) {
	msg := fmt.Sprintf("Validation failed: %s — %s", job.Source.Title, reason)
	event := buildEvent(job, "validation_failed", msg, reason)
	AppendToFeed(configDir, event)
}

// WriteTranscodeFailedNotification writes a transcode failure notification to the feed.
// The job continues with the original file; this is informational.
func WriteTranscodeFailedNotification(job *queue.Job, configDir, reason string) {
	msg := fmt.Sprintf("Transcode failed: %s — %s", job.Source.Title, reason)
	event := buildEvent(job, "transcode_failed", msg, reason)
	AppendToFeed(configDir, event)
}

func contentReadyMessage(job *queue.Job) string {
	if job.Source.Type == "movie" {
		if job.Source.Year > 0 {
			return fmt.Sprintf("Movie ready: %s (%d)", job.Source.Title, job.Source.Year)
		}
		return fmt.Sprintf("Movie ready: %s", job.Source.Title)
	}
	return fmt.Sprintf("Episode ready: %s", job.Source.Title)
}

func buildEvent(job *queue.Job, eventType, message, detail string) NotificationEvent {
	suffix := job.ID
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return NotificationEvent{
		ID:        fmt.Sprintf("notif_%d_%s", time.Now().UnixNano(), suffix),
		Timestamp: time.Now().UTC(),
		Type:      eventType,
		Title:     job.Source.Title,
		Year:      job.Source.Year,
		MediaType: job.Source.Type,
		Message:   message,
		Detail:    detail,
		JobID:     job.ID,
	}
}

// AppendToFeed appends event as a single JSON line to the JSONL feed file.
// On first use it migrates a legacy JSON-array feed to JSONL in place.
// The mutex is held only for the file open+write, keeping critical sections short.
func AppendToFeed(configDir string, event NotificationEvent) {
	feedPath := filepath.Join(configDir, "procula", "notifications_feed.json")

	if err := os.MkdirAll(filepath.Dir(feedPath), 0755); err != nil {
		slog.Error("failed to create feed directory", "component", "catalog", "error", err)
		return
	}

	line, err := json.Marshal(event)
	if err != nil {
		slog.Error("failed to marshal notification event", "component", "catalog", "error", err)
		return
	}
	line = append(line, '\n')

	feedMu.Lock()
	defer feedMu.Unlock()

	// One-shot migration: if the file starts with '[', it's the old JSON-array
	// format. Read all events from it and rewrite as JSONL before appending.
	migrateFeedIfLegacy(feedPath)

	f, err := os.OpenFile(feedPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		slog.Error("failed to open feed file", "component", "catalog", "error", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		slog.Error("failed to write notification", "component", "catalog", "error", err)
		return
	}
	slog.Info("notification written", "component", "catalog", "message", event.Message)
}

// migrateFeedIfLegacy rewrites a legacy JSON-array feed to JSONL in place.
// Caller must hold feedMu.
func migrateFeedIfLegacy(feedPath string) {
	f, err := os.Open(feedPath)
	if err != nil {
		return // file doesn't exist yet — nothing to migrate
	}
	first := make([]byte, 1)
	_, err = f.Read(first)
	f.Close()
	if err != nil || first[0] != '[' {
		return // not a JSON array — already JSONL or empty
	}

	data, err := os.ReadFile(feedPath)
	if err != nil {
		slog.Warn("feed migration: could not read file", "component", "catalog", "error", err)
		return
	}
	var events []NotificationEvent
	if err := json.Unmarshal(data, &events); err != nil {
		slog.Warn("feed migration: could not unmarshal legacy array, leaving as-is", "component", "catalog", "error", err)
		return
	}

	var buf bytes.Buffer
	for _, ev := range events {
		line, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(feedPath, buf.Bytes(), 0644); err != nil {
		slog.Warn("feed migration: could not rewrite as JSONL", "component", "catalog", "error", err)
		return
	}
	slog.Info("migrated notifications feed from JSON array to JSONL", "component", "catalog", "events", len(events))
}

// ReadFeed reads the JSONL feed file and returns events newest-first,
// pruned to the last 7 days and capped at maxFeedEvents.
// Caller must NOT hold feedMu.
func ReadFeed(configDir string) []NotificationEvent {
	feedPath := filepath.Join(configDir, "procula", "notifications_feed.json")

	feedMu.Lock()
	migrateFeedIfLegacy(feedPath)
	feedMu.Unlock()

	f, err := os.Open(feedPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
	var events []NotificationEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256*1024), 256*1024)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" {
			continue
		}
		var ev NotificationEvent
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			continue
		}
		if ev.Timestamp.After(cutoff) {
			events = append(events, ev)
		}
	}

	// Reverse to newest-first
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	if len(events) > maxFeedEvents {
		events = events[:maxFeedEvents]
	}
	return events
}

// LoadNotificationConfig loads the notification config from disk.
func LoadNotificationConfig(configDir string) *NotificationConfig {
	path := filepath.Join(configDir, "procula", "notifications.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return &NotificationConfig{Mode: "internal"}
	}
	var cfg NotificationConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &NotificationConfig{Mode: "internal"}
	}
	return &cfg
}

func sendExternalNotification(cfg *NotificationConfig, event NotificationEvent) {
	switch cfg.Mode {
	case "apprise":
		if len(cfg.AppriseURLs) == 0 {
			return
		}
		sendApprise(cfg.AppriseURLs, event)
	case "direct":
		if cfg.DirectURL == "" {
			return
		}
		sendDirect(cfg.DirectURL, event)
	}
}

// sendApprise sends a notification via the Apprise container at http://apprise:8000.
// AppriseURLs are provider URLs like "ntfy://topic", "gotify://host/token", etc.
func sendApprise(urls []string, event NotificationEvent) {
	payload := map[string]any{
		"title": event.Title,
		"body":  event.Message,
		"type":  "info",
		"urls":  strings.Join(urls, ","),
	}
	data, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post("http://apprise:8000/notify", "application/json", bytes.NewReader(data))
	if err != nil {
		slog.Error("Apprise notification failed", "component", "catalog", "error", err)
		return
	}
	resp.Body.Close()
	slog.Info("Apprise notification sent", "component", "catalog", "url_count", len(urls))
}

// sendDirect sends a notification as a JSON HTTP POST to an arbitrary webhook URL.
// Compatible with ntfy HTTP API, Gotify, and generic webhook receivers.
func sendDirect(webhookURL string, event NotificationEvent) {
	payload := map[string]any{
		"title":   event.Title,
		"message": event.Message,
		"type":    event.Type,
	}
	data, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(data))
	if err != nil {
		slog.Error("direct notification failed", "component", "catalog", "error", err)
		return
	}
	resp.Body.Close()
	slog.Info("direct notification sent", "component", "catalog")
}

// TriggerJellyfinRefresh POSTs to the pelicula-api Jellyfin refresh endpoint.
func TriggerJellyfinRefresh(peliculaAPI string) error {
	target := peliculaAPI + "/api/pelicula/jellyfin/refresh"
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodPost, target, nil)
	if err != nil {
		return err
	}
	// Authenticate with the shared Procula API key so the middleware can verify
	// the caller is Procula and not an external request.
	if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
		req.Header.Set("X-API-Key", key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	slog.Info("triggered Jellyfin library refresh", "component", "catalog")
	return nil
}

// ── Event log ─────────────────────────────────────────────────────────────────

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
	EventSubAcquired        EventType = "sub_acquired" // fired per-language when a sidecar is detected
	EventSubTimeout         EventType = "sub_timeout"  // fired when await_subs times out with missing langs
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

	// Filter by event type using exact JSON field matching.
	if f.Type != "" {
		type typeOnly struct {
			Type string `json:"type"`
		}
		filtered := lines[:0]
		for _, l := range lines {
			var ev typeOnly
			if err := json.Unmarshal([]byte(l), &ev); err != nil {
				continue // skip unparseable lines rather than including them
			}
			if ev.Type == f.Type {
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

// HandleListEvents serves GET /api/procula/events?limit=&offset=&type=
// It is called from the httpapi package via the Server type that embeds EventLog.
func HandleListEvents(el *EventLog, w http.ResponseWriter, r *http.Request, writeJSON func(http.ResponseWriter, any)) {
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
	events, total := el.Read(limit, offset, filter)
	writeJSON(w, map[string]any{
		"events": events,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// randStr generates a short random hex string of length n for use in event IDs.
func randStr(n int) string {
	if n < 1 {
		n = 6
	}
	b := make([]byte, (n+1)/2)
	if _, err := cryptorand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())[:n]
	}
	s := fmt.Sprintf("%x", b)
	if len(s) > n {
		return s[:n]
	}
	return s
}
