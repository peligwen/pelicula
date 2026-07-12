// Package journey implements the per-title journey aggregation endpoint:
// GET /api/pelicula/journey renders one title's progress through the
// canonical six-stage rail (requested → approved → searching → downloading →
// processing → available) by aggregating the *arr library cache, *arr queue
// records, qBittorrent torrents, Procula jobs, the request queue, and the
// catalog store.
//
// Upstream failures degrade rather than fail: the endpoint still returns 200
// with stages inferred from the remaining signals and a "degraded" list
// naming the upstreams that could not be reached.
package journey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"pelicula-api/httputil"
	"pelicula-api/internal/app/catalog"
	arr "pelicula-api/internal/clients/arr"
	qbt "pelicula-api/internal/clients/qbt"
	repocatalog "pelicula-api/internal/repo/catalog"
	reporeqs "pelicula-api/internal/repo/requests"
)

// Svc is the subset of services.Clients that the journey package needs —
// same shape as downloads.Svc.
type Svc interface {
	Keys() (sonarr, radarr, prowlarr string)
	SonarrClient() *arr.Client
	RadarrClient() *arr.Client
	QbtClient() *qbt.Client
}

// ProculaClient is the slice of the Procula typed client journey needs.
// Satisfied by *proculaclient.Client.
type ProculaClient interface {
	ListJobs(ctx context.Context) ([]byte, error)
}

// snapshotTTL bounds the per-upstream fan-out: at most one fetch per upstream
// per 10s window regardless of how many journey drawers/cards are open —
// modeled on network.Handler's cachedResponse (same TTL).
const snapshotTTL = 10 * time.Second

// snapCache is a single-value TTL cache for one upstream snapshot. Errors
// are cached alongside values so a failing upstream is also only probed once
// per window (the 4-calls-per-10s worst case holds on both paths). The mutex
// is held across the fetch so a concurrent miss performs exactly one call.
type snapCache[T any] struct {
	mu        sync.Mutex
	val       T
	err       error
	fetchedAt time.Time
}

func (c *snapCache[T]) get(now time.Time, fetch func() (T, error)) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.fetchedAt.IsZero() && now.Sub(c.fetchedAt) < snapshotTTL {
		return c.val, c.err
	}
	c.val, c.err = fetch()
	// A caller-cancelled request context is not an upstream fact — caching it
	// would show every other viewer a degraded upstream for the rest of the
	// window just because one client disconnected mid-fetch. Leave fetchedAt
	// zeroed so the next caller retries immediately.
	if errors.Is(c.err, context.Canceled) {
		c.fetchedAt = time.Time{}
		return c.val, c.err
	}
	c.fetchedAt = now
	return c.val, c.err
}

// Handler serves GET /api/pelicula/journey.
type Handler struct {
	Svc      Svc
	ArrCache *catalog.CatalogCache // shared 2-min full-library cache (bootstrap's arrCatalogCache)
	Procula  ProculaClient
	Requests *reporeqs.Store
	Catalog  *repocatalog.Store

	// SessionFor resolves the caller's identity for viewer scoping. Bootstrap
	// builds it from auth.SessionFor; isAdmin means peligrosa.RoleAdmin
	// (managers are scoped like viewers, matching HandleRequestList).
	SessionFor func(r *http.Request) (username string, isAdmin bool)

	// now is injectable for tests; production code leaves it nil (falls back
	// to time.Now) — same seam as search.Handler.
	now func() time.Time

	radarrQueue snapCache[[]map[string]any]
	sonarrQueue snapCache[[]map[string]any]
	torrents    snapCache[[]qbt.Torrent]
	proculaJobs snapCache[[]byte]
}

func (h *Handler) timeNow() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// ── Response types ────────────────────────────────────────────────────────────

// Canonical stage names, in rail order. All six are always present in a
// response, in exactly this order.
const (
	stageRequested   = "requested"
	stageApproved    = "approved"
	stageSearching   = "searching"
	stageDownloading = "downloading"
	stageProcessing  = "processing"
	stageAvailable   = "available"
)

// Stage statuses.
const (
	statusDone    = "done"
	statusActive  = "active"
	statusPending = "pending"
	statusSkipped = "skipped"
)

