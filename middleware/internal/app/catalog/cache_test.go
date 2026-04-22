package catalog

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestCache builds a CatalogCache with injectable now and counting fetch stubs.
func newTestCache(
	moviesFn func(ctx context.Context) ([]byte, error),
	seriesFn func(ctx context.Context) ([]byte, error),
	nowFn func() time.Time,
) *CatalogCache {
	c := NewCatalogCache(moviesFn, seriesFn)
	c.now = nowFn
	return c
}

func TestCatalogCache_FirstCallFetches(t *testing.T) {
	var calls atomic.Int32
	fn := func(ctx context.Context) ([]byte, error) {
		calls.Add(1)
		return []byte(`[{"id":1}]`), nil
	}
	now := time.Now()
	c := newTestCache(fn, fn, func() time.Time { return now })

	data, err := c.GetMovies(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != `[{"id":1}]` {
		t.Errorf("data = %q", data)
	}
	if calls.Load() != 1 {
		t.Errorf("fetch calls = %d, want 1", calls.Load())
	}
}

func TestCatalogCache_SecondCallWithinTTLReturnsCached(t *testing.T) {
	var calls atomic.Int32
	fn := func(ctx context.Context) ([]byte, error) {
		calls.Add(1)
		return []byte(`[]`), nil
	}
	now := time.Now()
	c := newTestCache(fn, fn, func() time.Time { return now })

	c.GetMovies(context.Background()) //nolint:errcheck
	c.GetMovies(context.Background()) //nolint:errcheck

	if calls.Load() != 1 {
		t.Errorf("fetch calls = %d, want 1 (second call should use cache)", calls.Load())
	}
}

func TestCatalogCache_AfterTTLRefetches(t *testing.T) {
	var calls atomic.Int32
	fn := func(ctx context.Context) ([]byte, error) {
		calls.Add(1)
		return []byte(`[]`), nil
	}

	// Start at t=0.
	nowVal := time.Now()
	nowFn := func() time.Time { return nowVal }
	c := newTestCache(fn, fn, nowFn)

	c.GetMovies(context.Background()) //nolint:errcheck
	// Advance beyond TTL.
	nowVal = nowVal.Add(catalogCacheTTL + time.Second)
	c.GetMovies(context.Background()) //nolint:errcheck

	if calls.Load() != 2 {
		t.Errorf("fetch calls = %d, want 2 (stale cache should refetch)", calls.Load())
	}
}

func TestCatalogCache_FetchErrorDoesNotPoisonCache(t *testing.T) {
	var calls atomic.Int32
	errFn := func(ctx context.Context) ([]byte, error) {
		calls.Add(1)
		return nil, errors.New("temporary failure")
	}
	now := time.Now()
	c := newTestCache(errFn, errFn, func() time.Time { return now })

	// First call errors.
	data, err := c.GetMovies(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if data != nil {
		t.Error("expected nil data on error")
	}

	// Second call should attempt fetch again (cache not poisoned / not set).
	data2, err2 := c.GetMovies(context.Background())
	if err2 == nil {
		t.Fatal("expected error on second call too")
	}
	if data2 != nil {
		t.Error("expected nil data on second error")
	}
	if calls.Load() != 2 {
		t.Errorf("fetch calls = %d, want 2 (failed fetch must not cache nil)", calls.Load())
	}
}

func TestCatalogCache_ConcurrentGetMoviesGetSeries(t *testing.T) {
	const goroutinesPerType = 50
	const fakeDelay = 50 * time.Millisecond

	var movieCalls, seriesCalls atomic.Int32

	movieFn := func(ctx context.Context) ([]byte, error) {
		movieCalls.Add(1)
		time.Sleep(fakeDelay)
		return []byte(`[{"type":"movie"}]`), nil
	}
	seriesFn := func(ctx context.Context) ([]byte, error) {
		seriesCalls.Add(1)
		time.Sleep(fakeDelay)
		return []byte(`[{"type":"series"}]`), nil
	}

	now := time.Now()
	c := newTestCache(movieFn, seriesFn, func() time.Time { return now })

	var wg sync.WaitGroup
	for range goroutinesPerType {
		wg.Add(2)
		go func() {
			defer wg.Done()
			c.GetMovies(context.Background()) //nolint:errcheck
		}()
		go func() {
			defer wg.Done()
			c.GetSeries(context.Background()) //nolint:errcheck
		}()
	}
	wg.Wait()

	if movieCalls.Load() != 1 {
		t.Errorf("movie fetch calls = %d, want 1", movieCalls.Load())
	}
	if seriesCalls.Load() != 1 {
		t.Errorf("series fetch calls = %d, want 1", seriesCalls.Load())
	}
}

func TestCatalogCache_SlowMovieFetchDoesNotBlockSeriesFetch(t *testing.T) {
	const movieDelay = 100 * time.Millisecond
	const seriesDelay = 5 * time.Millisecond
	const maxSeriesWait = 20 * time.Millisecond

	movieFn := func(ctx context.Context) ([]byte, error) {
		time.Sleep(movieDelay)
		return []byte(`[]`), nil
	}
	seriesFn := func(ctx context.Context) ([]byte, error) {
		time.Sleep(seriesDelay)
		return []byte(`[]`), nil
	}

	now := time.Now()
	c := newTestCache(movieFn, seriesFn, func() time.Time { return now })

	// Start movie fetch in background (it is slow).
	go c.GetMovies(context.Background()) //nolint:errcheck

	// Give the movie goroutine a moment to acquire the write lock.
	time.Sleep(2 * time.Millisecond)

	// Series fetch should complete well within maxSeriesWait despite movie still in-flight.
	start := time.Now()
	_, err := c.GetSeries(context.Background())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("GetSeries error: %v", err)
	}
	if elapsed > maxSeriesWait+seriesDelay+10*time.Millisecond {
		t.Errorf("GetSeries took %v, want < %v (movies mutex should not block series)",
			elapsed, maxSeriesWait+seriesDelay)
	}
}
