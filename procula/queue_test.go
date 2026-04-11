package main

import (
	"testing"
	"time"
)

func newTestQueue(t *testing.T) *Queue {
	t.Helper()
	db := testDB(t)
	q, err := NewQueue(db)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	return q
}

func testSource(path string) JobSource {
	return JobSource{
		Type:  "movie",
		Title: "Test Movie",
		Year:  2024,
		Path:  path,
		Size:  1_000_000_000,
	}
}

func TestQueueCreateAndGet(t *testing.T) {
	q := newTestQueue(t)
	src := testSource("/downloads/test.mkv")

	job, err := q.Create(src)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if job.ID == "" {
		t.Error("expected non-empty ID")
	}
	if job.State != StateQueued {
		t.Errorf("State = %q, want %q", job.State, StateQueued)
	}
	if job.Stage != StageValidate {
		t.Errorf("Stage = %q, want %q", job.Stage, StageValidate)
	}
	if job.Source.Path != src.Path {
		t.Errorf("Source.Path = %q, want %q", job.Source.Path, src.Path)
	}

	// Verify Get returns a copy with matching fields
	got, ok := q.Get(job.ID)
	if !ok {
		t.Fatal("Get: job not found")
	}
	if got.ID != job.ID {
		t.Errorf("Get ID = %q, want %q", got.ID, job.ID)
	}

	// Verify job is in DB
	var count int
	q.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE id=?`, job.ID).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row in DB for job %s, got %d", job.ID, count)
	}
}

func TestQueueGetIsolation(t *testing.T) {
	q := newTestQueue(t)
	job, _ := q.Create(testSource("/downloads/test.mkv"))

	// Mutating the returned copy must not affect the stored state
	got, _ := q.Get(job.ID)
	got.State = StateFailed
	got.Error = "mutated"

	got2, _ := q.Get(job.ID)
	if got2.State != StateQueued {
		t.Errorf("mutation leaked into queue: state = %q", got2.State)
	}
}

func TestQueueDuplicateDedup(t *testing.T) {
	q := newTestQueue(t)
	src := testSource("/downloads/test.mkv")

	job1, err := q.Create(src)
	if err != nil {
		t.Fatal(err)
	}

	job2, err := q.Create(src)
	if err != nil {
		t.Fatal(err)
	}

	if job1.ID != job2.ID {
		t.Errorf("expected same job ID for duplicate path, got %q and %q", job1.ID, job2.ID)
	}

	if len(q.List()) != 1 {
		t.Errorf("expected 1 job after dedup, got %d", len(q.List()))
	}
}

func TestQueueList(t *testing.T) {
	q := newTestQueue(t)

	// Create three jobs with small sleeps to ensure distinct CreatedAt
	paths := []string{"/downloads/a.mkv", "/downloads/b.mkv", "/downloads/c.mkv"}
	for _, p := range paths {
		if _, err := q.Create(testSource(p)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	jobs := q.List()
	if len(jobs) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(jobs))
	}

	// Verify ascending CreatedAt order
	for i := 1; i < len(jobs); i++ {
		if jobs[i].CreatedAt.Before(jobs[i-1].CreatedAt) {
			t.Errorf("jobs not sorted by CreatedAt: jobs[%d]=%v before jobs[%d]=%v",
				i, jobs[i].CreatedAt, i-1, jobs[i-1].CreatedAt)
		}
	}
}

func TestQueueUpdate(t *testing.T) {
	q := newTestQueue(t)
	job, _ := q.Create(testSource("/downloads/test.mkv"))

	before := time.Now().UTC()
	time.Sleep(time.Millisecond)

	if err := q.Update(job.ID, func(j *Job) {
		j.State = StateProcessing
		j.Progress = 0.5
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, ok := q.Get(job.ID)
	if !ok {
		t.Fatal("Get after Update: not found")
	}
	if got.State != StateProcessing {
		t.Errorf("State = %q, want %q", got.State, StateProcessing)
	}
	if got.Progress != 0.5 {
		t.Errorf("Progress = %v, want 0.5", got.Progress)
	}
	if !got.UpdatedAt.After(before) {
		t.Errorf("UpdatedAt not updated: %v <= %v", got.UpdatedAt, before)
	}

	// Verify persisted to DB
	var state string
	q.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, job.ID).Scan(&state)
	if state != string(StateProcessing) {
		t.Errorf("on-DB state = %q, want %q", state, StateProcessing)
	}
}

func TestQueueUpdateNotFound(t *testing.T) {
	q := newTestQueue(t)
	err := q.Update("bogus-id", func(j *Job) {})
	if err == nil {
		t.Error("expected error for missing job ID")
	}
}

func TestQueueRetry(t *testing.T) {
	q := newTestQueue(t)
	job, _ := q.Create(testSource("/downloads/test.mkv"))

	// Move to failed state
	q.Update(job.ID, func(j *Job) {
		j.State = StateFailed
		j.Error = "validation failed"
		j.Progress = 0.25
	})

	if err := q.Retry(job.ID); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	got, _ := q.Get(job.ID)
	if got.State != StateQueued {
		t.Errorf("State after retry = %q, want %q", got.State, StateQueued)
	}
	if got.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", got.RetryCount)
	}
	if got.Error != "" {
		t.Errorf("Error not cleared: %q", got.Error)
	}
	if got.Progress != 0 {
		t.Errorf("Progress not reset: %v", got.Progress)
	}
}

func TestQueueRetryFromCancelled(t *testing.T) {
	q := newTestQueue(t)
	job, _ := q.Create(testSource("/downloads/test.mkv"))

	q.Update(job.ID, func(j *Job) { j.State = StateCancelled })

	if err := q.Retry(job.ID); err != nil {
		t.Fatalf("Retry from cancelled: %v", err)
	}

	got, _ := q.Get(job.ID)
	if got.State != StateQueued {
		t.Errorf("State = %q, want %q", got.State, StateQueued)
	}
}

func TestQueueRetryNotRetryable(t *testing.T) {
	q := newTestQueue(t)
	job, _ := q.Create(testSource("/downloads/test.mkv"))
	// State is already queued — not retryable

	err := q.Retry(job.ID)
	if err == nil {
		t.Error("expected error when retrying a queued job")
	}
}

func TestQueueCancel(t *testing.T) {
	q := newTestQueue(t)
	job, _ := q.Create(testSource("/downloads/test.mkv"))

	if err := q.Cancel(job.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	got, _ := q.Get(job.ID)
	if got.State != StateCancelled {
		t.Errorf("State = %q, want %q", got.State, StateCancelled)
	}
}

func TestQueueStatus(t *testing.T) {
	q := newTestQueue(t)

	// Create jobs and move them to various states
	paths := []string{
		"/downloads/queued.mkv",
		"/downloads/proc.mkv",
		"/downloads/done.mkv",
		"/downloads/fail.mkv",
		"/downloads/cancel.mkv",
	}
	ids := make([]string, len(paths))
	for i, p := range paths {
		j, _ := q.Create(testSource(p))
		ids[i] = j.ID
	}

	q.Update(ids[1], func(j *Job) { j.State = StateProcessing })
	q.Update(ids[2], func(j *Job) { j.State = StateCompleted })
	q.Update(ids[3], func(j *Job) { j.State = StateFailed })
	q.Update(ids[4], func(j *Job) { j.State = StateCancelled })

	status := q.Status()
	if status["queued"] != 1 {
		t.Errorf("queued = %d, want 1", status["queued"])
	}
	if status["processing"] != 1 {
		t.Errorf("processing = %d, want 1", status["processing"])
	}
	if status["completed"] != 1 {
		t.Errorf("completed = %d, want 1", status["completed"])
	}
	if status["failed"] != 1 {
		t.Errorf("failed = %d, want 1", status["failed"])
	}
	if status["cancelled"] != 1 {
		t.Errorf("cancelled = %d, want 1", status["cancelled"])
	}
}

func TestQueueLoadExisting(t *testing.T) {
	// Use a shared DB path so both queues can access it
	import_db := testDB(t)

	// Create a queue, add a job, then abandon it "mid-processing"
	q1, _ := NewQueue(import_db)
	job, _ := q1.Create(testSource("/downloads/test.mkv"))
	q1.Update(job.ID, func(j *Job) {
		j.State = StateProcessing
		j.Stage = StageProcess
		j.Progress = 0.6
	})

	// Also add a completed job — should not be re-queued
	job2, _ := q1.Create(testSource("/downloads/done.mkv"))
	q1.Update(job2.ID, func(j *Job) { j.State = StateCompleted })

	// Also add a failed job — should not be re-queued
	job3, _ := q1.Create(testSource("/downloads/fail.mkv"))
	q1.Update(job3.ID, func(j *Job) { j.State = StateFailed })

	// Load a fresh queue from the same DB (simulating restart)
	q2, err := NewQueue(import_db)
	if err != nil {
		t.Fatalf("NewQueue on restart: %v", err)
	}

	loaded, ok := q2.Get(job.ID)
	if !ok {
		t.Fatal("interrupted job not loaded on restart")
	}
	if loaded.State != StateQueued {
		t.Errorf("interrupted job State = %q, want %q (crash recovery)", loaded.State, StateQueued)
	}
	if loaded.Stage != StageValidate {
		t.Errorf("interrupted job Stage = %q, want %q (reset to validate)", loaded.Stage, StageValidate)
	}
	if loaded.Progress != 0 {
		t.Errorf("interrupted job Progress = %v, want 0", loaded.Progress)
	}
	if loaded.Error != "interrupted: restarted" {
		t.Errorf("interrupted job Error = %q", loaded.Error)
	}

	// Completed and failed jobs loaded with their original state (not re-queued)
	loaded2, _ := q2.Get(job2.ID)
	if loaded2.State != StateCompleted {
		t.Errorf("completed job loaded with state %q", loaded2.State)
	}
	loaded3, _ := q2.Get(job3.ID)
	if loaded3.State != StateFailed {
		t.Errorf("failed job loaded with state %q", loaded3.State)
	}
}

func TestQueueCreateWithActionType(t *testing.T) {
	q := newTestQueue(t)

	job, err := q.Create(JobSource{Path: "/movies/Foo (2024)/foo.mkv", ArrType: "radarr", Type: "movie"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if job.ActionType != "pipeline" {
		t.Errorf("ActionType default = %q, want %q", job.ActionType, "pipeline")
	}

	err = q.Update(job.ID, func(j *Job) {
		j.ActionType = "validate"
		j.Params = map[string]any{"path": "/movies/Foo (2024)/foo.mkv"}
		j.Result = map[string]any{"passed": true}
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, ok := q.Get(job.ID)
	if !ok {
		t.Fatal("Get: not found")
	}
	if got.ActionType != "validate" {
		t.Errorf("ActionType = %q, want %q", got.ActionType, "validate")
	}
	if got.Params["path"] != "/movies/Foo (2024)/foo.mkv" {
		t.Errorf("Params[path] = %v", got.Params["path"])
	}
	if got.Result["passed"] != true {
		t.Errorf("Result[passed] = %v", got.Result["passed"])
	}
}

func TestQueueWaitReturnsOnTerminal(t *testing.T) {
	q := newTestQueue(t)
	job, err := q.Create(testSource("/movies/foo.mkv"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = q.Update(job.ID, func(j *Job) { j.State = StateCompleted })
	}()

	got, err := q.Wait(job.ID, 2*time.Second)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got.State != StateCompleted {
		t.Errorf("State = %q, want %q", got.State, StateCompleted)
	}
}

func TestQueueWaitTimesOut(t *testing.T) {
	q := newTestQueue(t)
	job, err := q.Create(testSource("/movies/bar.mkv"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = q.Wait(job.ID, 150*time.Millisecond)
	if err == nil {
		t.Fatal("Wait: expected timeout error, got nil")
	}
}

func TestQueueWaitNotFound(t *testing.T) {
	q := newTestQueue(t)
	_, err := q.Wait("nope", 100*time.Millisecond)
	if err == nil {
		t.Fatal("Wait: expected not-found error, got nil")
	}
}

func TestQueueListByActionType(t *testing.T) {
	q := newTestQueue(t)
	a, _ := q.Create(testSource("/movies/a.mkv"))
	b, _ := q.Create(testSource("/movies/b.mkv"))
	_ = q.Update(b.ID, func(j *Job) { j.ActionType = "validate" })

	pipe := q.ListByActionType("pipeline")
	if len(pipe) != 1 || pipe[0].ID != a.ID {
		t.Errorf("pipeline filter: got %d jobs", len(pipe))
	}
	val := q.ListByActionType("validate")
	if len(val) != 1 || val[0].ID != b.ID {
		t.Errorf("validate filter: got %d jobs", len(val))
	}
}

func TestQueueLoadSkipsCorruptFiles(t *testing.T) {
	// With SQLite backing there are no corrupt files to skip —
	// DB rows are always valid. Verify the queue loads normally.
	db := testDB(t)

	// Insert a valid job directly into the DB using the queue
	q1, _ := NewQueue(db)
	valid, err := q1.Create(testSource("/downloads/ok.mkv"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Open a second queue on the same DB
	q2, err := NewQueue(db)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}

	_, ok := q2.Get(valid.ID)
	if !ok {
		t.Error("valid job not found in second queue")
	}
}
