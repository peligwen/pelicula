package util

import "sync"

// SkipCounter tracks consecutive fetch failures and computes how many ticks
// should be skipped before retrying. Call RecordSuccess on a clean result and
// RecordFailure on an error. Call ShouldSkip at the top of each tick to check
// whether the fetch should be omitted this cycle.
//
// After N consecutive failures the next N ticks are skipped (N capped at
// maxSkip). A success resets the counters immediately.
type SkipCounter struct {
	mu          sync.Mutex
	failures    int
	maxSkip     int
	ticksToSkip int
}

// NewSkipCounter returns a SkipCounter that will skip at most maxSkip ticks
// after a run of consecutive failures.
func NewSkipCounter(maxSkip int) *SkipCounter {
	return &SkipCounter{maxSkip: maxSkip}
}

// RecordSuccess resets the failure count and clears any pending skip ticks.
func (s *SkipCounter) RecordSuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = 0
	s.ticksToSkip = 0
}

// RecordFailure increments the consecutive failure count and schedules the
// appropriate number of skip ticks.
func (s *SkipCounter) RecordFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	if s.failures > s.maxSkip {
		s.failures = s.maxSkip
	}
	s.ticksToSkip = s.failures
}

// ShouldSkip returns true if the current tick should be skipped. Each call
// that returns true decrements the internal skip counter by one.
func (s *SkipCounter) ShouldSkip() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ticksToSkip > 0 {
		s.ticksToSkip--
		return true
	}
	return false
}
