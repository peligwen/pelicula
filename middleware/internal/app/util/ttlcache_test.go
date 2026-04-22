package util

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTTLCache_BasicBehavior(t *testing.T) {
	t.Run("FirstCallFetches", func(t *testing.T) {
		var calls atomic.Int32
		fn := func(ctx context.Context) (string, error) {
			calls.Add(1)
			return "hello", nil
		}
		now := time.Now()
		c := NewTTLCacheWithClock(time.Minute, fn, func() time.Time { return now })

		v, err := c.Get(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v != "hello" {
			t.Errorf("value = %q, want %q", v, "hello")
		}
		if calls.Load() != 1 {
			t.Errorf("fetch calls = %d, want 1", calls.Load())
		}
	})

	t.Run("SecondCallWithinTTLReturnsCached", func(t *testing.T) {
		var calls atomic.Int32
		fn := func(ctx context.Context) (int, error) {
			n := calls.Add(1)
			return int(n), nil
		}
		now := time.Now()
		c := NewTTLCacheWithClock(time.Minute, fn, func() time.Time { return now })

		c.Get(context.Background()) //nolint:errcheck
		v2, _ := c.Get(context.Background())

		if calls.Load() != 1 {
			t.Errorf("fetch calls = %d, want 1 (second call should use cache)", calls.Load())
		}
		if v2 != 1 {
			t.Errorf("value on second call = %d, want 1", v2)
		}
	})

	t.Run("AfterTTLRefetches", func(t *testing.T) {
		var calls atomic.Int32
		fn := func(ctx context.Context) (string, error) {
			calls.Add(1)
			return "data", nil
		}

		nowVal := time.Now()
		c := NewTTLCacheWithClock(time.Minute, fn, func() time.Time { return nowVal })

		c.Get(context.Background()) //nolint:errcheck
		// Advance past TTL.
		nowVal = nowVal.Add(time.Minute + time.Second)
		c.Get(context.Background()) //nolint:errcheck

		if calls.Load() != 2 {
			t.Errorf("fetch calls = %d, want 2 (stale cache should refetch)", calls.Load())
		}
	})

	t.Run("FetchErrorDoesNotPoisonCache", func(t *testing.T) {
		var calls atomic.Int32
		fn := func(ctx context.Context) ([]byte, error) {
			calls.Add(1)
			return nil, errors.New("temporary failure")
		}
		now := time.Now()
		c := NewTTLCacheWithClock(time.Minute, fn, func() time.Time { return now })

		_, err1 := c.Get(context.Background())
		if err1 == nil {
			t.Fatal("expected error on first call, got nil")
		}
		_, err2 := c.Get(context.Background())
		if err2 == nil {
			t.Fatal("expected error on second call, got nil")
		}
		if calls.Load() != 2 {
			t.Errorf("fetch calls = %d, want 2 (failed fetch must not cache nil)", calls.Load())
		}
	})

	t.Run("ConcurrentGetCollapsesToOneFetch", func(t *testing.T) {
		var calls atomic.Int32
		fn := func(ctx context.Context) (string, error) {
			calls.Add(1)
			time.Sleep(20 * time.Millisecond)
			return "result", nil
		}
		now := time.Now()
		c := NewTTLCacheWithClock(time.Minute, fn, func() time.Time { return now })

		var wg sync.WaitGroup
		for range 50 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c.Get(context.Background()) //nolint:errcheck
			}()
		}
		wg.Wait()

		if n := calls.Load(); n != 1 {
			t.Errorf("fetch calls = %d, want 1 (concurrent callers should not all fetch)", n)
		}
	})
}
