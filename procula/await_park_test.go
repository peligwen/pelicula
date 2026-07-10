package procula

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// swapAwaitSlots replaces the package await-park semaphore with a fresh
// channel of the given capacity for the duration of the test. Returns the
// test-local channel so the test can join the parked continuation (see
// joinParkedContinuation) before the var is restored.
func swapAwaitSlots(t *testing.T, capacity int) chan struct{} {
	t.Helper()
	orig := awaitSlots
	ch := make(chan struct{}, capacity)
	awaitSlots = ch
	t.Cleanup(func() { awaitSlots = orig })
	return ch
}

// joinParkedContinuation blocks until the single parked continuation using a
// capacity-1 slots channel has fully finished (its slot-release receive is the
// last thing it does). The buffered-channel send below can only complete after
// that receive, which also establishes the happens-before edge the race
// detector needs between the continuation's work and the test's cleanup.
func joinParkedContinuation(t *testing.T, slots chan struct{}) {
	t.Helper()
	select {
	case slots <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("parked continuation did not release its await slot within 5s")
	}
}

// stubBazarrTrigger replaces the Bazarr search kick with a no-op so parked
// runs never make HTTP calls in tests.
func stubBazarrTrigger(t *testing.T) {
	t.Helper()
	orig := bazarrTrigger
	bazarrTrigger = func(context.Context, string, *Job) {}
	t.Cleanup(func() { bazarrTrigger = orig })
}

