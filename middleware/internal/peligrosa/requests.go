// Peligrosa: trust boundary layer.
// Viewer-created media request queue. Viewers submit; admins approve or deny.
// The viewer/admin split is structural: Guard vs GuardAdmin in main.go.
// See ../../docs/PELIGROSA.md.
package peligrosa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pelicula-api/clients"
	"pelicula-api/httputil"
	"pelicula-api/internal/config"
	reporeqs "pelicula-api/internal/repo/requests"
)

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
	// Seasons is the season-level scope for a series request; omitted (not
	// null/[]) when unspecified — the request covers all seasons. Set by the
	// viewer at creation (advisory) or the admin at approve (authoritative,
	// see handleRequestApprove's absent/override/[]-clears protocol).
	Seasons []int `json:"seasons,omitempty"`
	// AvailableSeenAt is when the requester acknowledged (in-app) that this
	// request became available. A pointer so nil (unseen) is omitted from
	// the JSON response entirely — a zero time.Time would otherwise
	// serialize as "0001-01-01T00:00:00Z". See HandleRequestUnseen/
	// HandleRequestAcknowledge.
	AvailableSeenAt *time.Time `json:"available_seen_at,omitempty"`
}

// isTerminal returns true for states that are end-of-lifecycle.
func (r *MediaRequest) isTerminal() bool {
	return r.State == RequestDenied || r.State == RequestAvailable
}

// InviteExport captures the full state of an invite for backup/restore.
// Defined here so peligrosa's InsertFull method can use it, and export.go can
// reference it via peligrosa.InviteExport.
type InviteExport struct {
	Token     string     `json:"token"`
	Label     string     `json:"label,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	CreatedBy string     `json:"created_by"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	MaxUses   *int       `json:"max_uses,omitempty"`
	Uses      int        `json:"uses"`
	Revoked   bool       `json:"revoked"`
}

