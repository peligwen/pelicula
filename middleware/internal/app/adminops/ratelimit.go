package adminops

import (
	"sync"
	"time"
)

// rateLimiter caps admin ops at 10 per minute per key (username when auth on, IP when off).
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string][]time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{buckets: make(map[string][]time.Time)}
}

func (rl *rateLimiter) allow(key string) bool {
	const limit = 10
	window := time.Now().Add(-time.Minute)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	times := rl.buckets[key]
	// trim expired
	valid := times[:0]
	for _, t := range times {
		if t.After(window) {
			valid = append(valid, t)
		}
	}
	rl.buckets[key] = valid
	if len(valid) >= limit {
		return false
	}
	rl.buckets[key] = append(rl.buckets[key], time.Now())
	return true
}
