package util

import (
	"sync"
	"testing"
)

func TestSkipCounter_CountsUpToMax(t *testing.T) {
	sc := NewSkipCounter(3)

	// First failure: failures=1, ticksToSkip=1
	sc.RecordFailure()
	if !sc.ShouldSkip() {
		t.Error("expected skip after 1 failure")
	}
	// ticksToSkip now 0
	if sc.ShouldSkip() {
		t.Error("expected no skip after consuming skip tick")
	}

	// Reset state via success then fail again for clean count
	sc.RecordSuccess()

	// Two failures: failures=2, ticksToSkip=2
	sc.RecordFailure()
	sc.RecordFailure()
	if !sc.ShouldSkip() {
		t.Error("expected skip 1/2")
	}
	if !sc.ShouldSkip() {
		t.Error("expected skip 2/2")
	}
	if sc.ShouldSkip() {
		t.Error("expected no more skips after count exhausted")
	}
}

func TestSkipCounter_RecordSuccessResetsCount(t *testing.T) {
	sc := NewSkipCounter(5)

	sc.RecordFailure()
	sc.RecordFailure()
	sc.RecordFailure()

	// Mid-way success clears pending skips immediately.
	sc.RecordSuccess()
	if sc.ShouldSkip() {
		t.Error("RecordSuccess should have cleared all skip ticks")
	}

	// After success, a single failure schedules only 1 skip tick.
	sc.RecordFailure()
	if !sc.ShouldSkip() {
		t.Error("expected 1 skip after fresh failure post-success")
	}
	if sc.ShouldSkip() {
		t.Error("expected no more skips")
	}
}

func TestSkipCounter_NeverExceedsMax(t *testing.T) {
	max := 3
	sc := NewSkipCounter(max)

	// Record many more failures than max.
	for i := 0; i < 20; i++ {
		sc.RecordFailure()
	}

	// Drain all scheduled skip ticks; must be exactly max.
	count := 0
	for sc.ShouldSkip() {
		count++
		if count > max+1 {
			t.Fatalf("ShouldSkip returned true more than maxSkip (%d) times", max)
		}
	}
	if count != max {
		t.Errorf("expected %d skip ticks, got %d", max, count)
	}
}

func TestSkipCounter_InitiallyNoSkip(t *testing.T) {
	sc := NewSkipCounter(5)
	if sc.ShouldSkip() {
		t.Error("fresh SkipCounter should not skip on first check")
	}
}

func TestSkipCounter_ConcurrentSafe(t *testing.T) {
	sc := NewSkipCounter(10)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				sc.RecordSuccess()
			} else if i%3 == 1 {
				sc.RecordFailure()
			} else {
				sc.ShouldSkip()
			}
		}(i)
	}
	wg.Wait()
	// No assertions beyond "did not panic/race" — correctness under
	// arbitrary interleaving is validated by go test -race.
}
