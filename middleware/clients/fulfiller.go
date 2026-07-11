package clients

import (
	"context"
	"errors"
)

// ErrInvalidSeasons is the sentinel wrapped by AddSeries implementations when
// a requested season number does not exist for the series being added (e.g.
// fmt.Errorf("%w: season(s) %v not found for this series", ErrInvalidSeasons,
// badNums)). HTTP handlers detect it with errors.Is and map it to 400, distinct
// from the 502 used when the upstream *arr itself is unreachable.
var ErrInvalidSeasons = errors.New("invalid seasons")

// Fulfiller handles the "add to *arr" side of a request approval. Peligrosa's
// request-queue handlers depend on this interface instead of calling
// addMovieInternal/addSeriesInternal directly, so the peligrosa subpackage
// avoids reaching into search.go.
type Fulfiller interface {
	AddMovie(ctx context.Context, tmdbID, profileID int, rootPath string) (int, error)
	// AddSeries adds a series to Sonarr. seasons selects which season numbers
	// to monitor; nil means "monitor all seasons" (today's default). A
	// non-nil seasons whose numbers don't all exist on the series returns
	// ErrInvalidSeasons wrapped with the offending numbers.
	AddSeries(ctx context.Context, tvdbID, profileID int, rootPath string, seasons []int) (int, error)
}
