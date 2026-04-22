package procula

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
func CatalogEarly(job *Job, configDir, peliculaAPI string) {
	slog.Info("cataloging job (early)", "component", "catalog", "job_id", job.ID, "title", job.Source.Title)

	// Trigger Jellyfin library refresh via pelicula-api
	if err := triggerJellyfinRefresh(peliculaAPI); err != nil {
		slog.Warn("Jellyfin refresh failed (non-fatal)", "component", "catalog", "error", err)
	}

	// Write "content ready" notification to the dashboard feed
	event := buildEvent(job, "content_ready", contentReadyMessage(job), "")
	appendToFeed(configDir, event)

	emitEvent(PipelineEvent{
		Type:      EventCatalogRefreshed,
		JobID:     job.ID,
		Title:     job.Source.Title,
		Year:      job.Source.Year,
		MediaType: job.Source.Type,
		Stage:     string(StageCatalog),
		Message:   contentReadyMessage(job),
	})

	// Send external notification if configured
	cfg := loadNotificationConfig(configDir)
	sendExternalNotification(cfg, event)
}

// CatalogLate runs after a transcoded sidecar has been written alongside the
// original. It triggers a second (silent) Jellyfin refresh so the alternate
// version appears in the version picker. No duplicate notification is emitted.
func CatalogLate(job *Job, peliculaAPI string) {
	slog.Info("cataloging job (late refresh for sidecar)", "component", "catalog", "job_id", job.ID, "title", job.Source.Title)
	if err := triggerJellyfinRefresh(peliculaAPI); err != nil {
		slog.Warn("late Jellyfin refresh failed (non-fatal)", "component", "catalog", "error", err)
	}
}

// WriteValidationFailedNotification writes a failed notification from the pipeline.
func WriteValidationFailedNotification(job *Job, configDir, reason string) {
	msg := fmt.Sprintf("Validation failed: %s — %s", job.Source.Title, reason)
	event := buildEvent(job, "validation_failed", msg, reason)
	appendToFeed(configDir, event)
}

// WriteTranscodeFailedNotification writes a transcode failure notification to the feed.
// The job continues with the original file; this is informational.
func WriteTranscodeFailedNotification(job *Job, configDir, reason string) {
	msg := fmt.Sprintf("Transcode failed: %s — %s", job.Source.Title, reason)
	event := buildEvent(job, "transcode_failed", msg, reason)
	appendToFeed(configDir, event)
}

func contentReadyMessage(job *Job) string {
	if job.Source.Type == "movie" {
		if job.Source.Year > 0 {
			return fmt.Sprintf("Movie ready: %s (%d)", job.Source.Title, job.Source.Year)
		}
		return fmt.Sprintf("Movie ready: %s", job.Source.Title)
	}
	return fmt.Sprintf("Episode ready: %s", job.Source.Title)
}

func buildEvent(job *Job, eventType, message, detail string) NotificationEvent {
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

// appendToFeed writes a notification event to the SQLite notifications table.
// If appDB is nil (e.g. in unit tests) it falls back to the JSONL file so
// existing tests that don't initialise the DB continue to work.
func appendToFeed(configDir string, event NotificationEvent) {
	if appDB != nil {
		if err := insertNotification(appDB, event); err != nil {
			slog.Error("failed to write notification to DB", "component", "catalog", "error", err)
		} else {
			slog.Info("notification written", "component", "catalog", "message", event.Message)
		}
		return
	}
	// Fallback: write to JSONL file (used when DB is not available).
	appendToFeedFile(configDir, event)
}

// insertNotification persists a single notification event to SQLite.
func insertNotification(db *sql.DB, event NotificationEvent) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO notifications (id, timestamp, type, title, year, media_type, message, detail, job_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID,
		event.Timestamp.UTC().Format(time.RFC3339Nano),
		event.Type,
		event.Title,
		event.Year,
		event.MediaType,
		event.Message,
		event.Detail,
		event.JobID,
	)
	return err
}

