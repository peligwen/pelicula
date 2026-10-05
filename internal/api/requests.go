package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"pelicula/internal/store"
)

const (
	maxTitleLen  = 300
	maxPosterLen = 1000
	maxNoteLen   = 500
)

type createRequestBody struct {
	Type   string `json:"type"`
	TmdbID int    `json:"tmdb_id"`
	TvdbID int    `json:"tvdb_id"`
	Title  string `json:"title"`
	Year   int    `json:"year"`
	Poster string `json:"poster"`
}

func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request) {
	sess := s.requireUser(w, r)
	if sess == nil {
		return
	}
	owner := sess.Username // viewers only ever see their own
	if sess.Role.AtLeast(store.RoleManager) {
		owner = ""
	}
	reqs, err := s.Store.ListRequests(r.Context(), owner)
	if err != nil {
		s.log().Error("list requests", "err", err)
		writeError(w, http.StatusInternalServerError, "could not list requests")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": reqs})
}

func (s *Server) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	sess := s.requireUser(w, r)
	if sess == nil {
		return
	}
	var body createRequestBody
	if !readJSON(w, r, &body, false) {
		return
	}
	body.Title = strings.TrimSpace(body.Title)
	switch {
	case body.Type != "movie" && body.Type != "series":
		writeError(w, http.StatusBadRequest, "type must be 'movie' or 'series'")
		return
	case body.Type == "movie" && body.TmdbID <= 0:
		writeError(w, http.StatusBadRequest, "tmdb_id is required for movies")
		return
	case body.Type == "series" && body.TvdbID <= 0:
		writeError(w, http.StatusBadRequest, "tvdb_id is required for series")
		return
	case body.Title == "" || len(body.Title) > maxTitleLen:
		writeError(w, http.StatusBadRequest, "title is required (max 300 characters)")
		return
	case body.Poster != "" && (len(body.Poster) > maxPosterLen ||
		!(strings.HasPrefix(body.Poster, "https://") || strings.HasPrefix(body.Poster, "http://"))):
		writeError(w, http.StatusBadRequest, "poster must be an http(s) URL")
		return
	}

	ctx := r.Context()
	existing, err := s.Store.FindOpenRequest(ctx, body.Type, body.TmdbID, body.TvdbID)
	if err != nil {
		s.log().Error("find open request", "err", err)
		writeError(w, http.StatusInternalServerError, "could not check existing requests")
		return
	}
	if existing != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "already requested", "request": existing})
		return
	}

	req := &store.Request{
		MediaType:   body.Type,
		TmdbID:      body.TmdbID,
		TvdbID:      body.TvdbID,
		Title:       body.Title,
		Year:        body.Year,
		Poster:      body.Poster,
		RequestedBy: sess.Username,
		Status:      store.RequestPending,
	}
	if s.Store.BoolSetting(ctx, store.SettingAutoApprove) {
		// If *arr is down the request still lands as pending so a manager can
		// approve it later; losing the request would be worse.
		arrID, _, _, _, err := s.addToArr(ctx, req.MediaType, req.TmdbID, req.TvdbID)
		if err != nil {
			s.log().Warn("auto-approve failed, leaving request pending", "title", req.Title, "err", err)
		} else {
			req.Status = store.RequestApproved
			req.ArrID = arrID
			req.DecidedBy = "auto"
		}
	}
	if err := s.Store.CreateRequest(ctx, req); err != nil {
		s.log().Error("create request", "err", err)
		writeError(w, http.StatusInternalServerError, "could not save request")
		return
	}
	writeJSON(w, http.StatusCreated, req)
}

// loadRequest reads the {id} path value and fetches that request, writing the
// error response itself on failure.
func (s *Server) loadRequest(w http.ResponseWriter, r *http.Request) (*store.Request, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid request id")
		return nil, false
	}
	req, err := s.Store.GetRequest(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "request not found")
		return nil, false
	}
	if err != nil {
		s.log().Error("get request", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "could not load request")
		return nil, false
	}
	return req, true
}

func (s *Server) handleApproveRequest(w http.ResponseWriter, r *http.Request) {
	sess := s.requireUser(w, r)
	if sess == nil {
		return
	}
	req, ok := s.loadRequest(w, r)
	if !ok {
		return
	}
	// A declined request may be reconsidered; one that is already approved
	// or fulfilled may not be approved twice.
	if req.Status != store.RequestPending && req.Status != store.RequestDeclined {
		writeError(w, http.StatusConflict, "request is already "+string(req.Status))
		return
	}
	arrID, _, _, _, err := s.addToArr(r.Context(), req.MediaType, req.TmdbID, req.TvdbID)
	if err != nil {
		s.writeArrError(w, err)
		return
	}
	if err := s.Store.UpdateRequestStatus(r.Context(), req.ID, store.RequestApproved, sess.Username, "", arrID); err != nil {
		s.log().Error("approve request", "id", req.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "could not update request")
		return
	}
	s.respondRequest(w, r, req.ID)
}

func (s *Server) handleDeclineRequest(w http.ResponseWriter, r *http.Request) {
	sess := s.requireUser(w, r)
	if sess == nil {
		return
	}
	req, ok := s.loadRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if !readJSON(w, r, &body, true) {
		return
	}
	if len(body.Note) > maxNoteLen {
		writeError(w, http.StatusBadRequest, "note is too long (max 500 characters)")
		return
	}
	if req.Status != store.RequestPending {
		writeError(w, http.StatusConflict, "request is already "+string(req.Status))
		return
	}
	if err := s.Store.UpdateRequestStatus(r.Context(), req.ID, store.RequestDeclined, sess.Username, strings.TrimSpace(body.Note), 0); err != nil {
		s.log().Error("decline request", "id", req.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "could not update request")
		return
	}
	s.respondRequest(w, r, req.ID)
}

// respondRequest re-reads a request and writes it as the response.
func (s *Server) respondRequest(w http.ResponseWriter, r *http.Request, id int64) {
	req, err := s.Store.GetRequest(r.Context(), id)
	if err != nil {
		s.log().Error("reload request", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "could not load request")
		return
	}
	writeJSON(w, http.StatusOK, req)
}
