package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestQueue(t *testing.T) *Queue {
	t.Helper()
	q, err := NewQueue(t.TempDir())
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

	// Verify JSON file written to disk
	jobPath := filepath.Join(q.configDir, "jobs", job.ID+".json")
	data, err := os.ReadFile(jobPath)
	if err != nil {
		t.Fatalf("job file not on disk: %v", err)
	}
	var onDisk Job
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("corrupt job file: %v", err)
	}
	if onDisk.ID != job.ID {
		t.Errorf("on-disk ID = %q, want %q", onDisk.ID, job.ID)
	}
}

func TestQueueGetIsolation(t *testing.T) {
	q := newTestQueue(t)
	job, _ := q.Create(testSource("/downloads/test.mkv"))

	// Mutating the returned copy must not affect the queue's internal state
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

	// Verify persisted to disk
	data, _ := os.ReadFile(filepath.Join(q.configDir, "jobs", job.ID+".json"))
	var onDisk Job
	json.Unmarshal(data, &onDisk)
	if onDisk.State != StateProcessing {
		t.Errorf("on-disk State = %q, want %q", onDisk.State, StateProcessing)
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
	dir := t.TempDir()

	// Create a queue, add a job, then abandon it "mid-processing"
	q1, _ := NewQueue(dir)
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

	// Load a fresh queue from the same directory (simulating restart)
	q2, err := NewQueue(dir)
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

func TestQueueLoadSkipsCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	jobsDir := filepath.Join(dir, "jobs")
	os.MkdirAll(jobsDir, 0755)

	// Write a corrupt JSON file
	os.WriteFile(filepath.Join(jobsDir, "job_corrupt.json"), []byte("not json{{"), 0644)

	// Also write a valid job
	valid := Job{
		ID:        "job_valid",
		State:     StateQueued,
		Stage:     StageValidate,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Source:    testSource("/downloads/ok.mkv"),
	}
	data, _ := json.Marshal(valid)
	os.WriteFile(filepath.Join(jobsDir, "job_valid.json"), data, 0644)

	q, err := NewQueue(dir)
	if err != nil {
		t.Fatalf("NewQueue with corrupt file: %v", err)
	}
	if len(q.jobs) != 1 {
		t.Errorf("expected 1 job loaded (corrupt skipped), got %d", len(q.jobs))
	}
	if _, ok := q.jobs["job_valid"]; !ok {
		t.Error("valid job not loaded")
	}
}
