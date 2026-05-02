package peligrosa

import "context"

// fakeFulfiller is a test double for clients.Fulfiller.
// Set addMovieFn/addSeriesFn to assert on calls; leave nil for a no-op.
type fakeFulfiller struct {
	addMovieFn  func(ctx context.Context, tmdbID, profileID int, rootPath string) (int, error)
	addSeriesFn func(ctx context.Context, tvdbID, profileID int, rootPath string) (int, error)
}

func (f *fakeFulfiller) AddMovie(ctx context.Context, tmdbID, profileID int, rootPath string) (int, error) {
	if f.addMovieFn != nil {
		return f.addMovieFn(ctx, tmdbID, profileID, rootPath)
	}
	return 0, nil
}

func (f *fakeFulfiller) AddSeries(ctx context.Context, tvdbID, profileID int, rootPath string) (int, error) {
	if f.addSeriesFn != nil {
		return f.addSeriesFn(ctx, tvdbID, profileID, rootPath)
	}
	return 0, nil
}
