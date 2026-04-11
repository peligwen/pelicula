package clients

// Fulfiller handles the "add to *arr" side of a request approval. Peligrosa's
// request-queue handlers depend on this interface instead of calling
// addMovieInternal/addSeriesInternal directly, so the peligrosa subpackage
// avoids reaching into search.go.
type Fulfiller interface {
	AddMovie(tmdbID, profileID int, rootPath string) (int, error)
	AddSeries(tvdbID, profileID int, rootPath string) (int, error)
}
