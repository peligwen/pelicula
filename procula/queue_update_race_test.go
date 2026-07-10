package procula

import (
	"sync"
	"testing"
	"time"
)

// TestUpdate_SerializesPerJobID proves mutual exclusion directly: while one
// Update call is parked mid-critical-section (via updateSyncHook, after its
// read but before its write), a second concurrent Update for the *same* job
// ID must not be able to complete first. This is a deterministic regression
// test for PRO-1 — before the per-job mutex, Update had no critical section
// at all, so this would fail every run (not just occasionally) on the old
// code: the second call's Get/fn/write would race freely against the first.
func TestUpdate_SerializesPerJobID(t *testing.T) {
	q := newTestQueue(t)
	job, err := q.Create(testSource("/downloads/serialize.mkv"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := q.Update(job.ID, func(j *Job) { j.State = StateProcessing }); err != nil {
		t.Fatalf("Update (seed processing): %v", err)
	}

	// One-shot hook: only the FIRST Update to enter the critical section
	// blocks. The second Update (the Cancel) also runs the hook once it
	// acquires the mutex — by then the sync.Once has fired, so it passes
	// straight through.
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	updateSyncHook = func(id string) {
		if id != job.ID {
			return
		}
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	t.Cleanup(func() { updateSyncHook = nil })

	// First Update: simulates the worker's progress callback. It will block
	// inside the hook, holding the per-job mutex, until we release it.
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_ = q.Update(job.ID, func(j *Job) { j.Progress = 0.9 })
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first Update never reached the sync hook")
	}

	// Second Update: simulates a concurrent HTTP cancel. Must not be able to
	// read/write the row until the first Update's critical section releases.
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		_ = q.Cancel(job.ID)
	}()

	select {
	case <-secondDone:
		t.Fatal("Cancel's Update completed while another Update was mid-flight for the same job — no serialization")
	case <-time.After(150 * time.Millisecond):
		// expected: still blocked on the per-job mutex
	}

	close(release)

	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first Update did not complete after release")
	}
	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel did not complete after first Update released the mutex")
	}

	got, ok := q.Get(job.ID)
	if !ok {
		t.Fatal("job not found")
	}
	if got.State != StateCancelled {
		t.Errorf("State = %q, want %q — the earlier progress write must not have reverted the later cancel, and the cancel (running second, serialized) must win",
			got.State, StateCancelled)
	}
}

// TestConcurrentUpdate_CancelNeverReverted is the stress-test regression guard
// PRO-1 calls for: N goroutines hammer progress-style Updates (which never
// touch State) on one job while a single goroutine cancels it, all racing
// freely with no external synchronization. With Update's read-modify-write
// serialized per job ID, this is deterministically correct regardless of
// scheduling order — the job's final State must be StateCancelled every time,
// never reverted back to StateProcessing by a stale progress write.
func TestConcurrentUpdate_CancelNeverReverted(t *testing.T) {
	q := newTestQueue(t)
	job, err := q.Create(testSource("/downloads/race.mkv"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := q.Update(job.ID, func(j *Job) { j.State = StateProcessing }); err != nil {
		t.Fatalf("Update (seed processing): %v", err)
	}

	const n = 50
	var wg sync.WaitGroup
	start := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_ = q.Cancel(job.ID)
	}()

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			// Mirrors the real progress callback in pipeline.go: it only ever
			// touches Progress/TranscodeETA, never State.
			_ = q.Update(job.ID, func(j *Job) {
				j.Progress = float64(i) / float64(n)
			})
		}(i)
	}

	close(start)
	wg.Wait()

	got, ok := q.Get(job.ID)
	if !ok {
		t.Fatal("job not found after concurrent updates")
	}
	if got.State != StateCancelled {
		t.Errorf("State = %q, want %q — a concurrent progress update reverted the cancellation (PRO-1 lost-update race)",
			got.State, StateCancelled)
	}
}
