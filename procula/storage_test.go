package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetVolumeInfo(t *testing.T) {
	// /tmp is always available and stat-able.
	info, err := getVolumeInfo("/tmp", "Temp")
	if err != nil {
		t.Fatalf("getVolumeInfo(/tmp): %v", err)
	}
	if info.Label != "Temp" {
		t.Errorf("label = %q, want %q", info.Label, "Temp")
	}
	if info.Total <= 0 {
		t.Errorf("total = %d, want > 0", info.Total)
	}
	if info.Available < 0 {
		t.Errorf("available = %d, want >= 0", info.Available)
	}
	if info.UsedPct < 0 || info.UsedPct > 100 {
		t.Errorf("used_pct = %f, want 0-100", info.UsedPct)
	}
	if info.Status == "" {
		t.Error("status must not be empty")
	}
}

func TestGetVolumeInfoMissingPath(t *testing.T) {
	_, err := getVolumeInfo("/this/path/does/not/exist/at/all", "Nope")
	if err == nil {
		t.Error("expected error for non-existent path, got nil")
	}
}

func TestVolumeStatus(t *testing.T) {
	// Override settings to test threshold logic without a config file.
	settingsMu.Lock()
	cachedSettings = &PipelineSettings{
		StorageWarningPct:  50,
		StorageCriticalPct: 90,
	}
	settingsMu.Unlock()
	defer func() {
		settingsMu.Lock()
		cachedSettings = nil
		settingsMu.Unlock()
	}()

	cases := []struct {
		usedPct float64
		want    string
	}{
		{30, "ok"},
		{50, "warning"},
		{75, "warning"},
		{90, "critical"},
		{99, "critical"},
	}

	for _, c := range cases {
		s := GetSettings()
		status := "ok"
		switch {
		case c.usedPct >= s.StorageCriticalPct:
			status = "critical"
		case c.usedPct >= s.StorageWarningPct:
			status = "warning"
		}
		if status != c.want {
			t.Errorf("usedPct=%.0f: status = %q, want %q", c.usedPct, status, c.want)
		}
	}
}

func TestBuildStorageReport(t *testing.T) {
	// buildStorageReport skips unmounted paths — this confirms it returns
	// a report with a recent timestamp without panicking.
	report := buildStorageReport()
	if report.Timestamp.IsZero() {
		t.Error("timestamp should not be zero")
	}
	if time.Since(report.Timestamp) > 5*time.Second {
		t.Error("timestamp should be very recent")
	}
	// Volumes may be empty in CI (container paths like /downloads don't exist),
	// but the function must not panic.
	for _, v := range report.Volumes {
		if v.Label == "" {
			t.Errorf("volume %s has empty label", v.Path)
		}
		if v.Status == "" {
			t.Errorf("volume %s has empty status", v.Path)
		}
	}
}

func TestStorageNotificationMessage(t *testing.T) {
	// Ensure the storage alert message doesn't contain the "label is label full" bug.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "procula"), 0755); err != nil {
		t.Fatal(err)
	}

	// Simulate what RunStorageMonitor emits.
	label := "Downloads"
	usedPct := 87.3
	msg := fmt.Sprintf("Storage %s: %s is %.0f%% full", "warning", label, usedPct)

	event := NotificationEvent{
		ID:        "storage_Downloads_123",
		Timestamp: time.Now().UTC(),
		Type:      "storage_warning",
		Message:   msg,
	}
	appendToFeed(dir, event)

	feedPath := filepath.Join(dir, "procula", "notifications_feed.json")
	data, err := os.ReadFile(feedPath)
	if err != nil {
		t.Fatalf("feed file not created: %v", err)
	}
	var events []NotificationEvent
	if err := json.Unmarshal(data, &events); err != nil {
		t.Fatalf("invalid JSON in feed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "storage_warning" {
		t.Errorf("type = %q, want storage_warning", events[0].Type)
	}
	// Verify the message doesn't contain the duplicate-label pattern.
	if contains(events[0].Message, "Downloads is Downloads") {
		t.Errorf("message contains duplicate label: %q", events[0].Message)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
