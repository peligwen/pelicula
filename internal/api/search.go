package api

import (
	"net/http"
	"strings"
	"sync"
)

const maxSearchResults = 40

// searchResult is one row of GET /api/search.
type searchResult struct {
	Type      string `json:"type"` // movie | series
	Title     string `json:"title"`
	Year      int    `json:"year"`
	Overview  string `json:"overview"`
	Poster    string `json:"poster"`
	TmdbID    int    `json:"tmdb_id"`
	TvdbID    int    `json:"tvdb_id"`
	InLibrary bool   `json:"in_library"`
	ArrID     int    `json:"arr_id"`
	HasFile   bool   `json:"has_file"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}

	var (
		wg                  sync.WaitGroup
		movies, series      []map[string]any
		movieErr, seriesErr error
	)
	if s.Radarr != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			movies, movieErr = s.Radarr.LookupMovie(r.Context(), q)
		}()
	}
	if s.Sonarr != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			series, seriesErr = s.Sonarr.LookupSeries(r.Context(), q)
		}()
	}
	wg.Wait()

	// One side down is not worth failing the whole search over.
	if movieErr != nil {
		s.log().Warn("radarr lookup failed", "err", movieErr)
	}
	if seriesErr != nil {
		s.log().Warn("sonarr lookup failed", "err", seriesErr)
	}
	if movieErr != nil && seriesErr != nil {
		writeError(w, http.StatusBadGateway, "search is unavailable: Radarr and Sonarr did not answer")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"results": interleave(movies, series)})
}

// interleave merges movie and series lookups as movie, series, movie, ...
// (leftovers of the longer list follow) and caps the result at
// maxSearchResults.
func interleave(movies, series []map[string]any) []searchResult {
	out := make([]searchResult, 0, min(len(movies)+len(series), maxSearchResults))
	for i := 0; len(out) < maxSearchResults && (i < len(movies) || i < len(series)); i++ {
		if i < len(movies) {
			out = append(out, normalizeLookup("movie", movies[i]))
		}
		if i < len(series) && len(out) < maxSearchResults {
			out = append(out, normalizeLookup("series", series[i]))
		}
	}
	return out
}

// normalizeLookup flattens a Radarr/Sonarr lookup record. kind is "movie" or
// "series".
func normalizeLookup(kind string, m map[string]any) searchResult {
	res := searchResult{
		Type:     kind,
		Title:    str(m, "title"),
		Year:     num(m, "year"),
		Overview: str(m, "overview"),
		Poster:   posterOf(m),
		TmdbID:   num(m, "tmdbId"),
		TvdbID:   num(m, "tvdbId"),
		ArrID:    num(m, "id"),
	}
	res.InLibrary = res.ArrID != 0
	if kind == "movie" {
		res.HasFile = flag(m, "hasFile")
	} else if stats, ok := m["statistics"].(map[string]any); ok {
		res.HasFile = num(stats, "episodeFileCount") > 0
	}
	return res
}

// posterOf returns remotePoster, else the remote URL of the first poster image.
func posterOf(m map[string]any) string {
	if p := str(m, "remotePoster"); p != "" {
		return p
	}
	images, _ := m["images"].([]any)
	for _, raw := range images {
		img, ok := raw.(map[string]any)
		if ok && str(img, "coverType") == "poster" {
			if u := str(img, "remoteUrl"); u != "" {
				return u
			}
		}
	}
	return ""
}

type addBody struct {
	Type   string `json:"type"`
	TmdbID int    `json:"tmdb_id"`
	TvdbID int    `json:"tvdb_id"`
}

func (s *Server) handleSearchAdd(w http.ResponseWriter, r *http.Request) {
	var body addBody
	if !readJSON(w, r, &body, false) {
		return
	}
	arrID, _, _, _, err := s.addToArr(r.Context(), body.Type, body.TmdbID, body.TvdbID)
	if err != nil {
		s.writeArrError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"arr_id": arrID})
}
