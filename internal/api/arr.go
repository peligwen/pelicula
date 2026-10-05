package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// apiError is an error that already knows which HTTP status it maps to.
// addToArr returns these for bad input and missing titles; anything else is
// treated as an upstream failure (502).
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &apiError{http.StatusBadRequest, fmt.Sprintf(format, args...)}
}

// writeArrError maps an addToArr (or other *arr) error onto a response.
func (s *Server) writeArrError(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeError(w, ae.status, ae.msg)
		return
	}
	s.log().Warn("arr request failed", "err", err)
	writeError(w, http.StatusBadGateway, err.Error())
}

// addToArr puts a title into Radarr (mediaType "movie", keyed by tmdbID) or
// Sonarr (mediaType "series", keyed by tvdbID) and starts a search. It
// returns the *arr id plus the title/year/poster from the lookup.
//
// A title that is already in the library is not added again: addToArr
// triggers a search for it and returns the existing id.
func (s *Server) addToArr(ctx context.Context, mediaType string, tmdbID, tvdbID int) (arrID int, title string, year int, poster string, err error) {
	switch mediaType {
	case "movie":
		return s.addMovie(ctx, tmdbID)
	case "series":
		return s.addSeries(ctx, tvdbID)
	}
	return 0, "", 0, "", badRequest("type must be 'movie' or 'series'")
}

func (s *Server) addMovie(ctx context.Context, tmdbID int) (int, string, int, string, error) {
	if tmdbID <= 0 {
		return 0, "", 0, "", badRequest("tmdb_id is required for movies")
	}
	if s.Radarr == nil {
		return 0, "", 0, "", errors.New("radarr is not configured")
	}
	results, err := s.Radarr.LookupMovieByTmdbID(ctx, tmdbID)
	if err != nil {
		return 0, "", 0, "", fmt.Errorf("radarr lookup: %w", err)
	}
	if len(results) == 0 {
		return 0, "", 0, "", &apiError{http.StatusNotFound, "movie not found"}
	}
	m := results[0]
	title, year, poster := str(m, "title"), num(m, "year"), posterOf(m)

	if id := num(m, "id"); id != 0 {
		err := s.Radarr.TriggerCommand(ctx, map[string]any{"name": "MoviesSearch", "movieIds": []int{id}})
		if err != nil {
			return 0, "", 0, "", fmt.Errorf("radarr search: %w", err)
		}
		return id, title, year, poster, nil
	}

	profile, err := firstProfileID(ctx, s.Radarr)
	if err != nil {
		return 0, "", 0, "", fmt.Errorf("radarr: %w", err)
	}
	payload := copyKeys(m, "title", "year", "titleSlug", "images")
	payload["tmdbId"] = tmdbID
	payload["qualityProfileId"] = profile
	payload["rootFolderPath"] = s.Cfg.MoviesPath
	payload["monitored"] = true
	payload["minimumAvailability"] = "released"
	payload["addOptions"] = map[string]any{"searchForMovie": true}
	added, err := s.Radarr.AddMovie(ctx, payload)
	if err != nil {
		return 0, "", 0, "", fmt.Errorf("radarr add: %w", err)
	}
	id := num(added, "id")
	if id == 0 {
		return 0, "", 0, "", errors.New("radarr add: response had no id")
	}
	return id, title, year, poster, nil
}

func (s *Server) addSeries(ctx context.Context, tvdbID int) (int, string, int, string, error) {
	if tvdbID <= 0 {
		return 0, "", 0, "", badRequest("tvdb_id is required for series")
	}
	if s.Sonarr == nil {
		return 0, "", 0, "", errors.New("sonarr is not configured")
	}
	results, err := s.Sonarr.LookupSeries(ctx, "tvdb:"+strconv.Itoa(tvdbID))
	if err != nil {
		return 0, "", 0, "", fmt.Errorf("sonarr lookup: %w", err)
	}
	if len(results) == 0 {
		return 0, "", 0, "", &apiError{http.StatusNotFound, "series not found"}
	}
	m := results[0]
	title, year, poster := str(m, "title"), num(m, "year"), posterOf(m)

	if id := num(m, "id"); id != 0 {
		err := s.Sonarr.TriggerCommand(ctx, map[string]any{"name": "SeriesSearch", "seriesId": id})
		if err != nil {
			return 0, "", 0, "", fmt.Errorf("sonarr search: %w", err)
		}
		return id, title, year, poster, nil
	}

	profile, err := firstProfileID(ctx, s.Sonarr)
	if err != nil {
		return 0, "", 0, "", fmt.Errorf("sonarr: %w", err)
	}
	payload := copyKeys(m, "title", "year", "titleSlug", "images", "seasons")
	payload["tvdbId"] = tvdbID
	payload["qualityProfileId"] = profile
	payload["rootFolderPath"] = s.Cfg.TVPath
	payload["seasonFolder"] = true
	payload["monitored"] = true
	payload["addOptions"] = map[string]any{"searchForMissingEpisodes": true}
	added, err := s.Sonarr.AddSeries(ctx, payload)
	if err != nil {
		return 0, "", 0, "", fmt.Errorf("sonarr add: %w", err)
	}
	id := num(added, "id")
	if id == 0 {
		return 0, "", 0, "", errors.New("sonarr add: response had no id")
	}
	return id, title, year, poster, nil
}

// firstProfileID returns the id of the first quality profile c reports.
func firstProfileID(ctx context.Context, c ArrClient) (int, error) {
	profiles, err := c.GetQualityProfiles(ctx)
	if err != nil {
		return 0, fmt.Errorf("quality profiles: %w", err)
	}
	for _, p := range profiles {
		if id := num(p, "id"); id != 0 {
			return id, nil
		}
	}
	return 0, errors.New("no quality profiles configured")
}

// copyKeys returns a new map holding the non-nil values of src under keys.
func copyKeys(src map[string]any, keys ...string) map[string]any {
	dst := make(map[string]any, len(keys)+8)
	for _, k := range keys {
		if v := src[k]; v != nil {
			dst[k] = v
		}
	}
	return dst
}
