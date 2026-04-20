package library

import (
	"fmt"
	"net/http"
	"strconv"

	"pelicula-api/httputil"
)

// HandleSuggestPath handles GET /api/pelicula/library/suggest-path.
// Returns the directory path where a file for the given title would land in
// the library, using the same root-folder and naming logic as scan/apply.
//
// Query parameters:
//
//	type  — "movie" or "series" (required)
//	title — media title (required)
//	year  — release year as integer (optional)
//	season — season number for series (optional)
func (h *Handler) HandleSuggestPath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()

	mediaType := q.Get("type")
	if mediaType != "movie" && mediaType != "series" {
		httputil.WriteError(w, `type must be "movie" or "series"`, http.StatusBadRequest)
		return
	}

	title := q.Get("title")
	if title == "" {
		httputil.WriteError(w, "title is required", http.StatusBadRequest)
		return
	}

	year := 0
	if raw := q.Get("year"); raw != "" {
		var err error
		year, err = strconv.Atoi(raw)
		if err != nil || year < 0 {
			httputil.WriteError(w, "year must be a non-negative integer", http.StatusBadRequest)
			return
		}
	}

	season := 0
	if raw := q.Get("season"); raw != "" {
		var err error
		season, err = strconv.Atoi(raw)
		if err != nil || season < 0 {
			httputil.WriteError(w, "season must be a non-negative integer", http.StatusBadRequest)
			return
		}
	}

	var path string
	switch mediaType {
	case "movie":
		root := h.FirstLibraryPath("radarr", "/media/movies")
		folder := title
		if year > 0 {
			folder = fmt.Sprintf("%s (%d)", title, year)
		}
		path = root + "/" + folder
	case "series":
		root := h.FirstLibraryPath("sonarr", "/media/tv")
		if season > 0 {
			path = fmt.Sprintf("%s/%s/Season %02d", root, title, season)
		} else {
			path = fmt.Sprintf("%s/%s", root, title)
		}
	}

	httputil.WriteJSON(w, map[string]string{"path": path})
}
