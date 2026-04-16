package main

import (
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

	monitoredVolumesOverride = []monitoredVolume{
		{label: "Alpha", path: sub1, registered: true},
		{label: "Beta", path: sub2, registered: true},
	}
	t.Cleanup(func() { monitoredVolumesOverride = nil })

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
	monitoredVolumesOverride = []monitoredVolume{
		{label: "Temp", path: dir, registered: true},
	}
	t.Cleanup(func() { monitoredVolumesOverride = nil })

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

	monitoredVolumesOverride = []monitoredVolume{
		{label: "Test", path: dir, registered: true},
	}
	t.Cleanup(func() { monitoredVolumesOverride = nil })

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
	// Override settings to test threshold logic.
	db := testDB(t)
	SaveSettings(db, PipelineSettings{
		StorageWarningPct:  50,
		StorageCriticalPct: 90,
	})
	old := appDB
	appDB = db
	t.Cleanup(func() { appDB = old })

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
		s := GetSettings(appDB)
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
	if _, err := os.Stat(feedPath); err != nil {
		t.Fatalf("feed file not created: %v", err)
	}
	events := ReadFeed(dir)
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

func TestAppendToFeed_PrunesEventsOlderThan7Days(t *testing.T) {
	dir := t.TempDir()

	old := NotificationEvent{
		ID:        "old-event",
		Timestamp: time.Now().UTC().Add(-8 * 24 * time.Hour), // 8 days ago
		Type:      "content_ready",
		Message:   "old movie",
	}
	recent := NotificationEvent{
		ID:        "recent-event",
		Timestamp: time.Now().UTC().Add(-1 * time.Hour),
		Type:      "content_ready",
		Message:   "new movie",
	}

	appendToFeed(dir, old)
	appendToFeed(dir, recent)

	// ReadFeed prunes events older than 7 days and returns newest-first.
	events := ReadFeed(dir)
	for _, ev := range events {
		if ev.ID == "old-event" {
			t.Error("old event (8 days ago) should have been pruned")
		}
	}
	found := false
	for _, ev := range events {
		if ev.ID == "recent-event" {
			found = true
		}
	}
	if !found {
		t.Error("recent event should still be present")
	}
}

func TestBuildEvent_SetsJobIDAndDetail(t *testing.T) {
	job := &Job{
		ID: "abc12345",
		Source: JobSource{
			Title: "Dune Part Two",
			Year:  2024,
			Type:  "movie",
		},
	}

	// Failure event: detail and job_id should be set
	ev := buildEvent(job, "validation_failed", "Validation failed: Dune Part Two", "FFmpeg error: codec not supported")
	if ev.JobID != "abc12345" {
		t.Errorf("JobID = %q, want %q", ev.JobID, "abc12345")
	}
	if ev.Detail != "FFmpeg error: codec not supported" {
		t.Errorf("Detail = %q, want %q", ev.Detail, "FFmpeg error: codec not supported")
	}

	// content_ready: detail should be empty (don't leak error text for successful imports)
	ev2 := buildEvent(job, "content_ready", "Movie ready: Dune Part Two (2024)", "")
	if ev2.Detail != "" {
		t.Errorf("content_ready Detail = %q, want empty", ev2.Detail)
	}
	if ev2.JobID != "abc12345" {
		t.Errorf("content_ready JobID = %q, want %q", ev2.JobID, "abc12345")
	}
}
