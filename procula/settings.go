package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// PipelineSettings controls which pipeline stages run and how notifications
// are sent. Persisted to /config/procula/settings.json.
type PipelineSettings struct {
	ValidationEnabled    bool     `json:"validation_enabled"`
	DeleteOnFailure      bool     `json:"delete_on_failure"`       // delete file when validation fails (default: false)
	TranscodingEnabled   bool     `json:"transcoding_enabled"`
	CatalogEnabled       bool     `json:"catalog_enabled"`
	NotifMode            string   `json:"notification_mode"`       // "internal", "apprise", "direct"
	AppriseURLs          []string `json:"apprise_urls,omitempty"`
	DirectURL            string   `json:"direct_url,omitempty"`
	StorageWarningPct    float64  `json:"storage_warning_pct"`     // emit warning notification above this % (default: 85)
	StorageCriticalPct   float64  `json:"storage_critical_pct"`    // emit critical notification above this % (default: 95)
}

var (
	settingsMu     sync.RWMutex
	cachedSettings *PipelineSettings
)

// GetSettings returns current settings, using the on-disk file when present
// and falling back to environment-variable defaults otherwise.
func GetSettings() PipelineSettings {
	settingsMu.RLock()
	if cachedSettings != nil {
		s := *cachedSettings
		settingsMu.RUnlock()
		return s
	}
	settingsMu.RUnlock()
	return reloadSettings()
}

func reloadSettings() PipelineSettings {
	s := defaultSettings()
	path := filepath.Join(configDir, "procula", "settings.json")
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &s) //nolint:errcheck
	}
	settingsMu.Lock()
	cachedSettings = &s
	settingsMu.Unlock()
	return s
}

func defaultSettings() PipelineSettings {
	return PipelineSettings{
		ValidationEnabled:  true,
		TranscodingEnabled: os.Getenv("TRANSCODING_ENABLED") == "true",
		CatalogEnabled:     true,
		NotifMode:          "internal",
		StorageWarningPct:  85,
		StorageCriticalPct: 95,
	}
}

// SaveSettings persists settings to disk and updates the in-memory cache.
func SaveSettings(s PipelineSettings) error {
	path := filepath.Join(configDir, "procula", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}
	settingsMu.Lock()
	cachedSettings = &s
	settingsMu.Unlock()
	return nil
}
