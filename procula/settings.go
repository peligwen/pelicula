package main

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
)

// PipelineSettings controls which pipeline stages run and how notifications
// are sent. Persisted to the settings table in SQLite under key "pipeline".
type PipelineSettings struct {
	ValidationEnabled  bool     `json:"validation_enabled"`
	DeleteOnFailure    bool     `json:"delete_on_failure"`    // delete file when validation fails (default: false)
	DualSubEnabled     bool     `json:"dualsub_enabled"`      // generate stacked dual-language ASS sidecar files
	DualSubPairs       []string `json:"dualsub_pairs"`        // e.g. ["en-es","en-de"]; first lang=bottom, second=top
	DualSubTranslator  string   `json:"dualsub_translator"`   // "argos" or "none"
	TranscodingEnabled bool     `json:"transcoding_enabled"`
	CatalogEnabled     bool     `json:"catalog_enabled"`
	NotifMode          string   `json:"notification_mode"` // "internal", "apprise", "direct"
	AppriseURLs        []string `json:"apprise_urls,omitempty"`
	DirectURL          string   `json:"direct_url,omitempty"`
	StorageWarningPct  float64  `json:"storage_warning_pct"`  // emit warning notification above this % (default: 85)
	StorageCriticalPct float64  `json:"storage_critical_pct"` // emit critical notification above this % (default: 95)
}

// GetSettings returns the pipeline settings from the DB, falling back to defaults.
func GetSettings(db *sql.DB) PipelineSettings {
	if db == nil {
		return defaultSettings()
	}
	var value string
	err := db.QueryRow(`SELECT value FROM settings WHERE key='pipeline'`).Scan(&value)
	if err != nil {
		if err != sql.ErrNoRows {
			slog.Warn("GetSettings query failed", "component", "settings", "error", err)
		}
		return defaultSettings()
	}
	s := defaultSettings()
	if jsonErr := json.Unmarshal([]byte(value), &s); jsonErr != nil {
		slog.Warn("GetSettings unmarshal failed", "component", "settings", "error", jsonErr)
		return defaultSettings()
	}
	return s
}

// SaveSettings persists pipeline settings to the DB.
func SaveSettings(db *sql.DB, s PipelineSettings) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`INSERT INTO settings (key, value) VALUES ('pipeline', ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		string(data),
	)
	return err
}

func defaultSettings() PipelineSettings {
	pairs := []string{"en-es"}
	if raw := os.Getenv("DUALSUB_PAIRS"); raw != "" {
		pairs = splitTrimmed(raw, ",")
	}
	translator := "none"
	if t := os.Getenv("DUALSUB_TRANSLATOR"); t == "argos" {
		translator = "argos"
	}
	return PipelineSettings{
		ValidationEnabled:  true,
		DualSubEnabled:     os.Getenv("DUALSUB_ENABLED") == "true",
		DualSubPairs:       pairs,
		DualSubTranslator:  translator,
		TranscodingEnabled: os.Getenv("TRANSCODING_ENABLED") == "true",
		CatalogEnabled:     true,
		NotifMode:          "internal",
		StorageWarningPct:  85,
		StorageCriticalPct: 95,
	}
}

func splitTrimmed(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}