// RequestExport captures the full state of a media request for backup/restore.
type RequestExport struct {
	ID              string         `json:"id"`
	Type            string         `json:"type"`
	TmdbID          int            `json:"tmdb_id"`
	TvdbID          int            `json:"tvdb_id"`
	Title           string         `json:"title"`
	Year            int            `json:"year"`
	Poster          string         `json:"poster,omitempty"`
	RequestedBy     string         `json:"requested_by"`
	State           RequestState   `json:"state"`
	Reason          string         `json:"reason,omitempty"`
	ArrID           int            `json:"arr_id,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	History         []RequestEvent `json:"history"`
	Seasons         []int          `json:"seasons,omitempty"`
	AvailableSeenAt *time.Time     `json:"available_seen_at,omitempty"`
}

// RequestStore persists media requests in SQLite.
// approveMu serializes concurrent approval attempts so that exactly one
// caller can pass the pending-state gate and call the *arr fulfiller.
// The SQL-level MarkGrabbedIfPending provides a second line of defense.
type RequestStore struct {
	repo      *reporeqs.Store
	fulfiller clients.Fulfiller
	approveMu sync.Mutex
}

func NewRequestStore(repo *reporeqs.Store, fulfiller clients.Fulfiller) *RequestStore {
	return &RequestStore{repo: repo, fulfiller: fulfiller}
}

// toMediaRequest converts a repo Request to a peligrosa MediaRequest.
func toMediaRequest(r *reporeqs.Request) *MediaRequest {
	history := make([]RequestEvent, len(r.History))
	for i, ev := range r.History {
		history[i] = RequestEvent{
			At:    ev.At,
			State: RequestState(ev.State),
			Actor: ev.Actor,
			Note:  ev.Note,
		}
	}
	out := &MediaRequest{
		ID:          r.ID,
		Type:        r.Type,
		TmdbID:      r.TmdbID,
		TvdbID:      r.TvdbID,
		Title:       r.Title,
		Year:        r.Year,
		Poster:      r.Poster,
		RequestedBy: r.RequestedBy,
		State:       RequestState(r.State),
		Reason:      r.Reason,
		ArrID:       r.ArrID,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
		History:     history,
		Seasons:     r.Seasons,
	}
	if !r.AvailableSeenAt.IsZero() {
		t := r.AvailableSeenAt
		out.AvailableSeenAt = &t
	}
	return out
}

// toRepoRequest converts a peligrosa MediaRequest to a repo Request.
func toRepoRequest(m *MediaRequest) *reporeqs.Request {
	history := make([]reporeqs.Event, len(m.History))
	for i, ev := range m.History {
		history[i] = reporeqs.Event{
			RequestID: m.ID,
			At:        ev.At,
			State:     reporeqs.State(ev.State),
			Actor:     ev.Actor,
			Note:      ev.Note,
		}
	}
	out := &reporeqs.Request{
		ID:          m.ID,
		Type:        m.Type,
		TmdbID:      m.TmdbID,
		TvdbID:      m.TvdbID,
		Title:       m.Title,
		Year:        m.Year,
		Poster:      m.Poster,
		RequestedBy: m.RequestedBy,
		State:       reporeqs.State(m.State),
		Reason:      m.Reason,
		ArrID:       m.ArrID,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
		History:     history,
		Seasons:     m.Seasons,
	}
	if m.AvailableSeenAt != nil {
		out.AvailableSeenAt = *m.AvailableSeenAt
	}
	return out
}

func (s *RequestStore) All(ctx context.Context) []*MediaRequest {
	all, err := s.repo.All(ctx)
	if err != nil {
		slog.Warn("requests: All query error", "component", "requests", "error", err)
		return []*MediaRequest{}
	}
	result := make([]*MediaRequest, len(all))
	for i, r := range all {
		result[i] = toMediaRequest(r)
	}
	return result
}

// findActive returns the first non-terminal request matching type + tmdbID or
// tvdbID. Uses a targeted query (repo.ListActiveByKey) rather than fetching
// and filtering the entire requests table — this runs on every
// POST /api/pelicula/requests, so it must not scale with total request count
// (MWD-8).
func (s *RequestStore) findActive(ctx context.Context, reqType string, tmdbID, tvdbID int) *MediaRequest {
	key := tmdbID
	if reqType == "series" {
		key = tvdbID
	}
	if key == 0 {
		return nil
	}
	rows, err := s.repo.ListActiveByKey(ctx, reqType, key)
	if err != nil || len(rows) == 0 {
		return nil
	}
	return toMediaRequest(rows[0])
}

func (s *RequestStore) get(ctx context.Context, id string) *MediaRequest {
	r, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil
	}
	return toMediaRequest(r)
}

// insertEvent appends a state-transition event to request_events.
func (s *RequestStore) insertEvent(ctx context.Context, id string, ev RequestEvent) error {
	return s.repo.InsertEvent(ctx, reporeqs.Event{
		RequestID: id,
		At:        ev.At,
		State:     reporeqs.State(ev.State),
		Actor:     ev.Actor,
		Note:      ev.Note,
	})
}

// updateRequest updates mutable fields on an existing request row.
func (s *RequestStore) updateRequest(ctx context.Context, req *MediaRequest) error {
	return s.repo.Update(ctx, toRepoRequest(req))
}

// ListUnseenAvailableByUser returns user's available-but-unacknowledged
// requests, converted to MediaRequest form. Returns a non-nil empty slice on
// query error (logged) or when there are none — mirrors All's convention.
func (s *RequestStore) ListUnseenAvailableByUser(ctx context.Context, user string) []*MediaRequest {
	rows, err := s.repo.ListUnseenAvailableByUser(ctx, user)
	if err != nil {
		slog.Warn("requests: ListUnseenAvailableByUser query error", "component", "requests", "error", err)
		return []*MediaRequest{}
	}
	result := make([]*MediaRequest, len(rows))
	for i, r := range rows {
		result[i] = toMediaRequest(r)
	}
	return result
}

// MarkAvailableSeen marks user's unseen-available requests as acknowledged
// as of now — all of them, or only ids when non-empty. Ownership is
// enforced server-side by the repo's requested_by filter, not by this
// method: a foreign id in ids simply matches 0 rows.
func (s *RequestStore) MarkAvailableSeen(ctx context.Context, user string, ids []string) (int, error) {
	return s.repo.MarkAvailableSeen(ctx, user, time.Now().UTC(), ids)
}

func generateRequestID() string {
	// Use a random token suffix; no dependency on main-package generateAPIKey.
	t, err := generateToken()
	if err != nil {
		// crypto/rand failure is unrecoverable; an ID without entropy risks collisions.
		panic("peligrosa: crypto/rand unavailable: " + err.Error())
	}
	return fmt.Sprintf("req_%d_%s", time.Now().UnixMilli(), t[:6])
}

// InsertFull inserts a media request from a backup export, preserving all
// fields including the ID, timestamps, and event history. Silently succeeds
// if the ID already exists (idempotent restore).
func (s *RequestStore) InsertFull(ctx context.Context, req RequestExport) error {
	history := make([]reporeqs.Event, len(req.History))
	for i, ev := range req.History {
		history[i] = reporeqs.Event{
			RequestID: req.ID,
			At:        ev.At,
			State:     reporeqs.State(ev.State),
			Actor:     ev.Actor,
			Note:      ev.Note,
		}
	}
	r := &reporeqs.Request{
		ID:          req.ID,
		Type:        req.Type,
		TmdbID:      req.TmdbID,
		TvdbID:      req.TvdbID,
		Title:       req.Title,
		Year:        req.Year,
		Poster:      req.Poster,
		RequestedBy: req.RequestedBy,
		State:       reporeqs.State(req.State),
		Reason:      req.Reason,
		ArrID:       req.ArrID,
		CreatedAt:   req.CreatedAt,
		UpdatedAt:   req.UpdatedAt,
		History:     history,
		Seasons:     req.Seasons,
	}
	if req.AvailableSeenAt != nil {
		r.AvailableSeenAt = *req.AvailableSeenAt
	}
	return s.repo.InsertFull(ctx, r)
}

// MarkAvailable transitions a request to "available" when its content has been imported.
// Matched by tmdbID (movies) or tvdbID (series). Non-fatal if no matching request exists.
// If notify is non-nil, it's invoked after the state transition to dispatch a notification.
// Returns a non-nil error only if the DB update fails; notify errors are logged but not returned.
func (s *RequestStore) MarkAvailable(ctx context.Context, reqType string, tmdbID, tvdbID int, title string, notify func(subject, body string) error) error {
	all := s.All(ctx)
	var matched *MediaRequest
	for _, req := range all {
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
		return nil
	}

	requester := matched.RequestedBy
	matched.State = RequestAvailable
	matched.UpdatedAt = time.Now().UTC()
	ev := RequestEvent{
		At:    matched.UpdatedAt,
		State: RequestAvailable,
		Note:  "content imported",
	}
	repoEv := reporeqs.Event{
		RequestID: matched.ID,
		At:        ev.At,
		State:     reporeqs.State(ev.State),
		Note:      ev.Note,
	}
	if err := s.repo.UpdateAndInsertEvent(ctx, toRepoRequest(matched), repoEv); err != nil {
		slog.Error("failed to save request after availability update", "component", "requests", "error", err)
		return err
	}

	slog.Info("request marked available", "component", "requests", "id", matched.ID, "title", title)
	if notify != nil {
		if err := notify(title+" is now available", fmt.Sprintf("Hey %s — %q has been imported and is ready to watch.", requester, title)); err != nil {
			slog.Warn("notify failed after marking available", "component", "requests", "error", err)
		}
	}
	return nil
}

// normalizeSeasons validates the shape of a seasons parameter: nil is valid
// ("all seasons"); a non-nil empty slice is rejected (there is no "monitor
// nothing" add/request); each number must be in [0,999]; at most 100
// entries; duplicates are silently removed. Returns the deduped, sorted
// slice and an empty error string on success, or (nil, message) with message
// suitable for a 400 response body.
//
// This intentionally mirrors internal/app/search.normalizeSeasons rather
// than importing it: peligrosa is structurally decoupled from the search
// package (it depends only on the clients.Fulfiller interface — see that
// interface's doc comment), and this is a small, stable, pure helper.
func normalizeSeasons(seasons []int) ([]int, string) {
	if seasons == nil {
		return nil, ""
	}
	if len(seasons) == 0 {
		return nil, "seasons must be a non-empty array of season numbers"
	}
	if len(seasons) > 100 {
		return nil, "seasons must contain at most 100 entries"
	}
	seen := make(map[int]bool, len(seasons))
	out := make([]int, 0, len(seasons))
	for _, n := range seasons {
		if n < 0 || n > 999 {
			return nil, fmt.Sprintf("season number %d out of range (0-999)", n)
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Ints(out)
	return out, ""
}

// formatSeasonsNote renders a non-nil seasons selection as an approve-event
// Note suffix, e.g. " (seasons: 1, 2)". Returns "" for an empty slice.
func formatSeasonsNote(seasons []int) string {
	if len(seasons) == 0 {
		return ""
	}
	parts := make([]string, len(seasons))
	for i, n := range seasons {
		parts[i] = strconv.Itoa(n)
	}
	return " (seasons: " + strings.Join(parts, ", ") + ")"
}

// --- HTTP handlers ---

// HandleRequests dispatches GET (list) and POST (create) on /api/pelicula/requests.
func (p *Deps) HandleRequests(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		p.HandleRequestList(w, r)
	case http.MethodPost:
		p.HandleRequestCreate(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleRequestList returns all requests. Admins see all; viewers see only their own.
func (p *Deps) HandleRequestList(w http.ResponseWriter, r *http.Request) {
	username, role, _ := p.Auth.SessionFor(r)

	all := p.Requests.All(r.Context())
	var out []*MediaRequest
	for _, req := range all {
		if role.atLeast(RoleAdmin) || req.RequestedBy == username {
			out = append(out, req)
		}
	}
	if out == nil {
		out = []*MediaRequest{}
	}
	httputil.WriteJSON(w, out)
}

// HandleRequestCreate creates a new request from a viewer.
func (p *Deps) HandleRequestCreate(w http.ResponseWriter, r *http.Request) {
	username, _, ok := p.Auth.SessionFor(r)
	if !ok {
		httputil.WriteError(w, "unauthorized", http.StatusUnauthorized)
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
		// Seasons is the viewer's desired season-level scope for a series
		// request — advisory only. It's shape-validated here (mirroring
		// search.HandleSearchAdd's rules) but NOT checked for existence
		// against Sonarr: that requires a live lookup, and an unprivileged
		// viewer request should not trigger one. Existence is the
		// authoritative gate at approve time (handleRequestApprove).
		Seasons []int `json:"seasons"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if body.Type != "movie" && body.Type != "series" {
		httputil.WriteError(w, "type must be 'movie' or 'series'", http.StatusBadRequest)
		return
	}
	if body.Title == "" {
		httputil.WriteError(w, "title is required", http.StatusBadRequest)
		return
	}
	if body.Type == "movie" && body.TmdbID == 0 {
		httputil.WriteError(w, "tmdb_id is required for movies", http.StatusBadRequest)
		return
	}
	if body.Type == "series" && body.TvdbID == 0 {
		httputil.WriteError(w, "tvdb_id is required for series", http.StatusBadRequest)
		return
	}
	if body.Seasons != nil && body.Type != "series" {
		httputil.WriteError(w, "seasons is only valid for series", http.StatusBadRequest)
		return
	}
	seasons, seasonsErr := normalizeSeasons(body.Seasons)
	if seasonsErr != "" {
		httputil.WriteError(w, seasonsErr, http.StatusBadRequest)
		return
	}

	// Deduplicate: return existing non-terminal request for the same content.
	existing := p.Requests.findActive(r.Context(), body.Type, body.TmdbID, body.TvdbID)
	if existing != nil {
		httputil.WriteJSON(w, existing)
		return
	}

	now := time.Now().UTC()
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
		CreatedAt:   now,
		UpdatedAt:   now,
		Seasons:     seasons,
	}

	if err := p.Requests.repo.Insert(r.Context(), toRepoRequest(req)); err != nil {
		slog.Error("failed to save request", "component", "requests", "error", err)
		httputil.WriteError(w, "failed to save request", http.StatusInternalServerError)
		return
	}

	ev := RequestEvent{At: now, State: RequestPending, Actor: username}
	if err := p.Requests.insertEvent(r.Context(), req.ID, ev); err != nil {
		slog.Warn("failed to insert initial request event", "component", "requests", "error", err)
	}
	req.History = []RequestEvent{ev}

	slog.Info("request created", "component", "requests", "id", req.ID, "title", req.Title, "user", username)
	w.WriteHeader(http.StatusCreated)
	httputil.WriteJSON(w, req)
}

