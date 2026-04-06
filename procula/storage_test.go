package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFsIDForPath(t *testing.T) {
	id, err := fsIDForPath("/tmp")
	if err != nil {
		t.Fatalf("fsIDForPath /tmp: %v", err)
	}
	if id == "" {
		t.Error("fsID must not be empty")
	}
	// Calling again must return the same value (deterministic).
	id2, _ := fsIDForPath("/tmp")
	if id != id2 {
		t.Errorf("fsIDForPath not deterministic: %q != %q", id, id2)
	}
	// Two subdirs of /tmp share the same device.
	id3, err := fsIDForPath(t.TempDir())
	if err != nil {
		t.Fatalf("fsIDForPath tempdir: %v", err)
	}
	if id != id3 {
		t.Errorf("expected same fsID for /tmp and tempdir, got %q vs %q", id, id3)
	}
}

func TestBuildStorageReport_GroupsByFilesystem(t *testing.T) {
	// Two subdirectories of /tmp share the same filesystem.
	dir := t.TempDir()
	sub1 := filepath.Join(dir, "a")
	sub2 := filepath.Join(dir, "b")
	if err := os.MkdirAll(sub1, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub2, 0755); err != nil {
		t.Fatal(err)
	}

	orig := monitoredVolumes
	monitoredVolumes = []struct {
		label string
		path  string
	}{
		{"Alpha", sub1},
		{"Beta", sub2},
	}
	defer func() { monitoredVolumes = orig }()

	report := buildStorageReport()

	if report.Timestamp.IsZero() {
		t.Error("timestamp should not be zero")
	}
	if len(report.Filesystems) != 1 {
		t.Fatalf("expected 1 filesystem (shared), got %d", len(report.Filesystems))
	}
	fsi := report.Filesystems[0]
	if len(fsi.Folders) != 2 {
		t.Fatalf("expected 2 folders, got %d", len(fsi.Folders))
	}
	labels := map[string]bool{}
	for _, f := range fsi.Folders {
		labels[f.Label] = true
	}
	if !labels["Alpha"] || !labels["Beta"] {
		t.Errorf("unexpected folder labels: %v", labels)
	}
	if fsi.Total <= 0 {
		t.Error("total must be > 0")
	}
}

func TestBuildStorageReport_Timestamp(t *testing.T) {
	dir := t.TempDir()
	orig := monitoredVolumes
	monitoredVolumes = []struct {
		label string
		path  string
	}{
		{"Temp", dir},
	}
	defer func() { monitoredVolumes = orig }()

	report := buildStorageReport()
	if report.Timestamp.IsZero() {
		t.Error("timestamp should not be zero")
	}
	if time.Since(report.Timestamp) > 5*time.Second {
		t.Error("timestamp should be very recent")
	}
}

func TestComputeFolderSizes(t *testing.T) {
	dir := t.TempDir()
	// Write two files with known sizes.
	file1 := filepath.Join(dir, "a.bin")
	file2 := filepath.Join(dir, "b.bin")
	if err := os.WriteFile(file1, make([]byte, 1024), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file2, make([]byte, 2048), 0644); err != nil {
		t.Fatal(err)
	}

	orig := monitoredVolumes
	monitoredVolumes = []struct {
		label string
		path  string
	}{
		{"Test", dir},
	}
	defer func() { monitoredVolumes = orig }()

	// Reset cache first.
	folderSizeMu.Lock()
	folderSizes = map[string]int64{}
	folderSizeMu.Unlock()

	computeFolderSizes()

	sizes := getCachedFolderSizes()
	got, ok := sizes[dir]
	if !ok {
		t.Fatalf("no size cached for %s", dir)
	}
	// Expect at least 3072 bytes (1024 + 2048); OS may round up.
	if got < 3072 {
		t.Errorf("folder size = %d, want >= 3072", got)
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

func TestStorageNotificationMessage(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "procula"), 0755); err != nil {
		t.Fatal(err)
	}

	// Simulate the alert message format from RunStorageMonitor.
	labels := []string{"Downloads", "Movies"}
	usedPct := 87.3
	msg := fmt.Sprintf("Storage %s: disk (%s) is %.0f%% full",
		"warning", "Downloads, Movies", usedPct)

	event := NotificationEvent{
		ID:        "storage_abc123_1",
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
	// Message must include all folder labels, not duplicate them.
	for _, label := range labels {
		if !strings.Contains(events[0].Message, label) {
			t.Errorf("message missing label %q: %q", label, events[0].Message)
		}
	}
	if strings.Contains(events[0].Message, "Downloads is Downloads") {
		t.Errorf("message contains duplicate label pattern: %q", events[0].Message)
	}
}

