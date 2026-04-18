// Package apprise provides a client for the Apprise notification container.
// It reads notification configuration from the procula notifications.json
// file and forwards notifications when mode is "apprise".
package apprise

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type notifConfig struct {
	Mode        string   `json:"mode"`
	AppriseURLs []string `json:"apprise_urls"`
}

// Client is a client for the Apprise notification container.
type Client struct {
	url       string
	configDir string
	http      *http.Client
}

// New constructs a Client.
// url is the Apprise notify endpoint (e.g. cfg.URLs.Apprise).
// configDir is the root config directory (e.g. cfg.ConfigDir); the client
// reads <configDir>/procula/notifications.json to determine the active mode.
func New(url, configDir string) *Client {
	return &Client{
		url:       url,
		configDir: configDir,
		http:      &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) loadConfig() *notifConfig {
	data, err := os.ReadFile(filepath.Join(c.configDir, "procula", "notifications.json"))
	if err != nil {
		return &notifConfig{Mode: "internal"}
	}
	var cfg notifConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &notifConfig{Mode: "internal"}
	}
	return &cfg
}

// Notify sends a notification via the Apprise container if configured.
// Non-fatal: logs on error and returns.
func (c *Client) Notify(title, body string) {
	cfg := c.loadConfig()
	if cfg.Mode != "apprise" || len(cfg.AppriseURLs) == 0 {
		return
	}
	payload := map[string]any{
		"title": title,
		"body":  body,
		"type":  "info",
		"urls":  strings.Join(cfg.AppriseURLs, ","),
	}
	data, _ := json.Marshal(payload)
	resp, err := c.http.Post(c.url, "application/json", bytes.NewReader(data))
	if err != nil {
		slog.Warn("apprise notification failed", "component", "requests", "error", err)
		return
	}
	resp.Body.Close()
}
