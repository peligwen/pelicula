package catalog

import (
	"context"
	"pelicula-api/internal/app/util"
	"time"
)

const catalogCacheTTL = 2 * time.Minute

// CatalogCache is a shared, TTL-based cache for the full Radarr movie list and
// Sonarr series list. Both the catalog HTTP handler and the missing-content
// watcher use this cache to avoid redundant full-library fetches.
//
// Two independent TTLCache instances allow Movies and Series fetches to proceed
// concurrently. Each uses double-checked locking: a read-lock checks freshness;
// on miss the read-lock is released and a write-lock is acquired; the write-lock
// path re-checks freshness so that only one goroutine actually fetches on a
// concurrent miss.
type CatalogCache struct {
	movies *util.TTLCache[[]byte]
	series *util.TTLCache[[]byte]

	// now is injectable for tests; production code leaves it nil (falls back to time.Now).
	// The TTLCache instances hold a closure that dereferences this field, so
	// setting c.now before any Get call is reflected immediately.
	now func() time.Time
}

// NewCatalogCache constructs a CatalogCache. fetchMovies and fetchSeries are
// called on cache miss; they should return the raw JSON body from Radarr/Sonarr.
func NewCatalogCache(
	fetchMovies func(ctx context.Context) ([]byte, error),
	fetchSeries func(ctx context.Context) ([]byte, error),
) *CatalogCache {
	c := &CatalogCache{}
	// Pass a clock function that always reads through c.now so test injection
	// via "c.now = fn" takes effect without rebuilding the TTLCaches.
	clock := func() time.Time {
		if c.now != nil {
			return c.now()
		}
		return time.Now()
	}
	c.movies = util.NewTTLCacheWithClock(catalogCacheTTL, fetchMovies, clock)
	c.series = util.NewTTLCacheWithClock(catalogCacheTTL, fetchSeries, clock)
	return c
}

// GetMovies returns the cached Radarr movie list, refreshing if stale.
func (c *CatalogCache) GetMovies(ctx context.Context) ([]byte, error) {
	return c.movies.Get(ctx)
}

// GetSeries returns the cached Sonarr series list, refreshing if stale.
func (c *CatalogCache) GetSeries(ctx context.Context) ([]byte, error) {
	return c.series.Get(ctx)
}
