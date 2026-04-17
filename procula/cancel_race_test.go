package procula

import (
	"context"
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