// HandleRequestOp dispatches approve/deny/delete on /api/pelicula/requests/{id}[/action].
func (p *Deps) HandleRequestOp(w http.ResponseWriter, r *http.Request) {
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
		httputil.WriteError(w, "request id required", http.StatusBadRequest)
		return
	}

	switch action {
	case "approve":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		actorUsername, _, _ := p.Auth.SessionFor(r)
		p.Requests.handleRequestApprove(w, r, id, actorUsername, p.notify)
	case "deny":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p.HandleRequestDeny(w, r, id)
	case "":
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p.Requests.HandleRequestDelete(w, r, id)
	default:
		httputil.WriteError(w, "unknown action: "+action, http.StatusNotFound)
	}
}

func (rs *RequestStore) handleRequestApprove(w http.ResponseWriter, r *http.Request, id, actorUsername string, notify func(string, string)) {
	// Layer 1: per-handler serialization mutex.
	// Approvals are admin-only and infrequent; global serialization is simpler
	// and safer than per-id sharding. This prevents two concurrent admins from
	// both passing the pending-state gate and both calling the *arr fulfiller.
	rs.approveMu.Lock()
	defer rs.approveMu.Unlock()

	// Read profile/root from settings env vars (set at container start from .env)
	radarrProfileID := config.IntOr("REQUESTS_RADARR_PROFILE_ID", 0)
	radarrRoot := os.Getenv("REQUESTS_RADARR_ROOT")
	sonarrProfileID := config.IntOr("REQUESTS_SONARR_PROFILE_ID", 0)
	sonarrRoot := os.Getenv("REQUESTS_SONARR_ROOT")

	req := rs.get(r.Context(), id)
	if req == nil {
		httputil.WriteError(w, "request not found", http.StatusNotFound)
		return
	}
	if req.State != RequestPending {
		httputil.WriteError(w, fmt.Sprintf("request is %s, not pending", req.State), http.StatusConflict)
		return
	}

	// Optional body: {"seasons": [...]} — an admin override of the series
	// request's stored season scope. Protocol (deliberately asymmetric with
	// search/add and request-create, which have no "stored" value to fall
	// back to — see API.md):
	//   - absent body / JSON null → nil here → use the request's stored scope
	//   - non-empty array → shape-validated and used as the final scope
	//   - explicit []       → admin cleared the scope → all seasons (nil)
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body struct {
		Seasons *[]int `json:"seasons"`
	}
	if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil && decodeErr != io.EOF {
		httputil.WriteError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	finalSeasons := req.Seasons
	if body.Seasons != nil {
		if req.Type != "series" {
			httputil.WriteError(w, "seasons is only valid for series", http.StatusBadRequest)
			return
		}
		override := *body.Seasons
		if len(override) == 0 {
			finalSeasons = nil // admin cleared the scope → all seasons
		} else {
			normalized, seasonsErr := normalizeSeasons(override)
			if seasonsErr != "" {
				httputil.WriteError(w, seasonsErr, http.StatusBadRequest)
				return
			}
			finalSeasons = normalized
		}
	}

	// Snapshot what we need for the *arr call
	reqType := req.Type
	tmdbID := req.TmdbID
	tvdbID := req.TvdbID
	title := req.Title
	requester := req.RequestedBy

	// Add to *arr (while holding the mutex — serializes fulfiller calls)
	var arrID int
	var addErr error
	switch reqType {
	case "movie":
		arrID, addErr = rs.fulfiller.AddMovie(r.Context(), tmdbID, radarrProfileID, radarrRoot)
	case "series":
		arrID, addErr = rs.fulfiller.AddSeries(r.Context(), tvdbID, sonarrProfileID, sonarrRoot, finalSeasons)
	default:
		httputil.WriteError(w, "unknown request type", http.StatusInternalServerError)
		return
	}
	if addErr != nil {
		if errors.Is(addErr, clients.ErrInvalidSeasons) {
			// Request stays pending — the admin fixes the override and retries.
			httputil.WriteError(w, addErr.Error(), http.StatusBadRequest)
			return
		}
		slog.Error("failed to add content to *arr", "component", "requests", "id", id, "error", addErr)
		httputil.WriteError(w, "failed to add to *arr: "+addErr.Error(), http.StatusBadGateway)
		return
	}

	// Layer 2: atomic conditional UPDATE — only transitions pending → grabbed.
	// This is a second-line defense: if the mutex ever fails to serialize (e.g.
	// separate process instances), the SQL WHERE state=? ensures only one writer wins.
	// finalSeasons is also persisted here — the admin's final season selection,
	// which may differ from what the viewer originally requested.
	now := time.Now().UTC()
	ok, err := rs.repo.MarkGrabbedIfPending(r.Context(), id, arrID, finalSeasons, now)
	if err != nil {
		slog.Error("failed to save request after approve", "component", "requests", "error", err)
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		// The request was concurrently modified out of pending state (denied, deleted, etc.)
		// NOTE: The *arr AddMovie/AddSeries call above already SUCCEEDED, so the media
		// has been added to the downstream service but the requests row was not transitioned
		// to grabbed. This is an orphan condition that requires manual reconciliation.
		slog.Warn("approve: request left pending between gate and update; *arr add succeeded but row not transitioned",
			"component", "requests", "id", id, "arr_id", arrID)
		httputil.WriteError(w, "request was modified concurrently", http.StatusConflict)
		return
	}

	// Persist the audit event BEFORE attempting the re-fetch — the history record
	// must be durable regardless of whether we can build a rich response body.
	// A non-nil finalSeasons appends a scope detail, e.g. " (seasons: 1, 2)".
	ev := RequestEvent{
		At:    now,
		State: RequestGrabbed,
		Actor: actorUsername,
		Note:  "approved and added to *arr" + formatSeasonsNote(finalSeasons),
	}
	if err := rs.insertEvent(r.Context(), id, ev); err != nil {
		slog.Warn("failed to insert approve event", "component", "requests", "error", err)
	}

	slog.Info("request approved", "component", "requests", "id", id, "title", title, "arr_id", arrID)
	if notify != nil {
		go notify("Request approved: "+title, fmt.Sprintf("%s requested %q — it's been added to the download queue.", requester, title))
	}

	// Re-fetch the updated row for a rich response body. The state transition has
	// already succeeded at this point; a re-fetch failure must NOT surface as an error
	// to the caller (a retry would hit the 409 path and confuse the dashboard UI).
	refreshed := rs.get(r.Context(), id)
	if refreshed == nil {
		slog.Warn("approve: state transition succeeded but row could not be re-fetched for response",
			"component", "requests", "id", id, "arr_id", arrID)
		httputil.WriteJSON(w, map[string]any{"id": id, "state": string(RequestGrabbed), "arr_id": arrID})
		return
	}

	httputil.WriteJSON(w, refreshed)
}

// HandleRequestDeny denies a pending media request.
func (p *Deps) HandleRequestDeny(w http.ResponseWriter, r *http.Request, id string) {
	actorUsername, _, _ := p.Auth.SessionFor(r)

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body struct {
		Reason string `json:"reason"`
	}
	json.NewDecoder(r.Body).Decode(&body) // best-effort; reason is optional

	req := p.Requests.get(r.Context(), id)
	if req == nil {
		httputil.WriteError(w, "request not found", http.StatusNotFound)
		return
	}
	if req.isTerminal() {
		httputil.WriteError(w, fmt.Sprintf("request is already %s", req.State), http.StatusConflict)
		return
	}

	title := req.Title
	requester := req.RequestedBy

	req.State = RequestDenied
	req.Reason = body.Reason
	req.UpdatedAt = time.Now().UTC()
	ev := RequestEvent{
		At:    req.UpdatedAt,
		State: RequestDenied,
		Actor: actorUsername,
		Note:  body.Reason,
	}
	repoEv := reporeqs.Event{
		RequestID: req.ID,
		At:        ev.At,
		State:     reporeqs.State(ev.State),
		Actor:     ev.Actor,
		Note:      ev.Note,
	}
	if err := p.Requests.repo.UpdateAndInsertEvent(r.Context(), toRepoRequest(req), repoEv); err != nil {
		slog.Error("failed to save request after deny", "component", "requests", "error", err)
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	req.History = append(req.History, ev)

	slog.Info("request denied", "component", "requests", "id", id, "title", title)
	if p.Notify != nil {
		msg := fmt.Sprintf("Your request for %q was not approved.", title)
		if body.Reason != "" {
			msg += " Reason: " + body.Reason
		}
		go p.Notify("Request denied: "+title, requester+" — "+msg)
	}

	httputil.WriteJSON(w, req)
}

// HandleRequestDelete hard-deletes a media request record.
func (rs *RequestStore) HandleRequestDelete(w http.ResponseWriter, r *http.Request, id string) {
	if err := rs.repo.Delete(r.Context(), id); err == reporeqs.ErrNotFound {
		httputil.WriteError(w, "request not found", http.StatusNotFound)
		return
	} else if err != nil {
		slog.Error("failed to delete request", "component", "requests", "error", err)
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	httputil.WriteJSON(w, map[string]string{"status": "deleted"})
}

// --- Availability notifications (viewer-scoped) ---
//
// A viewer's request eventually flips to "available" (MarkAvailable, above)
// and fires an Apprise notification to the operator's global channel — but
// the requester themselves gets nothing in-app. These two endpoints close
// that gap: the dashboard polls HandleRequestUnseen every 15s for a small
// badge/toast payload, and calls HandleRequestAcknowledge to clear it once
// the viewer has looked. Both are registered in routes.go as method+exact-path
// patterns so they win Go's ServeMux precedence over the admin-gated
// "/api/pelicula/requests/" subtree.

// unseenRequestItem is the trimmed shape returned by HandleRequestUnseen —
// polled every 15s, so the payload is kept deliberately small (no history,
// no reason, no seasons).
type unseenRequestItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Type   string `json:"type"`
	Year   int    `json:"year"`
	Poster string `json:"poster,omitempty"`
}

// HandleRequestUnseen returns the count and trimmed detail of the session
// user's available-but-unacknowledged requests. GET only (enforced by the
// "GET /api/pelicula/requests/unseen" mux pattern in routes.go).
func (p *Deps) HandleRequestUnseen(w http.ResponseWriter, r *http.Request) {
	username, _, ok := p.Auth.SessionFor(r)
	if !ok {
		httputil.WriteError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	rows := p.Requests.ListUnseenAvailableByUser(r.Context(), username)
	items := make([]unseenRequestItem, len(rows))
	for i, req := range rows {
		items[i] = unseenRequestItem{
			ID:     req.ID,
			Title:  req.Title,
			Type:   req.Type,
			Year:   req.Year,
			Poster: req.Poster,
		}
	}
	httputil.WriteJSON(w, map[string]any{"count": len(items), "items": items})
}

// HandleRequestAcknowledge marks the session user's unseen-available
// requests as seen — all of them, or an optional {"ids": [...]} subset.
// POST only (enforced by the "POST /api/pelicula/requests/acknowledge" mux
// pattern in routes.go, but also checked here defensively since this
// handler could in principle be wired directly by a future caller).
func (p *Deps) HandleRequestAcknowledge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	username, _, ok := p.Auth.SessionFor(r)
	if !ok {
		httputil.WriteError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body struct {
		IDs []string `json:"ids"`
	}
	json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck — best-effort; empty body = "acknowledge all"

	n, err := p.Requests.MarkAvailableSeen(r.Context(), username, body.IDs)
	if err != nil {
		slog.Error("failed to acknowledge requests", "component", "requests", "error", err)
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}

	slog.Info("requests acknowledged", "component", "requests", "user", username, "n", n)
	httputil.WriteJSON(w, map[string]any{"acknowledged": n})
}