// Stage is one entry in the six-stage rail.
type Stage struct {
	Stage  string `json:"stage"`
	Status string `json:"status"`
	// At/By are populated only on request-derived stages (requested/approved)
	// visible to the request's owner or an admin.
	At string `json:"at,omitempty"` // RFC3339
	By string `json:"by,omitempty"`
	// Progress is 0..1 on an active downloading/processing stage.
	Progress *float64 `json:"progress,omitempty"`
	// Detail is a short human-readable hint: a qbt state ("stalledDL"), a
	// procula stage ("transcode"), or series counts ("12/20 episodes").
	Detail string `json:"detail,omitempty"`
	// ETA is seconds remaining, from qBittorrent (fallback: *arr timeleft).
	ETA int64 `json:"eta,omitempty"`
}

// RequestEvent mirrors one request_events row in the response.
type RequestEvent struct {
	At    string `json:"at"` // RFC3339
	State string `json:"state"`
	Actor string `json:"actor,omitempty"`
	Note  string `json:"note,omitempty"`
}

// RequestInfo is the request object included only for the request's owner or
// an admin — never for other viewers (they get skipped requested/approved
// stages instead; see viewer scoping in HandleJourney).
type RequestInfo struct {
	ID          string         `json:"id"`
	State       string         `json:"state"`
	RequestedBy string         `json:"requested_by"`
	History     []RequestEvent `json:"history"`
}

// Response is the journey endpoint's 200 body.
type Response struct {
	Type         string       `json:"type"` // "movie" | "series"
	Title        string       `json:"title"`
	Year         int          `json:"year"`
	TmdbID       int          `json:"tmdb_id"`
	TvdbID       int          `json:"tvdb_id"`
	ArrType      string       `json:"arr_type"` // "radarr" | "sonarr" | ""
	ArrID        int          `json:"arr_id"`
	Monitored    bool         `json:"monitored"`
	HasFile      bool         `json:"has_file"` // series: episodeFileCount > 0
	CurrentStage string       `json:"current_stage"`
	Progress     *float64     `json:"progress,omitempty"`
	Stages       []Stage      `json:"stages"`
	Request      *RequestInfo `json:"request,omitempty"`
	Degraded     []string     `json:"degraded,omitempty"`
}

// ── Query resolution ──────────────────────────────────────────────────────────

// query is the resolved, canonical form of the two accepted query-param shapes.
type query struct {
	mediaType string // "movie" | "series"
	key       int    // tmdb_id (movie) or tvdb_id (series); 0 = unknown
	arrType   string // "radarr" | "sonarr"
	arrID     int    // 0 = unknown (form 1 until the *arr item resolves it)
}

// parseQuery validates the two accepted key forms:
//  1. type=movie|series + tmdb_id (movies) / tvdb_id (series)
//  2. arr_type=radarr|sonarr + arr_id
//
// Returns a non-empty errMsg (for a 400) when neither form is complete.
func parseQuery(r *http.Request) (query, string) {
	q := r.URL.Query()
	atoi := func(key string) int {
		n, _ := strconv.Atoi(q.Get(key))
		return n
	}

	// Form 1: type + tmdb_id/tvdb_id.
	switch q.Get("type") {
	case "movie":
		if id := atoi("tmdb_id"); id > 0 {
			return query{mediaType: "movie", key: id, arrType: "radarr"}, ""
		}
		return query{}, "tmdb_id is required for type=movie"
	case "series":
		if id := atoi("tvdb_id"); id > 0 {
			return query{mediaType: "series", key: id, arrType: "sonarr"}, ""
		}
		return query{}, "tvdb_id is required for type=series"
	case "":
		// fall through to form 2
	default:
		return query{}, "type must be 'movie' or 'series'"
	}

	// Form 2: arr_type + arr_id.
	switch q.Get("arr_type") {
	case "radarr":
		if id := atoi("arr_id"); id > 0 {
			return query{mediaType: "movie", arrType: "radarr", arrID: id}, ""
		}
		return query{}, "arr_id is required with arr_type"
	case "sonarr":
		if id := atoi("arr_id"); id > 0 {
			return query{mediaType: "series", arrType: "sonarr", arrID: id}, ""
		}
		return query{}, "arr_id is required with arr_type"
	case "":
		return query{}, "specify type=movie|series with tmdb_id/tvdb_id, or arr_type=radarr|sonarr with arr_id"
	default:
		return query{}, "arr_type must be 'radarr' or 'sonarr'"
	}
}

// ── Handler ───────────────────────────────────────────────────────────────────

