package util

import (
	"context"
	"sync"
	"time"
)

// TTLCache is a generic, single-value, TTL-based cache. It wraps a single
// value of type T and refreshes it by calling a caller-supplied fetch function
// when the TTL has expired.
//
// The implementation uses double-checked locking: a read-lock is taken for the
// fast path (within TTL); on a miss the read-lock is released and a write-lock
// is acquired with a re-check before fetching, so only one goroutine fetches on
// a concurrent miss.
//
// A failed fetch (non-nil error) does not poison the cache: the existing value
// is not replaced, and the next call will retry.
type TTLCache[T any] struct {
	mu      sync.RWMutex
	value   T
	fetched time.Time
	ttl     time.Duration
	hasVal  bool // true once value has been successfully populated

	fetch func(ctx context.Context) (T, error)
	now   func() time.Time
}

// NewTTLCache constructs a TTLCache with the given TTL and fetch function.
// fetch is called on cache miss; it should return the value to cache and any
// error. On error the existing (possibly zero) value is returned unchanged and
// the cache entry is not updated.
func NewTTLCache[T any](ttl time.Duration, fetch func(ctx context.Context) (T, error)) *TTLCache[T] {
	return &TTLCache[T]{ttl: ttl, fetch: fetch, now: time.Now}
}

// NewTTLCacheWithClock is like NewTTLCache but uses the provided clock function
// instead of time.Now. Useful for injecting a shared clock into multiple caches
// (e.g. a closure that reads a parent struct field) so they all advance together.
func NewTTLCacheWithClock[T any](ttl time.Duration, fetch func(ctx context.Context) (T, error), clock func() time.Time) *TTLCache[T] {
	return &TTLCache[T]{ttl: ttl, fetch: fetch, now: clock}
}

func (c *TTLCache[T]) nowTime() time.Time {
	return c.now()
}

func (c *TTLCache[T]) isFresh() bool {
	return c.hasVal && c.nowTime().Sub(c.fetched) < c.ttl
}

// Get returns the cached value, refreshing it if the TTL has expired.
// Errors from the fetch function are returned directly without updating the
// cache, so the caller sees the error and can decide how to handle it.
func (c *TTLCache[T]) Get(ctx context.Context) (T, error) {
	// Fast path: read-lock only.
	c.mu.RLock()
	if c.isFresh() {
		v := c.value
		c.mu.RUnlock()
		return v, nil
	}
	c.mu.RUnlock()

	// Slow path: write-lock + re-check before fetching.
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.isFresh() {
		return c.value, nil
	}
	v, err := c.fetch(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	c.value = v
	c.fetched = c.nowTime()
	c.hasVal = true
	return v, nil
}
