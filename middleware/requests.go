package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// requestStore is the package-level request store, initialised in main.
var requestStore *RequestStore

// RequestState represents the lifecycle state of a media request.
type RequestState string

const (
	RequestPending   RequestState = "pending"
	RequestApproved  RequestState = "approved"
	RequestDenied    RequestState = "denied"
	RequestGrabbed   RequestState = "grabbed"
	RequestAvailable RequestState = "available"
)

// RequestEvent records a single state transition for audit purposes.
type RequestEvent struct {
	At    time.Time    `json:"at"`
	State RequestState `json:"state"`
	Actor string       `json:"actor,omitempty"`
	Note  string       `json:"note,omitempty"`
}

// MediaRequest is a viewer's request for a movie or TV series.
type MediaRequest struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`    // "movie" | "series"
	TmdbID      int            `json:"tmdb_id"` // movies (primary); also present on many series
	TvdbID      int            `json:"tvdb_id"` // series (primary for Sonarr)
	Title       string         `json:"title"`
	Year        int            `json:"year"`
	Poster      string         `json:"poster,omitempty"`
	RequestedBy string         `json:"requested_by"`
	State       RequestState   `json:"state"`
	Reason      string         `json:"reason,omitempty"` // denial reason or status note
	ArrID       int            `json:"arr_id,omitempty"` // *arr internal id after approval
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	History     []RequestEvent `json:"history"`
}

// isTerminal returns true for states that are end-of-lifecycle.
func (r *MediaRequest) isTerminal() bool {
	return r.State == RequestDenied || r.State == RequestAvailable
}

// RequestStore persists media requests to a JSON file under a single mutex.
type RequestStore struct {
	path     string
	mu       sync.Mutex
	requests []*MediaRequest
}

func NewRequestStore(path string) *RequestStore {
	s := &RequestStore{path: path}
	if err := s.load(); err != nil {
		slog.Warn("could not load requests", "component", "requests", "path", path, "error", err)
	}
	return s
}

func (s *RequestStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, &s.requests)
}

func (s *RequestStore) save() error {
	data, err := json.MarshalIndent(s.requests, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

func (s *RequestStore) all() []*MediaRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*MediaRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

// findActive returns the first non-terminal request matching type + tmdbID or tvdbID.
func (s *RequestStore) findActive(reqType string, tmdbID, tvdbID int) *MediaRequest {
	for _, r := range s.requests {
		if r.Type != reqType || r.isTerminal() {
			continue
		}
		if reqType == "movie" && tmdbID != 0 && r.TmdbID == tmdbID {
			return r
		}
		if reqType == "series" && tvdbID != 0 && r.TvdbID == tvdbID {
			return r
		}
	}
	return nil
}

func (s *RequestStore) get(id string) *MediaRequest {
	for _, r := range s.requests {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func generateRequestID() string {
	return fmt.Sprintf("req_%d_%s", time.Now().UnixMilli(), generateAPIKey()[:6])
}

// --- HTTP handlers ---

// handleRequests dispatches GET (list) and POST (create) on /api/pelicula/requests.
func handleRequests(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		handleRequestList(w, r)
	case http.MethodPost:
		handleRequestCreate(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRequestList returns all requests. Admins see all; viewers see only their own.
func handleRequestList(w http.ResponseWriter, r *http.Request) {
	username, role, _ := authMiddleware.SessionFor(r)

	all := requestStore.all()
	var out []*MediaRequest
	for _, req := range all {
		if role.atLeast(RoleAdmin) || req.RequestedBy == username {
			out = append(out, req)
		}
	}
	if out == nil {
		out = []*MediaRequest{}
	}
	writeJSON(w, out)
}

// handleRequestCreate creates a new request from a viewer.
func handleRequestCreate(w http.ResponseWriter, r *http.Request) {
	username, _, ok := authMiddleware.SessionFor(r)
	if !ok && authMiddleware.mode != "off" {
		writeError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		Type   string `json:"type"`
		TmdbID int    `json:"tmdb_id"`
		TvdbID int    `json:"tvdb_id"`
		Title  string `json:"title"`
		Year   int    `json:"year"`
		Poster string `json:"poster"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if body.Type != "movie" && body.Type != "series" {
		writeError(w, "type must be 'movie' or 'series'", http.StatusBadRequest)
		return
	}
	if body.Title == "" {
		writeError(w, "title is required", http.StatusBadRequest)
		return
	}
	if body.Type == "movie" && body.TmdbID == 0 {
		writeError(w, "tmdb_id is required for movies", http.StatusBadRequest)
		return
	}
	if body.Type == "series" && body.TvdbID == 0 {
		writeError(w, "tvdb_id is required for series", http.StatusBadRequest)
		return
	}

	requestStore.mu.Lock()
	defer requestStore.mu.Unlock()

	// Deduplicate: return existing non-terminal request for the same content.
	existing := requestStore.findActive(body.Type, body.TmdbID, body.TvdbID)
	if existing != nil {
		writeJSON(w, existing)
		return
	}

	req := &MediaRequest{
		ID:          generateRequestID(),
		Type:        body.Type,
		TmdbID:      body.TmdbID,
		TvdbID:      body.TvdbID,
		Title:       body.Title,
		Year:        body.Year,
		Poster:      body.Poster,
		RequestedBy: username,
		State:       RequestPending,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		History: []RequestEvent{
			{At: time.Now(), State: RequestPending, Actor: username},
		},
	}
	requestStore.requests = append(requestStore.requests, req)
	if err := requestStore.save(); err != nil {
		slog.Error("failed to save request", "component", "requests", "error", err)
		writeError(w, "failed to save request", http.StatusInternalServerError)
		return
	}

	slog.Info("request created", "component", "requests", "id", req.ID, "title", req.Title, "user", username)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, req)
}

