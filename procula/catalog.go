package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

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

// Catalog runs after processing completes: triggers Jellyfin library refresh
// via pelicula-api and writes a "content ready" notification to the feed.
func Catalog(job *Job, configDir, peliculaAPI string) {
	log.Printf("[catalog] starting for job %s: %s", job.ID, job.Source.Title)

	// Trigger Jellyfin library refresh via pelicula-api
	if err := triggerJellyfinRefresh(peliculaAPI); err != nil {
		log.Printf("[catalog] Jellyfin refresh failed (non-fatal): %v", err)
	}

	// Write "content ready" notification to the dashboard feed
	event := buildEvent(job, "content_ready", contentReadyMessage(job))
	appendToFeed(configDir, event)
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
		log.Printf("[catalog] failed to marshal notifications: %v", err)
		return
	}
	if err := os.WriteFile(feedPath, data, 0644); err != nil {
		log.Printf("[catalog] failed to write notifications feed: %v", err)
		return
	}
	log.Printf("[catalog] notification written: %s", event.Message)
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
	log.Printf("[catalog] triggered Jellyfin library refresh")
	return nil
}
