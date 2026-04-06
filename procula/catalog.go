package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var feedMu sync.Mutex

// NotificationEvent is an entry in the dashboard notification feed.
type NotificationEvent struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`       // "content_ready", "validation_failed"
	Title     string    `json:"title"`
	Year      int       `json:"year,omitempty"`
	MediaType string    `json:"media_type"` // "movie" or "episode"
	Message   string    `json:"message"`
}

const maxFeedEvents = 50

// NotificationConfig controls how Procula sends external notifications.
// Written to /config/procula/notifications.json by `./pelicula configure`.
type NotificationConfig struct {
	Mode        string   `json:"mode"`         // "internal", "apprise", "direct"
	AppriseURLs []string `json:"apprise_urls"` // provider URLs passed to the Apprise container
	DirectURL   string   `json:"direct_url"`   // single webhook URL for "direct" mode
}

// Catalog runs after processing completes: triggers Jellyfin library refresh
// via pelicula-api and writes a "content ready" notification to the feed.
func Catalog(job *Job, configDir, peliculaAPI string) {
	slog.Info("cataloging job", "component", "catalog", "job_id", job.ID, "title", job.Source.Title)

	// Trigger Jellyfin library refresh via pelicula-api
	if err := triggerJellyfinRefresh(peliculaAPI); err != nil {
		slog.Warn("Jellyfin refresh failed (non-fatal)", "component", "catalog", "error", err)
	}

	// Write "content ready" notification to the dashboard feed
	event := buildEvent(job, "content_ready", contentReadyMessage(job))
	appendToFeed(configDir, event)

	// Send external notification if configured
	cfg := loadNotificationConfig(configDir)
	sendExternalNotification(cfg, event)
}

// WriteValidationFailedNotification writes a failed notification from the pipeline.
func WriteValidationFailedNotification(job *Job, configDir, reason string) {
	msg := fmt.Sprintf("Validation failed: %s — %s", job.Source.Title, reason)
	event := buildEvent(job, "validation_failed", msg)
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

func buildEvent(job *Job, eventType, message string) NotificationEvent {
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
	}
}

func appendToFeed(configDir string, event NotificationEvent) {
	feedPath := filepath.Join(configDir, "procula", "notifications_feed.json")

	feedMu.Lock()
	defer feedMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(feedPath), 0755); err != nil {
		slog.Error("failed to create feed directory", "component", "catalog", "error", err)
		return
	}

	var events []NotificationEvent
	if data, err := os.ReadFile(feedPath); err == nil {
		json.Unmarshal(data, &events) //nolint:errcheck
	}

	// Prepend new event, cap at maxFeedEvents
	events = append([]NotificationEvent{event}, events...)
	if len(events) > maxFeedEvents {
		events = events[:maxFeedEvents]
	}

	data, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		slog.Error("failed to marshal notifications", "component", "catalog", "error", err)
		return
	}
	if err := os.WriteFile(feedPath, data, 0644); err != nil {
		slog.Error("failed to write notifications feed", "component", "catalog", "error", err)
		return
	}
	slog.Info("notification written", "component", "catalog", "message", event.Message)
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

func triggerJellyfinRefresh(peliculaAPI string) error {
	url := peliculaAPI + "/api/pelicula/jellyfin/refresh"
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", nil)
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