// parkTestQueue builds a queue + appDB on one shared test DB with all
// pipeline stages disabled (the await/park behavior is what's under test).
func parkTestQueue(t *testing.T) *Queue {
	t.Helper()
	db := testDB(t)
	old := appDB
	appDB = db
	t.Cleanup(func() { appDB = old })
	if err := SaveSettings(db, PipelineSettings{SubAcquireTimeout: 1}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	q, err := NewQueue(db)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	return q
}

// TestAwaitSubs_ParksJobAndWorkerResumes is the core PRO-3 regression guard:
// a pipeline job with missing subtitles must NOT block processJob (the
// worker's hot path) while polling for Bazarr sidecars. processJob must
// return with the job parked (processing/await_subs); when the sidecar later
// appears, the background continuation must finish the remaining stages and
// complete the job on its own.
func TestAwaitSubs_ParksJobAndWorkerResumes(t *testing.T) {
	fastPollInterval(t)
	stubBazarrTrigger(t)
	slots := swapAwaitSlots(t, 1)

	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Movie.mkv")
	os.WriteFile(mediaPath, []byte("fake"), 0644)

	q := parkTestQueue(t)
	created, err := q.Create(testSource(mediaPath))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := q.Update(created.ID, func(j *Job) { j.MissingSubs = []string{"es"} }); err != nil {
		t.Fatalf("Update (set MissingSubs): %v", err)
	}

	api := fakePeliculaAPI(t)
	cfgDir := t.TempDir()

	// No sidecar exists yet — pre-PRO-3, this call would block for the whole
	// SubAcquireTimeout. Now it must return promptly with the job parked.
	done := make(chan struct{})
	go func() {
		defer close(done)
		processJob(q, created.ID, cfgDir, api)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("processJob did not return while subtitles were missing — job was not parked off the worker")
	}

	parked, ok := q.Get(created.ID)
	if !ok {
		t.Fatal("job not found after park")
	}
	if parked.State != StateProcessing || parked.Stage != StageAwaitSubs {
		t.Fatalf("after park: state/stage = %q/%q, want %q/%q",
			parked.State, parked.Stage, StateProcessing, StageAwaitSubs)
	}

	// Deliver the sidecar; the background continuation should acquire it and
	// run the remaining stages through to completion.
	sidecar := filepath.Join(dir, "Movie.es.srt")
	os.WriteFile(sidecar, []byte("1\n00:00:01,000 --> 00:00:02,000\nHola\n"), 0644)

	final, err := q.Wait(created.ID, 5*time.Second)
	if err != nil {
		t.Fatalf("Wait: %v (state=%q stage=%q)", err, final.State, final.Stage)
	}
	if final.State != StateCompleted {
		t.Errorf("State = %q, want %q", final.State, StateCompleted)
	}
	if final.Stage != StageDone {
		t.Errorf("Stage = %q, want %q", final.Stage, StageDone)
	}
	if len(final.SubsAcquired) != 1 || final.SubsAcquired[0] != "es" {
		t.Errorf("SubsAcquired = %v, want [es]", final.SubsAcquired)
	}
	if len(final.MissingSubs) != 0 {
		t.Errorf("MissingSubs = %v, want empty", final.MissingSubs)
	}

	joinParkedContinuation(t, slots)
}

// TestAwaitSubs_ParkedJobCancellable verifies requirement (iii) of PRO-3:
// cancelling a job parked in the background await must terminate the
// continuation promptly and leave the job Cancelled — never Completed.
func TestAwaitSubs_ParkedJobCancellable(t *testing.T) {
	fastPollInterval(t)
	stubBazarrTrigger(t)
	slots := swapAwaitSlots(t, 1)

	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Movie.mkv")
	os.WriteFile(mediaPath, []byte("fake"), 0644)

	q := parkTestQueue(t)
	created, err := q.Create(testSource(mediaPath))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := q.Update(created.ID, func(j *Job) { j.MissingSubs = []string{"es"} }); err != nil {
		t.Fatalf("Update (set MissingSubs): %v", err)
	}

	api := fakePeliculaAPI(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		processJob(q, created.ID, t.TempDir(), api)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("processJob did not return — job was not parked")
	}

	// The parked continuation still owns the registered cancel func; Cancel
	// must commit StateCancelled and unblock the await via ctx.
	if err := q.Cancel(created.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	// The continuation exits promptly (ctx.Done inside awaitSubtitles, then
	// the stage boundary observes the cancelled state).
	joinParkedContinuation(t, slots)

	got, ok := q.Get(created.ID)
	if !ok {
		t.Fatal("job not found after cancel")
	}
	if got.State != StateCancelled {
		t.Errorf("State = %q, want %q — parked job must stay cancelled, not resume to completion",
			got.State, StateCancelled)
	}
}

// TestAwaitSubs_InlineFallbackWhenSlotsFull verifies requirement (ii) of
// PRO-3 (bounded concurrency): when every park slot is taken, the run must
// fall back to the historical inline (worker-blocking) wait rather than
// skipping subtitle acquisition or parking without a slot. The overridden
// clock makes the inline wait's deadline trip on its first check so the test
// stays fast; processJob returning with the job already terminal proves the
// run stayed synchronous on the worker.
func TestAwaitSubs_InlineFallbackWhenSlotsFull(t *testing.T) {
	fastPollInterval(t)
	stubBazarrTrigger(t)
	slots := swapAwaitSlots(t, 1)
	slots <- struct{}{} // occupy the only slot: parking must be impossible

	oldNow := nowFn
	nowFn = func() time.Time { return time.Now().Add(2 * time.Hour) }
	t.Cleanup(func() { nowFn = oldNow })

	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Movie.mkv")
	os.WriteFile(mediaPath, []byte("fake"), 0644)

	q := parkTestQueue(t)
	created, err := q.Create(testSource(mediaPath))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := q.Update(created.ID, func(j *Job) { j.MissingSubs = []string{"es"} }); err != nil {
		t.Fatalf("Update (set MissingSubs): %v", err)
	}

	api := fakePeliculaAPI(t)
	processJob(q, created.ID, t.TempDir(), api)

	// Inline execution: by the time processJob returns, the run finished all
	// stages synchronously (await timed out via the overridden clock).
	got, ok := q.Get(created.ID)
	if !ok {
		t.Fatal("job not found")
	}
	if got.State != StateCompleted {
		t.Errorf("State = %q, want %q immediately after processJob — inline fallback did not run synchronously",
			got.State, StateCompleted)
	}
	// Timed out without acquisition: the language is still missing.
	if len(got.MissingSubs) != 1 || got.MissingSubs[0] != "es" {
		t.Errorf("MissingSubs = %v, want [es] (await timed out inline)", got.MissingSubs)
	}
	if len(slots) != 1 {
		t.Errorf("await slot count = %d, want 1 — inline fallback must not touch park slots", len(slots))
	}
}

// TestLoadExisting_RequeuesParkedAwaitSubsJob verifies the restart-recovery
// half of PRO-3: a job that dies while parked in the background await (row
// state=processing, stage=await_subs) must be re-queued from the top by the
// existing crash recovery, exactly like any other interrupted mid-flight job.
// The re-run is idempotent — any sidecars Bazarr delivered before the crash
// are found on the await stage's first poll.
func TestLoadExisting_RequeuesParkedAwaitSubsJob(t *testing.T) {
	db := testDB(t)
	q1, err := NewQueue(db)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	job, err := q1.Create(testSource("/downloads/parked.mkv"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := q1.Update(job.ID, func(j *Job) {
		j.State = StateProcessing
		j.Stage = StageAwaitSubs
		j.Progress = 0.41
		j.MissingSubs = []string{"es"}
	}); err != nil {
		t.Fatalf("Update (simulate parked): %v", err)
	}

	// Simulate restart: a fresh queue on the same DB runs loadExisting.
	q2, err := NewQueue(db)
	if err != nil {
		t.Fatalf("NewQueue (restart): %v", err)
	}
	got, ok := q2.Get(job.ID)
	if !ok {
		t.Fatal("parked job not found after restart")
	}
	if got.State != StateQueued {
		t.Errorf("State = %q, want %q — parked job must re-enter the queue on restart", got.State, StateQueued)
	}
	if got.Stage != StageValidate {
		t.Errorf("Stage = %q, want %q — recovery resets to the first stage", got.Stage, StageValidate)
	}
	if got.InterruptCount != 1 {
		t.Errorf("InterruptCount = %d, want 1 (parked interruption counts like any other)", got.InterruptCount)
	}
	if len(got.MissingSubs) != 1 || got.MissingSubs[0] != "es" {
		t.Errorf("MissingSubs = %v, want [es] preserved across recovery", got.MissingSubs)
	}
}
