// Package apprise provides a client for the Apprise notification container.
// It reads notification configuration from the procula notifications.json
// file and forwards notifications when mode is "apprise".
package apprise

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"pelicula-api/internal/httpx"
)

type notifConfig struct {
	Mode        string   `json:"mode"`
	AppriseURLs []string `json:"apprise_urls"`
}

// Client is a client for the Apprise notification container.
type Client struct {
	url       string
	configDir string
	base      *httpx.Client
}

// New constructs a Client.
// url is the Apprise notify endpoint (e.g. cfg.URLs.Apprise).
// configDir is the root config directory (e.g. cfg.ConfigDir); the client
// reads <configDir>/procula/notifications.json to determine the active mode.
func New(url, configDir string) *Client {
	return &Client{
		url:       url,
		configDir: configDir,
		base:      httpx.New(url, "", "", 10*time.Second),
	}
}

// configPath returns the path to the notifications config file.
// It is a package-level variable so tests can override it.
var configPath = func(configDir string) string {
	return filepath.Join(configDir, "procula", "notifications.json")
}

func (c *Client) loadConfig() *notifConfig {
	data, err := os.ReadFile(configPath(c.configDir))
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
// Each configured URL receives its own POST so one bad URL does not abort the batch.
// Non-fatal: logs on error and returns.
func (c *Client) Notify(title, body string) {
	cfg := c.loadConfig()
	if cfg.Mode != "apprise" || len(cfg.AppriseURLs) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var errs []error
	for _, u := range cfg.AppriseURLs {
		payload := map[string]any{
			"title": title,
			"body":  body,
			"type":  "info",
			"urls":  u,
		}
		if err := c.base.PostJSON(ctx, "", payload, nil); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		slog.Warn("apprise notification failed", "component", "requests", "error", err)
	}
}