// handleRequestOp dispatches approve/deny/delete on /api/pelicula/requests/{id}[/action].
func handleRequestOp(w http.ResponseWriter, r *http.Request) {
	// Path: /api/pelicula/requests/{id} or /api/pelicula/requests/{id}/approve|deny
	path := strings.TrimPrefix(r.URL.Path, "/api/pelicula/requests/")
	path = strings.TrimSuffix(path, "/")

	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	if id == "" {
		writeError(w, "request id required", http.StatusBadRequest)
		return
	}

	switch action {
	case "approve":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleRequestApprove(w, r, id)
	case "deny":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleRequestDeny(w, r, id)
	case "":
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleRequestDelete(w, r, id)
	default:
		writeError(w, "unknown action: "+action, http.StatusNotFound)
	}
}

func handleRequestApprove(w http.ResponseWriter, r *http.Request, id string) {
	actorUsername, _, _ := authMiddleware.SessionFor(r)

	// Read profile/root from settings env vars (set at container start from .env)
	radarrProfileID := envIntOr("REQUESTS_RADARR_PROFILE_ID", 0)
	radarrRoot := os.Getenv("REQUESTS_RADARR_ROOT")
	sonarrProfileID := envIntOr("REQUESTS_SONARR_PROFILE_ID", 0)
	sonarrRoot := os.Getenv("REQUESTS_SONARR_ROOT")

	requestStore.mu.Lock()
	req := requestStore.get(id)
	if req == nil {
		requestStore.mu.Unlock()
		writeError(w, "request not found", http.StatusNotFound)
		return
	}
	if req.State != RequestPending {
		requestStore.mu.Unlock()
		writeError(w, fmt.Sprintf("request is %s, not pending", req.State), http.StatusConflict)
		return
	}

	// Snapshot what we need before releasing the lock for the *arr call
	reqType := req.Type
	tmdbID := req.TmdbID
	tvdbID := req.TvdbID
	title := req.Title
	requester := req.RequestedBy
	requestStore.mu.Unlock()

	// Add to *arr (outside lock — network call)
	var arrID int
	var addErr error
	switch reqType {
	case "movie":
		arrID, addErr = addMovieInternal(tmdbID, radarrProfileID, radarrRoot)
	case "series":
		arrID, addErr = addSeriesInternal(tvdbID, sonarrProfileID, sonarrRoot)
	default:
		writeError(w, "unknown request type", http.StatusInternalServerError)
		return
	}
	if addErr != nil {
		slog.Error("failed to add content to *arr", "component", "requests", "id", id, "error", addErr)
		writeError(w, "failed to add to *arr: "+addErr.Error(), http.StatusBadGateway)
		return
	}

	// Update state back under lock
	requestStore.mu.Lock()
	req = requestStore.get(id)
	if req == nil {
		requestStore.mu.Unlock()
		writeError(w, "request not found", http.StatusNotFound)
		return
	}
	req.State = RequestGrabbed
	req.ArrID = arrID
	req.UpdatedAt = time.Now()
	req.History = append(req.History, RequestEvent{
		At:    time.Now(),
		State: RequestGrabbed,
		Actor: actorUsername,
		Note:  "approved and added to *arr",
	})
	if err := requestStore.save(); err != nil {
		requestStore.mu.Unlock()
		slog.Error("failed to save request after approve", "component", "requests", "error", err)
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := *req
	requestStore.mu.Unlock()

	slog.Info("request approved", "component", "requests", "id", id, "title", title, "arr_id", arrID)
	go notifyApprise("Request approved: "+title, fmt.Sprintf("%s requested %q — it's been added to the download queue.", requester, title))

	writeJSON(w, &out)
}

func handleRequestDeny(w http.ResponseWriter, r *http.Request, id string) {
	actorUsername, _, _ := authMiddleware.SessionFor(r)

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body struct {
		Reason string `json:"reason"`
	}
	json.NewDecoder(r.Body).Decode(&body) // best-effort; reason is optional

	requestStore.mu.Lock()
	req := requestStore.get(id)
	if req == nil {
		requestStore.mu.Unlock()
		writeError(w, "request not found", http.StatusNotFound)
		return
	}
	if req.isTerminal() {
		requestStore.mu.Unlock()
		writeError(w, fmt.Sprintf("request is already %s", req.State), http.StatusConflict)
		return
	}

	title := req.Title
	requester := req.RequestedBy

	req.State = RequestDenied
	req.Reason = body.Reason
	req.UpdatedAt = time.Now()
	req.History = append(req.History, RequestEvent{
		At:    time.Now(),
		State: RequestDenied,
		Actor: actorUsername,
		Note:  body.Reason,
	})
	if err := requestStore.save(); err != nil {
		requestStore.mu.Unlock()
		slog.Error("failed to save request after deny", "component", "requests", "error", err)
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := *req
	requestStore.mu.Unlock()

	slog.Info("request denied", "component", "requests", "id", id, "title", title)
	msg := fmt.Sprintf("Your request for %q was not approved.", title)
	if body.Reason != "" {
		msg += " Reason: " + body.Reason
	}
	go notifyApprise("Request denied: "+title, requester+" — "+msg)

	writeJSON(w, &out)
}

func handleRequestDelete(w http.ResponseWriter, r *http.Request, id string) {
	requestStore.mu.Lock()
	defer requestStore.mu.Unlock()

	idx := -1
	for i, req := range requestStore.requests {
		if req.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		writeError(w, "request not found", http.StatusNotFound)
		return
	}
	requestStore.requests = append(requestStore.requests[:idx], requestStore.requests[idx+1:]...)
	if err := requestStore.save(); err != nil {
		slog.Error("failed to save requests after delete", "component", "requests", "error", err)
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

// MarkRequestAvailable transitions a request to "available" when its content has been imported.
// Matched by tmdbID (movies) or tvdbID (series). Non-fatal if no matching request exists.
func MarkRequestAvailable(reqType string, tmdbID, tvdbID int, title string) {
	requestStore.mu.Lock()
	defer requestStore.mu.Unlock()

	var matched *MediaRequest
	for _, req := range requestStore.requests {
		if req.isTerminal() || req.State == RequestAvailable {
			continue
		}
		if reqType == "movie" && tmdbID != 0 && req.TmdbID == tmdbID {
			matched = req
			break
		}
		if reqType == "series" && tvdbID != 0 && req.TvdbID == tvdbID {
			matched = req
			break
		}
	}
	if matched == nil {
		return
	}

	requester := matched.RequestedBy
	matched.State = RequestAvailable
	matched.UpdatedAt = time.Now()
	matched.History = append(matched.History, RequestEvent{
		At:    time.Now(),
		State: RequestAvailable,
		Note:  "content imported",
	})
	if err := requestStore.save(); err != nil {
		slog.Error("failed to save request after availability update", "component", "requests", "error", err)
		return
	}
	slog.Info("request marked available", "component", "requests", "id", matched.ID, "title", title)
	go notifyApprise(title+" is now available", fmt.Sprintf("Hey %s — %q has been imported and is ready to watch.", requester, title))
}

// envIntOr reads an env var as an integer, returning fallback on parse error or if unset.
func envIntOr(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return fallback
	}
	return n
}
