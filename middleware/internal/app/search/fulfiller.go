package search

import (
	"context"

	"pelicula-api/clients"
)

// ArrFulfiller implements clients.Fulfiller backed by Sonarr/Radarr.
// It delegates to the Handler's addMovieInternal/addSeriesInternal methods.
type ArrFulfiller struct {
	handler *Handler
}

// NewArrFulfiller returns a clients.Fulfiller that delegates to the Handler's
// add-to-arr helpers.
func NewArrFulfiller(handler *Handler) clients.Fulfiller {
	return &ArrFulfiller{handler: handler}
}

func (f *ArrFulfiller) AddMovie(tmdbID, profileID int, rootPath string) (int, error) {
	// Phase 4 will thread the caller's ctx through Fulfiller.AddMovie.
	return f.handler.addMovieInternal(context.Background(), tmdbID, profileID, rootPath)
}

func (f *ArrFulfiller) AddSeries(tvdbID, profileID int, rootPath string) (int, error) {
	// Phase 4 will thread the caller's ctx through Fulfiller.AddSeries.
	return f.handler.addSeriesInternal(context.Background(), tvdbID, profileID, rootPath)
}