// appendToFeedFile is the legacy JSONL write path, kept as a fallback.
func appendToFeedFile(configDir string, event NotificationEvent) {
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

	migrateFeedIfLegacy(feedPath)

	f, err := os.OpenFile(feedPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		slog.Error("failed to open feed file", "component", "catalog", "error", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		slog.Error("failed to write notification", "component", "catalog", "error", err)
	}
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

// ReadFeed returns notification events newest-first, pruned to the last 7 days
// and capped at maxFeedEvents. Reads from SQLite when the DB is available,
// falling back to the JSONL file otherwise (for tests and cold-start scenarios).
func ReadFeed(configDir string) []NotificationEvent {
	if appDB != nil {
		events, err := readNotificationsFromDB(appDB)
		if err != nil {
			slog.Warn("failed to read notifications from DB", "component", "catalog", "error", err)
		} else {
			return events
		}
	}
	return readFeedFromFile(configDir)
}

// readNotificationsFromDB reads notifications from SQLite, newest-first,
// within a 7-day window, capped at maxFeedEvents.
func readNotificationsFromDB(db *sql.DB) ([]NotificationEvent, error) {
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)
	rows, err := db.Query(
		`SELECT id, timestamp, type, title, year, media_type, message, detail, job_id
		 FROM notifications
		 WHERE timestamp >= ?
		 ORDER BY timestamp DESC
		 LIMIT ?`,
		cutoff, maxFeedEvents,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []NotificationEvent
	for rows.Next() {
		var ev NotificationEvent
		var tsStr string
		if err := rows.Scan(
			&ev.ID, &tsStr, &ev.Type, &ev.Title, &ev.Year,
			&ev.MediaType, &ev.Message, &ev.Detail, &ev.JobID,
		); err != nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
			ev.Timestamp = t
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

// readFeedFromFile reads notifications from the JSONL file (fallback path).
func readFeedFromFile(configDir string) []NotificationEvent {
	feedPath := filepath.Join(configDir, "procula", "notifications_feed.json")
	migrateFeedIfLegacy(feedPath)

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

	// Reverse to newest-first.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	if len(events) > maxFeedEvents {
		events = events[:maxFeedEvents]
	}
	return events
}

// migrateNotificationsFeedToDB imports any existing JSONL notifications feed into
// the SQLite notifications table. Runs once at startup; idempotent (INSERT OR IGNORE).
// After import the JSONL file is renamed to .migrated so it is not processed again.
func migrateNotificationsFeedToDB(db *sql.DB, configDir string) {
	feedPath := filepath.Join(configDir, "procula", "notifications_feed.json")
	migratedPath := feedPath + ".migrated"

	// If already migrated, nothing to do.
	if _, err := os.Stat(migratedPath); err == nil {
		return
	}

	f, err := os.Open(feedPath)
	if err != nil {
		return // file doesn't exist yet; nothing to import
	}

	// Migrate legacy JSON-array to JSONL in memory before importing.
	data, _ := os.ReadFile(feedPath)
	f.Close()

	var events []NotificationEvent
	if len(data) > 0 && data[0] == '[' {
		// Legacy JSON-array format.
		if err := json.Unmarshal(data, &events); err != nil {
			slog.Warn("notifications feed migration: could not parse JSON array", "component", "catalog", "error", err)
			return
		}
	} else {
		// JSONL format — parse line by line.
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 256*1024), 256*1024)
		for sc.Scan() {
			l := strings.TrimSpace(sc.Text())
			if l == "" {
				continue
			}
			var ev NotificationEvent
			if err := json.Unmarshal([]byte(l), &ev); err == nil {
				events = append(events, ev)
			}
		}
	}

	imported := 0
	for _, ev := range events {
		if err := insertNotification(db, ev); err == nil {
			imported++
		}
	}

	// Rename the file so we don't import it again.
	if err := os.Rename(feedPath, migratedPath); err != nil {
		slog.Warn("notifications feed migration: could not rename feed file", "component", "catalog", "error", err)
	}

	if imported > 0 {
		slog.Info("notifications feed migrated to SQLite", "component", "catalog", "count", imported)
	}
}

func loadNotificationConfig(configDir string) *NotificationConfig {
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
	client := newProculaClient(10 * time.Second)
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
	client := newProculaClient(10 * time.Second)
	resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(data))
	if err != nil {
		slog.Error("direct notification failed", "component", "catalog", "error", err)
		return
	}
	resp.Body.Close()
	slog.Info("direct notification sent", "component", "catalog")
}

func triggerJellyfinRefresh(peliculaAPI string) error {
	target := peliculaAPI + "/api/pelicula/jellyfin/refresh"
	client := newProculaClient(10 * time.Second)
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
