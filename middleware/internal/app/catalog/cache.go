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
type CatalogCache struct {
	mu            sync.Mutex
	movies        []byte
	series        []byte
	moviesFetched time.Time
	seriesFetched time.Time
	fetchMovies   func(ctx context.Context) ([]byte, error)
	fetchSeries   func(ctx context.Context) ([]byte, error)
}

// NewCatalogCache constructs a CatalogCache. fetchMovies and fetchSeries are
// called on cache miss; they should return the raw JSON body from Radarr/Sonarr.
func NewCatalogCache(
	fetchMovies func(ctx context.Context) ([]byte, error),
	fetchSeries func(ctx context.Context) ([]byte, error),
) *CatalogCache {
	return &CatalogCache{fetchMovies: fetchMovies, fetchSeries: fetchSeries}
}

// GetMovies returns the cached Radarr movie list, refreshing if stale.
func (c *CatalogCache) GetMovies(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.moviesFetched) < catalogCacheTTL && c.movies != nil {
		return c.movies, nil
	}
	data, err := c.fetchMovies(ctx)
	if err != nil {
		return nil, err
	}
	c.movies = data
	c.moviesFetched = time.Now()
	return data, nil
}

// GetSeries returns the cached Sonarr series list, refreshing if stale.
func (c *CatalogCache) GetSeries(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.seriesFetched) < catalogCacheTTL && c.series != nil {
		return c.series, nil
	}
	data, err := c.fetchSeries(ctx)
	if err != nil {
		return nil, err
	}
	c.series = data
	c.seriesFetched = time.Now()
	return data, nil
}
