package procula

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v1.2.0", "v1.1.0", true},
		{"v1.1.0", "v1.1.0", false}, // same version
		{"v1.0.0", "v1.1.0", false}, // older
		{"", "v1.0.0", false},       // no latest (no release yet)
		{"v1.0.0", "dev", false},    // dev build — never show update
		{"v2.0.0", "v1.9.9", true},
		{"v1.10.0", "v1.9.0", true},  // multi-digit minor version
		{"v1.9.0", "v1.10.0", false}, // older multi-digit
		{"v1.0.1", "v1.0.0", true},   // patch bump
	}
	for _, c := range cases {
		got := isNewerVersion(c.latest, c.current)
		if got != c.want {
			t.Errorf("isNewerVersion(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestParseSemver(t *testing.T) {
	cases := []struct {
		input string
		want  [3]int
	}{
		{"1.2.3", [3]int{1, 2, 3}},
		{"1.10.0", [3]int{1, 10, 0}},
		{"0.0.1", [3]int{0, 0, 1}},
		{"1", [3]int{1, 0, 0}},
		{"", [3]int{0, 0, 0}},
		{"abc", [3]int{0, 0, 0}},
	}
	for _, c := range cases {
		got := parseSemver(c.input)
		if got != c.want {
			t.Errorf("parseSemver(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

func TestGetCachedUpdateDefault(t *testing.T) {
	// Reset global cache to ensure test isolation.
	updateMu.Lock()
	orig := cachedUpdate
	cachedUpdate = nil
	updateMu.Unlock()
	defer func() {
		updateMu.Lock()
		cachedUpdate = orig
		updateMu.Unlock()
	}()

	info := getCachedUpdate()
	if info.CurrentVersion != Version {
		t.Errorf("current_version = %q, want %q", info.CurrentVersion, Version)
	}
	if info.UpdateAvail {
		t.Error("update_available should be false when no check has run")
	}
}

func TestUpdateCachePersistence(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "procula"), 0755); err != nil {
		t.Fatal(err)
	}

	// Simulate a cached update file written by RunUpdateChecker.
	expected := UpdateInfo{
		CurrentVersion: "v1.0.0",
		LatestVersion:  "v1.1.0",
		UpdateAvail:    true,
		CheckedAt:      time.Now().UTC().Truncate(time.Second),
	}
	data, _ := json.MarshalIndent(expected, "", "  ")
	cachePath := filepath.Join(dir, "procula", "update_check.json")
	if err := os.WriteFile(cachePath, data, 0644); err != nil {
		t.Fatal(err)
	}

	// Loading should round-trip correctly.
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	var loaded UpdateInfo
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if loaded.LatestVersion != expected.LatestVersion {
		t.Errorf("latest_version = %q, want %q", loaded.LatestVersion, expected.LatestVersion)
	}
	if !loaded.UpdateAvail {
		t.Error("update_available should be true")
	}
}

func TestUpdateCacheThreadSafety(t *testing.T) {
	// Reset global cache to ensure a writer goroutine must populate it.
	updateMu.Lock()
	cachedUpdate = nil
	updateMu.Unlock()

	// Confirm getCachedUpdate and writing to cachedUpdate don't race.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = getCachedUpdate()
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			updateMu.Lock()
			u := UpdateInfo{CurrentVersion: "v1.0.0", CheckedAt: time.Now()}
			cachedUpdate = &u
			updateMu.Unlock()
		}()
	}
	wg.Wait()

	updateMu.RLock()
	final := cachedUpdate
	updateMu.RUnlock()
	if final == nil {
		t.Fatal("cachedUpdate should be non-nil after concurrent writes")
	}
}

// TestRunUpdateChecker_StopsOnCtxCancelDuringSleep verifies that the ctx-aware
// select replacing the bare time.Sleep(30s) returns promptly on cancellation.
// The test forces the sleep branch by resetting cachedUpdate to nil and using a
// configDir with no on-disk cache file.
func TestRunUpdateChecker_StopsOnCtxCancelDuringSleep(t *testing.T) {
	// Reset in-memory cache so the "cached == nil || stale" branch is taken.
	updateMu.Lock()
	orig := cachedUpdate
	cachedUpdate = nil
	updateMu.Unlock()
	t.Cleanup(func() {
		updateMu.Lock()
		cachedUpdate = orig
		updateMu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Empty dir → no on-disk cache file; cachedUpdate stays nil → sleep branch.
		RunUpdateChecker(ctx, t.TempDir())
	}()

	// Cancel while the goroutine is sleeping in the ctx-aware select.
	cancel()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("RunUpdateChecker did not exit within 1s after ctx cancel during sleep")
	}
}
