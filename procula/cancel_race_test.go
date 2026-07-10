package procula

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestCancelRace_ActionJob verifies that cancelling a mid-flight action job
// leaves the job in StateCancelled (not re-queued, not failed).
//
// This guards against the race where:
//  1. Cancel() sets j.State = StateCancelled in the DB
//  2. Context is cancelled → handler returns context.Canceled
//  3. The post-handler guard previously checked
//     "if ctx.Err() != nil && (j.State == StateProcessing || j.State == StateQueued)"
//     which FAILED because j.State was already Cancelled
//  4. Error classification ran: context.Canceled is not permanent → classified
//     transient → job re-queued with backoff
func TestCancelRace_ActionJob(t *testing.T) {
	q := newTestQueue(t)

	// Gate channel: the handler blocks until we release it or its context is cancelled.
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})

	const actionName = "test_slow_action"
	Register(&ActionDef{
		Name:      actionName,
		Label:     "Slow test action",
		AppliesTo: []string{"movie"},
		Handler: func(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
			close(handlerStarted) // signal: handler is running
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-releaseHandler:
				return map[string]any{"done": true}, nil
			}
		},
	})
	t.Cleanup(func() {
		actionRegistryMu.Lock()
		delete(actionRegistry, actionName)
		actionRegistryMu.Unlock()
	})

	job, err := q.createActionJob(testSource("/downloads/slow.mkv"), actionName, nil)
	if err != nil {
		t.Fatalf("createActionJob: %v", err)
	}

	// Run the action job in a goroutine with a cancellable context.
	ctx, cancel := context.WithCancel(context.Background())
	q.registerCancel(job.ID, cancel)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runActionJob(ctx, q, job)
	}()

	// Wait for the handler to start, then cancel via the queue (simulates user cancel).
	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start within 2s")
	}

	if err := q.Cancel(job.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	// q.Cancel calls the registered cancel func which cancels ctx.

	wg.Wait()

	got, ok := q.Get(job.ID)
	if !ok {
		t.Fatal("job not found after cancel")
	}

	if got.State != StateCancelled {
		t.Errorf("State = %q, want %q — cancel race: job was re-queued or failed instead of cancelled",
			got.State, StateCancelled)
	}

	// The job must not appear in nextQueued (i.e. not re-queued with backoff).
	ids := q.nextQueued()
	for _, id := range ids {
		if id == job.ID {
			t.Errorf("cancelled job appeared in nextQueued — backoff was incorrectly applied")
		}
	}

	// NextAttemptAt must be nil (not a future backoff time).
	if got.NextAttemptAt != nil {
		t.Errorf("NextAttemptAt = %v, want nil — cancelled job should not have a backoff timestamp",
			got.NextAttemptAt)
	}
}

// TestCancelRace_ManualTranscode verifies that cancelling a mid-flight manual
// transcode job (ManualProfile set) leaves the job in StateCancelled — not
// StateFailed with a cryptic "FFmpeg exited with error: signal: killed" — and
// that no "Transcode failed" notification is written to the dashboard feed.
//
// This is the PRO-2 regression guard: runActionJob got this cancel-vs-failure
// guard long ago (see TestCancelRace_ActionJob) but the structurally identical
// runManualTranscode path never did.
func TestCancelRace_ManualTranscode(t *testing.T) {
	cfgDir := t.TempDir()

	profileDir := filepath.Join(cfgDir, "procula", "profiles")
	os.MkdirAll(profileDir, 0755)
	os.WriteFile(filepath.Join(profileDir, "hevc.json"), []byte(`{
		"name":"hevc-compat","enabled":true,
		"conditions":{"codecs_include":["hevc"]},
		"output":{"video_codec":"libx264","audio_codec":"aac","suffix":" - H264"}
	}`), 0644)

	// Fake ffmpeg in "sleep" mode: runs for ~1s, giving the test a wide
	// window to cancel mid-flight. Whichever way the race lands (cancel
	// before FFmpeg starts, SIGKILL mid-run, or even FFmpeg exiting first),
	// the job must end up Cancelled because the cancel commits StateCancelled
	// and cancels the registered context.
	setupFakeFFmpeg(t)
	t.Setenv("GO_TEST_FFMPEG", "sleep")

	sourceDir := t.TempDir()
	sourcePath := filepath.Join(sourceDir, "movie.hevc.mkv")
	os.WriteFile(sourcePath, []byte("fake"), 0644)

	// Shared DB for both the queue and appDB so the notification assert below
	// reads the same notifications table appendToFeed writes to.
	db := testDB(t)
	oldDB := appDB
	appDB = db
	t.Cleanup(func() { appDB = oldDB })
	if err := SaveSettings(db, PipelineSettings{CatalogEnabled: false}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	q, err := NewQueue(db)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}

	created, err := q.Create(testSource(sourcePath))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := q.Update(created.ID, func(j *Job) { j.ManualProfile = "hevc-compat" }); err != nil {
		t.Fatalf("Update (set ManualProfile): %v", err)
	}

	api := fakePeliculaAPI(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		processJob(q, created.ID, cfgDir, api)
	}()

	// Wait until the job is actually mid-flight (runManualTranscode marks it
	// Processing at entry, after the cancel func is registered), then cancel.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if job, ok := q.Get(created.ID); ok && job.State == StateProcessing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job never reached StateProcessing")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := q.Cancel(created.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("processJob did not return within 5s after cancel")
	}

	got, ok := q.Get(created.ID)
	if !ok {
		t.Fatal("job not found after cancel")
	}
	if got.State != StateCancelled {
		t.Errorf("State = %q, want %q — cancelled manual transcode must not be classified as failed (TranscodeError: %q)",
			got.State, StateCancelled, got.TranscodeError)
	}
	if got.TranscodeDecision == "failed" {
		t.Errorf("TranscodeDecision = %q — cancellation must not be recorded as a transcode failure", got.TranscodeDecision)
	}

	// No misleading "Transcode failed" notification for a deliberate cancel.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE type='transcode_failed'`).Scan(&n); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if n != 0 {
		t.Errorf("found %d transcode_failed notification(s) after cancel, want 0", n)
	}
}