// HandleJourney serves GET /api/pelicula/journey (Viewer+; request-derived
// fields are additionally scoped to the request's owner or an admin).
func (h *Handler) HandleJourney(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q, errMsg := parseQuery(r)
	if errMsg != "" {
		httputil.WriteError(w, errMsg, http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	var degraded []string
	addDegraded := func(name string, err error) {
		slog.Warn("journey: upstream unavailable, degrading", "component", "journey", "upstream", name, "error", err)
		degraded = append(degraded, name)
	}

	// 1. *arr library item (shared ArrCache; a failure here degrades — the
	// request row and catalog row can still carry the journey).
	item, arrErr := h.findArrItem(ctx, q)
	if arrErr != nil {
		addDegraded(q.arrType, arrErr)
	}
	if item != nil {
		q.arrID = item.arrID
		if q.key == 0 {
			q.key = item.key
		}
	}

	// 2. Catalog row (local sqlite): availability tier, plus identity
	// fallback for the arr_id form when the *arr item is already gone.
	var catRow *repocatalog.Item
	if q.arrID > 0 {
		var err error
		catRow, err = h.Catalog.GetByArr(ctx, q.arrType, q.arrID)
		if err != nil {
			addDegraded("catalog", err)
		}
		if catRow != nil && q.key == 0 {
			if q.mediaType == "movie" {
				q.key = catRow.TmdbID
			} else {
				q.key = catRow.TvdbID
			}
		}
	}

	// 3. Request row: newest for this title, any state, with history.
	var req *reporeqs.Request
	if q.key > 0 {
		var err error
		req, err = h.Requests.LatestByKey(ctx, q.mediaType, q.key)
		if err != nil {
			addDegraded("requests", err)
			req = nil
		}
	}

	// 404: nothing anywhere knows this title. The catalog row keeps a title
	// alive for the brief window after a manager removes it from the *arr but
	// before the sweep clears our index — strictly more informative than 404.
	if item == nil && req == nil && catRow == nil {
		httputil.WriteError(w, "title not found", http.StatusNotFound)
		return
	}

	// 4. *arr queue records for this title (only when the *arr item exists —
	// queue records are keyed by *arr internal id).
	var records []map[string]any
	if item != nil {
		var err error
		records, err = h.queueRecordsFor(ctx, q)
		if err != nil {
			addDegraded(q.arrType+"-queue", err)
		}
	}
	dl := summarizeQueue(records)

	// 5. qBittorrent torrents — only when a queue record carries a hash.
	if len(dl.hashes) > 0 {
		torrents, err := h.torrents.get(h.timeNow(), func() ([]qbt.Torrent, error) {
			return h.Svc.QbtClient().ListTorrents(ctx)
		})
		if err != nil {
			addDegraded("qbt", err)
		} else {
			dl.applyTorrents(torrents)
		}
	}

	// 6. Procula job — only when there is something to match against.
	var job *proculaJob
	if item != nil || len(dl.hashes) > 0 {
		raw, err := h.proculaJobs.get(h.timeNow(), func() ([]byte, error) {
			return h.Procula.ListJobs(ctx)
		})
		if err != nil {
			addDegraded("procula", err)
		} else {
			job = matchJob(raw, q, dl.hashes)
		}
	}

	// Viewer scoping (server-side): request-derived FIELDS (the request
	// object, requested/approved at/by) are visible only to the request's
	// owner or an admin. Everyone else gets skipped requested/approved stages
	// and no request object at all. Title existence and availability remain
	// facts about the title, not the viewer — the raw request row still
	// drives those for every caller.
	var username string
	var isAdmin bool
	if h.SessionFor != nil {
		username, isAdmin = h.SessionFor(r)
	}
	reqVisible := req != nil && (isAdmin || req.RequestedBy == username)

	resp := h.buildResponse(q, item, catRow, req, reqVisible, dl, job)
	resp.Degraded = degraded
	httputil.WriteJSON(w, resp)
}

// ── *arr item lookup ──────────────────────────────────────────────────────────

// arrItem is the extracted subset of a Radarr movie / Sonarr series record.
type arrItem struct {
	arrID     int
	key       int // tmdbId (movie) / tvdbId (series)
	title     string
	year      int
	monitored bool
	hasFile   bool // series: episodeFileCount > 0
	fileCount int  // series only
	epCount   int  // series only
}

// findArrItem resolves the queried title against the shared full-library
// cache: by tmdbId/tvdbId (form 1) or by *arr internal id (form 2).
// Returns (nil, nil) when the library loads but has no match.
func (h *Handler) findArrItem(ctx context.Context, q query) (*arrItem, error) {
	var data []byte
	var err error
	if q.mediaType == "movie" {
		data, err = h.ArrCache.GetMovies(ctx)
	} else {
		data, err = h.ArrCache.GetSeries(ctx)
	}
	if err != nil {
		return nil, err
	}
	var list []map[string]any
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse %s library: %w", q.arrType, err)
	}

	keyField := "tmdbId"
	if q.mediaType == "series" {
		keyField = "tvdbId"
	}
	for _, m := range list {
		if q.arrID > 0 {
			if int(floatVal(m, "id")) != q.arrID {
				continue
			}
		} else if int(floatVal(m, keyField)) != q.key {
			continue
		}

		it := &arrItem{
			arrID:     int(floatVal(m, "id")),
			key:       int(floatVal(m, keyField)),
			title:     strVal(m, "title"),
			year:      int(floatVal(m, "year")),
			monitored: boolVal(m, "monitored"),
		}
		if q.mediaType == "movie" {
			it.hasFile = boolVal(m, "hasFile")
		} else if stats, ok := m["statistics"].(map[string]any); ok {
			it.fileCount = int(floatVal(stats, "episodeFileCount"))
			it.epCount = int(floatVal(stats, "totalEpisodeCount"))
			it.hasFile = it.fileCount > 0
		}
		return it, nil
	}
	return nil, nil
}

// queueRecordsFor returns this title's *arr queue records from the 10s
// snapshot (radarr keyed by movieId, sonarr by seriesId — the same filters
// as missingwatcher's radarrQueuedMovieIDs/sonarrQueuedEpisodeIDs).
func (h *Handler) queueRecordsFor(ctx context.Context, q query) ([]map[string]any, error) {
	var all []map[string]any
	var err error
	if q.mediaType == "movie" {
		all, err = h.radarrQueue.get(h.timeNow(), func() ([]map[string]any, error) {
			return h.Svc.RadarrClient().GetAllQueueRecords(ctx, "/api/v3", "")
		})
	} else {
		all, err = h.sonarrQueue.get(h.timeNow(), func() ([]map[string]any, error) {
			return h.Svc.SonarrClient().GetAllQueueRecords(ctx, "/api/v3", "")
		})
	}
	if err != nil {
		return nil, err
	}

	idField := "movieId"
	if q.mediaType == "series" {
		idField = "seriesId"
	}
	var matched []map[string]any
	for _, rec := range all {
		if int(floatVal(rec, idField)) == q.arrID {
			matched = append(matched, rec)
		}
	}
	return matched, nil
}

// ── Download summary ──────────────────────────────────────────────────────────

// downloadState aggregates a title's queue records (a series may have one per
// episode) and, when reachable, the matching qBittorrent torrents.
type downloadState struct {
	active   bool
	hashes   []string
	progress *float64
	detail   string // qbt state, e.g. "stalledDL"
	eta      int64  // seconds
}

// summarizeQueue derives the queue-record-only view: active flag, hashes for
// torrent/procula matching, and fallback progress from size/sizeleft
// (overridden by qbt data in applyTorrents when qbt is reachable).
func summarizeQueue(records []map[string]any) *downloadState {
	dl := &downloadState{}
	if len(records) == 0 {
		return dl
	}
	dl.active = true

	seen := map[string]bool{}
	var size, sizeleft float64
	for _, rec := range records {
		if hash := strVal(rec, "downloadId"); hash != "" && !seen[strings.ToLower(hash)] {
			seen[strings.ToLower(hash)] = true
			dl.hashes = append(dl.hashes, hash)
		}
		size += floatVal(rec, "size")
		sizeleft += floatVal(rec, "sizeleft")
	}
	if size > 0 {
		p := (size - sizeleft) / size
		dl.progress = &p
	}
	if tl := strVal(records[0], "timeleft"); tl != "" {
		dl.eta = parseTimeleft(tl)
	}
	return dl
}

// applyTorrents overrides the queue-record fallback with live qBittorrent
// data: hashes matched with strings.EqualFold (downloads.go's convention).
// A queue record whose torrent is gone keeps the fallback values.
func (dl *downloadState) applyTorrents(torrents []qbt.Torrent) {
	var sum float64
	var n int
	for _, t := range torrents {
		for _, hash := range dl.hashes {
			if strings.EqualFold(t.Hash, hash) {
				sum += t.Progress
				n++
				if dl.detail == "" {
					dl.detail = t.State
					dl.eta = t.ETA
				}
				break
			}
		}
	}
	if n > 0 {
		p := sum / float64(n)
		dl.progress = &p
	}
}

// parseTimeleft converts an *arr queue "timeleft" string ("hh:mm:ss" or
// "d.hh:mm:ss") to seconds. Returns 0 on any parse failure.
func parseTimeleft(s string) int64 {
	var days int64
	if dot := strings.IndexByte(s, '.'); dot > 0 {
		d, err := strconv.ParseInt(s[:dot], 10, 64)
		if err != nil {
			return 0
		}
		days, s = d, s[dot+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0
	}
	var hms [3]int64
	for i, p := range parts {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return 0
		}
		hms[i] = v
	}
	return days*86400 + hms[0]*3600 + hms[1]*60 + hms[2]
}

// ── Procula job matching ──────────────────────────────────────────────────────

// proculaJob is the extracted subset of a Procula job record.
type proculaJob struct {
	state    string // queued|processing|completed|failed|cancelled
	stage    string // validate|catalog|await_subs|dualsub|process|done
	progress float64
}

func (j *proculaJob) active() bool {
	return j != nil && (j.state == "queued" || j.state == "processing")
}

func (j *proculaJob) done() bool {
	return j != nil && j.state == "completed"
}

// matchJob finds this title's newest Procula job (by updated_at) in the raw
// jobs list: matched by source.arr_id+arr_type, or by source.download_hash
// against the queue-record hashes (EqualFold). Only the newest matching job
// drives the rail — see the processing-stage rules in buildResponse.
func matchJob(raw []byte, q query, hashes []string) *proculaJob {
	var jobs []map[string]any
	if json.Unmarshal(raw, &jobs) != nil {
		return nil
	}

	var best map[string]any
	var bestAt string
	for _, j := range jobs {
		source, _ := j["source"].(map[string]any)
		if source == nil {
			continue
		}
		matched := q.arrID > 0 &&
			int(floatVal(source, "arr_id")) == q.arrID &&
			strVal(source, "arr_type") == q.arrType
		if !matched {
			jobHash := strVal(source, "download_hash")
			for _, hash := range hashes {
				if jobHash != "" && strings.EqualFold(jobHash, hash) {
					matched = true
					break
				}
			}
		}
		if !matched {
			continue
		}
		// RFC3339 timestamps compare chronologically as strings.
		if at := strVal(j, "updated_at"); best == nil || at > bestAt {
			best, bestAt = j, at
		}
	}
	if best == nil {
		return nil
	}
	return &proculaJob{
		state:    strVal(best, "state"),
		stage:    strVal(best, "stage"),
		progress: floatVal(best, "progress"),
	}
}

// ── Rail assembly ─────────────────────────────────────────────────────────────

// buildResponse assembles the six-stage rail and top-level metadata from the
// gathered signals. req is the raw request row (nil when none exists);
// reqVisible is false unless the caller may see request-derived fields
// (owner or admin) — a scoped-out viewer gets skipped requested/approved
// stages and no request object, indistinguishable from a manager-added
// title, while title identity and availability stay viewer-independent.
func (h *Handler) buildResponse(q query, item *arrItem, catRow *repocatalog.Item, req *reporeqs.Request, reqVisible bool, dl *downloadState, job *proculaJob) *Response {
	resp := &Response{
		Type:    q.mediaType,
		ArrID:   q.arrID,
		ArrType: q.arrType,
	}
	if q.arrID == 0 {
		resp.ArrType = ""
	}
	if q.mediaType == "movie" {
		resp.TmdbID = q.key
	} else {
		resp.TvdbID = q.key
	}

	// Title/year: *arr item is authoritative; catalog row, then request row,
	// cover titles the *arr no longer knows.
	switch {
	case item != nil:
		resp.Title, resp.Year = item.title, item.year
		resp.Monitored, resp.HasFile = item.monitored, item.hasFile
	case catRow != nil:
		resp.Title, resp.Year = catRow.Title, catRow.Year
		if q.mediaType == "movie" && resp.TmdbID == 0 {
			resp.TmdbID = catRow.TmdbID
		}
		if q.mediaType == "series" && resp.TvdbID == 0 {
			resp.TvdbID = catRow.TvdbID
		}
	case req != nil:
		// The title itself is not identity-sensitive (it names what the
		// caller already queried by id) — only requested_by/history are.
		resp.Title, resp.Year = req.Title, req.Year
	}

	// Availability: an explicit signal (request marked available, catalog row
	// promoted to the library tier) or file evidence with nothing in flight.
	available := (req != nil && req.State == reporeqs.StateAvailable) ||
		(catRow != nil && catRow.Tier == "library") ||
		(item != nil && item.hasFile && !dl.active && !job.active())

	var requested, approved Stage
	if reqVisible {
		requested, approved = requestStages(req)
	} else {
		requested, approved = requestStages(nil)
	}
	searching := Stage{Stage: stageSearching, Status: statusPending}
	downloading := Stage{Stage: stageDownloading, Status: statusPending}
	processing := Stage{Stage: stageProcessing, Status: statusPending}
	availStage := Stage{Stage: stageAvailable, Status: statusPending}

	switch {
	case available:
		searching.Status = statusDone
		downloading.Status = statusDone
		processing.Status = statusDone
		availStage.Status = statusDone
		if q.mediaType == "series" && item != nil && item.epCount > 0 {
			availStage.Detail = fmt.Sprintf("%d/%d episodes", item.fileCount, item.epCount)
		}
	case job.active():
		searching.Status = statusDone
		downloading.Status = statusDone
		processing.Status = statusActive
		processing.Detail = job.stage
		p := job.progress
		processing.Progress = &p
	case job.done():
		searching.Status = statusDone
		downloading.Status = statusDone
		processing.Status = statusDone
	case dl.active:
		searching.Status = statusDone
		downloading.Status = statusActive
		downloading.Progress = dl.progress
		downloading.Detail = dl.detail
		downloading.ETA = dl.eta
	case item != nil && item.monitored:
		searching.Status = statusActive
	}

	resp.Stages = []Stage{requested, approved, searching, downloading, processing, availStage}
	resp.CurrentStage = currentStage(resp.Stages)
	switch {
	case downloading.Status == statusActive && downloading.Progress != nil:
		resp.Progress = downloading.Progress
	case processing.Status == statusActive && processing.Progress != nil:
		resp.Progress = processing.Progress
	case availStage.Status == statusDone:
		one := 1.0
		resp.Progress = &one
	}

	if reqVisible {
		resp.Request = requestInfo(req)
	}
	return resp
}

// requestStages derives the requested/approved stages. A nil visibleReq —
// no request row, or a request the viewer may not see — yields both stages
// skipped with no at/by (manager-added titles and scoped-out viewers are
// deliberately indistinguishable).
func requestStages(req *reporeqs.Request) (requested, approved Stage) {
	requested = Stage{Stage: stageRequested, Status: statusSkipped}
	approved = Stage{Stage: stageApproved, Status: statusSkipped}
	if req == nil {
		return requested, approved
	}

	requested.Status = statusDone
	requested.At = req.CreatedAt.UTC().Format(time.RFC3339)
	requested.By = req.RequestedBy

	switch req.State {
	case reporeqs.StatePending:
		approved.Status = statusPending
	case reporeqs.StateDenied:
		approved.Status = statusPending
		approved.Detail = "denied"
	default: // approved, grabbed, available — the request cleared approval
		approved.Status = statusDone
		approved.At = req.UpdatedAt.UTC().Format(time.RFC3339)
		for _, ev := range req.History {
			if ev.State == reporeqs.StateGrabbed || ev.State == reporeqs.StateApproved {
				approved.At = ev.At.UTC().Format(time.RFC3339)
				approved.By = ev.Actor
				break
			}
		}
	}
	return requested, approved
}

// currentStage picks the rail's headline stage: the active stage if any;
// else "available" when the rail is fully done; else the last done stage;
// else "requested" (a registered title with nothing in flight yet).
func currentStage(stages []Stage) string {
	for _, s := range stages {
		if s.Status == statusActive {
			return s.Stage
		}
	}
	last := ""
	for _, s := range stages {
		if s.Status == statusDone {
			last = s.Stage
		}
	}
	if last != "" {
		return last
	}
	return stageRequested
}

// requestInfo converts a repo request row to the response's request object.
func requestInfo(req *reporeqs.Request) *RequestInfo {
	history := make([]RequestEvent, len(req.History))
	for i, ev := range req.History {
		history[i] = RequestEvent{
			At:    ev.At.UTC().Format(time.RFC3339),
			State: string(ev.State),
			Actor: ev.Actor,
			Note:  ev.Note,
		}
	}
	return &RequestInfo{
		ID:          req.ID,
		State:       string(req.State),
		RequestedBy: req.RequestedBy,
		History:     history,
	}
}

// ── map[string]any helpers (local copies, per package convention) ─────────────

func strVal(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func floatVal(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

func boolVal(m map[string]any, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}
