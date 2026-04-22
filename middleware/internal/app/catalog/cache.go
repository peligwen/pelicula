package catalog

import (
	"context"
	"sync"
	"time"
)

const catalogCacheTTL = 2 * time.Minute

// CatalogCache is a shared, TTL-based cache for the full Radarr movie list and
// Sonarr series list. Both the catalog HTTP handler and the missing-content
// watcher use this cache to avoid redundant full-library fetches.
//
// Two independent RWMutexes allow Movies and Series fetches to proceed
// concurrently. Each uses double-checked locking: a read-lock checks freshness;
// on miss the read-lock is released and a write-lock is acquired; the write-lock
// path re-checks freshness so that only one goroutine actually fetches on a
// concurrent miss.
type CatalogCache struct {
	muMovies      sync.RWMutex
	movies        []byte
	moviesFetched time.Time

	muSeries      sync.RWMutex
	series        []byte
	seriesFetched time.Time

	fetchMovies func(ctx context.Context) ([]byte, error)
	fetchSeries func(ctx context.Context) ([]byte, error)

	// now is injectable for tests; production code leaves it nil (falls back to time.Now).
	now func() time.Time
}

// NewCatalogCache constructs a CatalogCache. fetchMovies and fetchSeries are
// called on cache miss; they should return the raw JSON body from Radarr/Sonarr.
func NewCatalogCache(
	fetchMovies func(ctx context.Context) ([]byte, error),
	fetchSeries func(ctx context.Context) ([]byte, error),
) *CatalogCache {
	return &CatalogCache{fetchMovies: fetchMovies, fetchSeries: fetchSeries}
}

func (c *CatalogCache) nowTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// GetMovies returns the cached Radarr movie list, refreshing if stale.
func (c *CatalogCache) GetMovies(ctx context.Context) ([]byte, error) {
	// Fast path: read-lock only.
	c.muMovies.RLock()
	if c.nowTime().Sub(c.moviesFetched) < catalogCacheTTL && c.movies != nil {
		data := c.movies
		c.muMovies.RUnlock()
		return data, nil
	}
	c.muMovies.RUnlock()

	// Slow path: write-lock + re-check before fetching.
	c.muMovies.Lock()
	defer c.muMovies.Unlock()
	if c.nowTime().Sub(c.moviesFetched) < catalogCacheTTL && c.movies != nil {
		return c.movies, nil
	}
	data, err := c.fetchMovies(ctx)
	if err != nil {
		return nil, err
	}
	c.movies = data
	c.moviesFetched = c.nowTime()
	return data, nil
}

// GetSeries returns the cached Sonarr series list, refreshing if stale.
func (c *CatalogCache) GetSeries(ctx context.Context) ([]byte, error) {
	// Fast path: read-lock only.
	c.muSeries.RLock()
	if c.nowTime().Sub(c.seriesFetched) < catalogCacheTTL && c.series != nil {
		data := c.series
		c.muSeries.RUnlock()
		return data, nil
	}
	c.muSeries.RUnlock()

	// Slow path: write-lock + re-check before fetching.
	c.muSeries.Lock()
	defer c.muSeries.Unlock()
	if c.nowTime().Sub(c.seriesFetched) < catalogCacheTTL && c.series != nil {
		return c.series, nil
	}
	data, err := c.fetchSeries(ctx)
	if err != nil {
		return nil, err
	}
	c.series = data
	c.seriesFetched = c.nowTime()
	return data, nil
}
