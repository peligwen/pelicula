package main

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

type appriseNotifConfig struct {
	Mode        string   `json:"mode"`
	AppriseURLs []string `json:"apprise_urls"`
}

func loadAppriseConfig() *appriseNotifConfig {
	configDir := os.Getenv("CONFIG_DIR")
	if configDir == "" {
		configDir = "/config"
	}
	data, err := os.ReadFile(filepath.Join(configDir, "procula", "notifications.json"))
	if err != nil {
		return &appriseNotifConfig{Mode: "internal"}
	}
	var cfg appriseNotifConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &appriseNotifConfig{Mode: "internal"}
	}
	return &cfg
}

// notifyApprise sends a notification via the Apprise container if configured.
// Non-fatal: logs on error and returns.
func notifyApprise(title, body string) {
	cfg := loadAppriseConfig()
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
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post("http://apprise:8000/notify", "application/json", bytes.NewReader(data))
	if err != nil {
		slog.Warn("apprise notification failed", "component", "requests", "error", err)
		return
	}
	resp.Body.Close()
}
