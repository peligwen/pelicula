package procula

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// restorePollInterval resets awaitPollInterval after a test.
func fastPollInterval(t *testing.T) {
	t.Helper()
	orig := awaitPollInterval
	awaitPollInterval = 50 * time.Millisecond
	t.Cleanup(func() { awaitPollInterval = orig })
}

// TestAwaitSubtitles_NoMissingSubs — returns immediately when MissingSubs is nil.
func TestAwaitSubtitles_NoMissingSubs(t *testing.T) {
	fastPollInterval(t)
	q := newTestQueue(t)
	job, err := q.Create(testSource("/movies/test.mkv"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// MissingSubs is nil by default

	done := make(chan struct{})
	go func() {
		awaitSubtitles(context.Background(), q, job, PipelineSettings{SubAcquireTimeout: 1}, "")
		close(done)
	}()

	select {
	case <-done:
		// expected — returned immediately
	case <-time.After(2 * time.Second):
		t.Fatal("awaitSubtitles did not return promptly with no missing subs")
	}
}

// TestAwaitSubtitles_AlreadyPresent — sidecar already exists; function returns quickly with SubsAcquired populated.
func TestAwaitSubtitles_AlreadyPresent(t *testing.T) {
	fastPollInterval(t)

	// Create a temp dir to hold the fake media and sidecar
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Movie.mkv")
	sidecarPath := filepath.Join(dir, "Movie.es.srt")

	// Write the sidecar file so findSubSidecar will find it
	if err := os.WriteFile(sidecarPath, []byte("1\n00:00:01,000 --> 00:00:02,000\nHola\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	q := newTestQueue(t)
	src := testSource(mediaPath)
	job, err := q.Create(src)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Set MissingSubs on the job
	if err := q.Update(job.ID, func(j *Job) {
		j.MissingSubs = []string{"es"}
	}); err != nil {
		t.Fatalf("Update MissingSubs: %v", err)
	}
	job, _ = q.Get(job.ID)

	// Swap out bazarrURL so no real HTTP call is made
	orig := bazarrURL
	bazarrURL = "http://127.0.0.1:0" // will fail silently — fire-and-forget
	t.Cleanup(func() { bazarrURL = orig })

	done := make(chan struct{})
	go func() {
		awaitSubtitles(context.Background(), q, job, PipelineSettings{SubAcquireTimeout: 1}, t.TempDir())
		close(done)
	}()

	select {
	case <-done:
		// expected
	case <-time.After(3 * time.Second):
		t.Fatal("awaitSubtitles did not return after sidecar present")
	}

	// Verify DB state
	updated, ok := q.Get(job.ID)
	if !ok {
		t.Fatal("job not found after awaitSubtitles")
	}
	if len(updated.MissingSubs) != 0 {
		t.Errorf("MissingSubs = %v, want empty", updated.MissingSubs)
	}
	if len(updated.SubsAcquired) != 1 || updated.SubsAcquired[0] != "es" {
		t.Errorf("SubsAcquired = %v, want [es]", updated.SubsAcquired)
	}
}

// TestAwaitSubtitles_Cancelled — context cancelled before any sidecar appears; returns promptly.
func TestAwaitSubtitles_Cancelled(t *testing.T) {
	fastPollInterval(t)

	q := newTestQueue(t)
	job, err := q.Create(testSource("/movies/no-subs.mkv"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := q.Update(job.ID, func(j *Job) {
		j.MissingSubs = []string{"en", "es"}
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	job, _ = q.Get(job.ID)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	orig := bazarrURL
	bazarrURL = "http://127.0.0.1:0"
	t.Cleanup(func() { bazarrURL = orig })

	done := make(chan struct{})
	go func() {
		awaitSubtitles(ctx, q, job, PipelineSettings{SubAcquireTimeout: 30}, t.TempDir())
		close(done)
	}()

	select {
	case <-done:
		// expected — cancelled context caused early return
	case <-time.After(3 * time.Second):
		t.Fatal("awaitSubtitles did not return after context cancellation")
	}

	// SubsAcquired should remain empty (no sidecars written)
	updated, _ := q.Get(job.ID)
	if len(updated.SubsAcquired) != 0 {
		t.Errorf("SubsAcquired = %v, want empty after cancellation", updated.SubsAcquired)
	}
}

// TestAwaitSubtitles_Cancelled_Context — context deadline expires before the internal deadline;
// function returns via the ctx.Done() path (cancellation, not EventSubTimeout).
func TestAwaitSubtitles_Cancelled_Context(t *testing.T) {
	orig := awaitPollInterval
	awaitPollInterval = 50 * time.Millisecond
	t.Cleanup(func() { awaitPollInterval = orig })

	q := newTestQueue(t)
	job, err := q.Create(testSource("/movies/timeout.mkv"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := q.Update(job.ID, func(j *Job) {
		j.MissingSubs = []string{"fr"}
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	job, _ = q.Get(job.ID)

	origURL := bazarrURL
	bazarrURL = "http://127.0.0.1:0"
	t.Cleanup(func() { bazarrURL = origURL })

	// SubAcquireTimeout=1 (1 min) is the internal deadline, but ctx expires in
	// 200ms — this exercises the ctx.Done() path, not EventSubTimeout.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		awaitSubtitles(ctx, q, job, PipelineSettings{SubAcquireTimeout: 1}, t.TempDir())
		close(done)
	}()

	select {
	case <-done:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("awaitSubtitles did not return after context timeout")
	}

	// Nothing should have been acquired
	updated, _ := q.Get(job.ID)
	if len(updated.SubsAcquired) != 0 {
		t.Errorf("SubsAcquired = %v, want empty on timeout", updated.SubsAcquired)
	}
}

// TestAwaitSubsSkipsFlaggedJob — bazarr trigger must not fire when the job already
// carries an error-severity flag (automation is paused for flagged items).
func TestAwaitSubsSkipsFlaggedJob(t *testing.T) {
	called := false
	origTrigger := bazarrTrigger
	bazarrTrigger = func(ctx context.Context, cfgDir string, j *Job) { called = true }
	t.Cleanup(func() { bazarrTrigger = origTrigger })

	job := &Job{
		Source:      JobSource{Path: "/movies/X/X.mkv", ArrType: "radarr", ArrID: 1},
		MissingSubs: []string{"es"},
		Flags:       []Flag{{Code: "validation_failed", Severity: FlagSeverityError}},
	}
	awaitSubtitles(context.Background(), nil, job, PipelineSettings{SubAcquireTimeout: 0}, t.TempDir())
	if called {
		t.Fatalf("bazarr trigger should be skipped when job is flagged error")
	}
}

// TestAwaitSubtitles_Timeout — exercises the internal deadline path (EventSubTimeout emission).
// Overrides nowFn so the deadline appears already expired on the first check,
// without any context cancellation pressure.
func TestAwaitSubtitles_Timeout(t *testing.T) {
	fastPollInterval(t)

	oldNow := nowFn
	nowFn = func() time.Time { return time.Now().Add(2 * time.Hour) }
	t.Cleanup(func() { nowFn = oldNow })

	q := newTestQueue(t)
	job, err := q.Create(testSource("/movies/timeout-deadline.mkv"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := q.Update(job.ID, func(j *Job) {
		j.MissingSubs = []string{"es"}
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	job, _ = q.Get(job.ID)

	origURL := bazarrURL
	bazarrURL = "http://127.0.0.1:0"
	t.Cleanup(func() { bazarrURL = origURL })

	done := make(chan struct{})
	go func() {
		awaitSubtitles(context.Background(), q, job, PipelineSettings{SubAcquireTimeout: 1}, t.TempDir())
		close(done)
	}()

	select {
	case <-done:
		// expected — deadline expired immediately, returned via timeout path
	case <-time.After(2 * time.Second):
		t.Fatal("awaitSubtitles did not return promptly after deadline")
	}

	updated, _ := q.Get(job.ID)
	if len(updated.SubsAcquired) != 0 {
		t.Errorf("SubsAcquired = %v, want empty", updated.SubsAcquired)
	}
	// MissingSubs should still contain "es" (timed out, never acquired)
	if len(updated.MissingSubs) == 0 {
		t.Error("expected MissingSubs to still contain 'es' after timeout")
	}
}
