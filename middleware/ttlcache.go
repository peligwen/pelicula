package main

import (
	"sync"
	"time"
)

// ttlCache is a generic single-value cache with a configurable TTL.
// The zero value is usable; set TTL before first use or pass it via Get.
// Thread-safe: Get serialises fetch calls under the mutex.
type ttlCache[T any] struct {
	mu  sync.Mutex
	val T
	err error
	at  time.Time
	ttl time.Duration
}

// Get returns the cached value if it is still within the TTL; otherwise it
// calls fetch, stores the result (even on error), and returns it.
func (c *ttlCache[T]) Get(fetch func() (T, error)) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl > 0 && !c.at.IsZero() && time.Since(c.at) < c.ttl {
		return c.val, c.err
	}
	val, err := fetch()
	c.val = val
	c.err = err
	c.at = time.Now()
	return c.val, c.err
}
