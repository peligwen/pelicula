package main

// fakeFulfiller is a test double for Fulfiller.
// Set addMovieFn/addSeriesFn to assert on calls; leave nil for a no-op.
type fakeFulfiller struct {
	addMovieFn  func(tmdbID, profileID int, rootPath string) (int, error)
	addSeriesFn func(tvdbID, profileID int, rootPath string) (int, error)
}

func (f *fakeFulfiller) AddMovie(tmdbID, profileID int, rootPath string) (int, error) {
	if f.addMovieFn != nil {
		return f.addMovieFn(tmdbID, profileID, rootPath)
	}
	return 0, nil
}

func (f *fakeFulfiller) AddSeries(tvdbID, profileID int, rootPath string) (int, error) {
	if f.addSeriesFn != nil {
		return f.addSeriesFn(tvdbID, profileID, rootPath)
	}
	return 0, nil
}
