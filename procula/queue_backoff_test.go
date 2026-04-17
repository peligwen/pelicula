package procula

import (
	"testing"
	"time"
)

// TestNextQueued_BackoffFilter verifies that nextQueued correctly applies the
// datetime() comparison for RFC3339Nano next_attempt_at values stored in SQLite.
// This guards against the TEXT comparison bug where 'T' (0x54) > ' ' (0x20)
// caused deferred jobs to never be picked up.
func TestNextQueued_BackoffFilter(t *testing.T) {
	q := newTestQueue(t)

	// Job 1: next_attempt_at in the past — should be returned by nextQueued.
	past := time.Now().UTC().Add(-5 * time.Second)
	pastJob, err := q.Create(testSource("/downloads/past.mkv"))
	if err != nil {
		t.Fatalf("Create past job: %v", err)
	}
	if err := q.Update(pastJob.ID, func(j *Job) {
		j.NextAttemptAt = &past
	}); err != nil {
		t.Fatalf("Update past job: %v", err)
	}

	// Job 2: next_attempt_at in the future — should NOT be returned.
	future := time.Now().UTC().Add(5 * time.Minute)
	futureJob, err := q.Create(testSource("/downloads/future.mkv"))
	if err != nil {
		t.Fatalf("Create future job: %v", err)
	}
	if err := q.Update(futureJob.ID, func(j *Job) {
		j.NextAttemptAt = &future
	}); err != nil {
		t.Fatalf("Update future job: %v", err)
	}

	// Job 3: next_attempt_at IS NULL — should be returned immediately.
	nullJob, err := q.Create(testSource("/downloads/null.mkv"))
	if err != nil {
		t.Fatalf("Create null job: %v", err)
	}
	// next_attempt_at is nil by default — no update needed.

	ids := q.nextQueued()
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}

	if !idSet[pastJob.ID] {
		t.Errorf("nextQueued: past job (next_attempt_at -5s) was NOT returned — datetime() comparison broken")
	}
	if idSet[futureJob.ID] {
		t.Errorf("nextQueued: future job (next_attempt_at +5m) was returned — should be deferred")
	}
	if !idSet[nullJob.ID] {
		t.Errorf("nextQueued: null next_attempt_at job was NOT returned — should be immediately eligible")
	}
}

// TestRetry_ClearsNextAttemptAt verifies that Retry() nulls out NextAttemptAt
// so a job with a backoff timestamp is immediately re-eligible after manual retry.
func TestRetry_ClearsNextAttemptAt(t *testing.T) {
	q := newTestQueue(t)

	job, err := q.Create(testSource("/downloads/retry.mkv"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Simulate a transient failure with a future backoff timestamp.
	future := time.Now().UTC().Add(2 * time.Hour)
	if err := q.Update(job.ID, func(j *Job) {
		j.State = StateFailed
		j.Error = "transient: connection refused"
		j.NextAttemptAt = &future
	}); err != nil {
		t.Fatalf("Update to failed+backoff: %v", err)
	}

	if err := q.Retry(job.ID); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	got, ok := q.Get(job.ID)
	if !ok {
		t.Fatal("Get after Retry: not found")
	}
	if got.State != StateQueued {
		t.Errorf("State = %q, want queued", got.State)
	}
	if got.NextAttemptAt != nil {
		t.Errorf("NextAttemptAt = %v, want nil — Retry must clear backoff timestamp", got.NextAttemptAt)
	}

	// Verify the job now appears in nextQueued (i.e. immediately eligible).
	ids := q.nextQueued()
	found := false
	for _, id := range ids {
		if id == job.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("job not returned by nextQueued after Retry — backoff timestamp not cleared in DB")
	}
}
