package journey

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"pelicula-api/internal/app/catalog"
	arrclient "pelicula-api/internal/clients/arr"
	proculaclient "pelicula-api/internal/clients/procula"
	qbtclient "pelicula-api/internal/clients/qbt"
	repocatalog "pelicula-api/internal/repo/catalog"
	reporeqs "pelicula-api/internal/repo/requests"
)

// ── stub upstream fixture ─────────────────────────────────────────────────────

// stubUpstreams runs httptest servers for Radarr, Sonarr, qBittorrent, and
// Procula with canned JSON bodies, counting requests per endpoint so cache
// tests can assert exact fan-out.
type stubUpstreams struct {
	// canned bodies (raw JSON); queue bodies are full QueuePage objects.
	movies      string
	series      string
	radarrQueue string
	sonarrQueue string
	torrents    string
	jobs        string

	// qbtStatus lets a test force qBittorrent failures (default 200).
	qbtStatus int

	// request counters, per endpoint
	movieCalls       atomic.Int32
	seriesCalls      atomic.Int32
	radarrQueueCalls atomic.Int32
	sonarrQueueCalls atomic.Int32
	torrentCalls     atomic.Int32
	jobCalls         atomic.Int32

	radarrSrv  *httptest.Server
	sonarrSrv  *httptest.Server
	qbtSrv     *httptest.Server
	proculaSrv *httptest.Server
}

func (s *stubUpstreams) start(t *testing.T) {
	t.Helper()

	writeJSON := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body)) //nolint:errcheck
	}

	radarrMux := http.NewServeMux()
	radarrMux.HandleFunc("/api/v3/movie", func(w http.ResponseWriter, r *http.Request) {
		s.movieCalls.Add(1)
		writeJSON(w, orDefault(s.movies, `[]`))
	})
	radarrMux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		s.radarrQueueCalls.Add(1)
		writeJSON(w, orDefault(s.radarrQueue, `{"totalRecords":0,"records":[]}`))
	})

	sonarrMux := http.NewServeMux()
	sonarrMux.HandleFunc("/api/v3/series", func(w http.ResponseWriter, r *http.Request) {
		s.seriesCalls.Add(1)
		writeJSON(w, orDefault(s.series, `[]`))
	})
	sonarrMux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		s.sonarrQueueCalls.Add(1)
		writeJSON(w, orDefault(s.sonarrQueue, `{"totalRecords":0,"records":[]}`))
	})

	qbtMux := http.NewServeMux()
	qbtMux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		s.torrentCalls.Add(1)
		if s.qbtStatus != 0 && s.qbtStatus != http.StatusOK {
			http.Error(w, "boom", s.qbtStatus)
			return
		}
		writeJSON(w, orDefault(s.torrents, `[]`))
	})

	proculaMux := http.NewServeMux()
	proculaMux.HandleFunc("/api/procula/jobs", func(w http.ResponseWriter, r *http.Request) {
		s.jobCalls.Add(1)
		writeJSON(w, orDefault(s.jobs, `[]`))
	})

	s.radarrSrv = httptest.NewServer(radarrMux)
	s.sonarrSrv = httptest.NewServer(sonarrMux)
	s.qbtSrv = httptest.NewServer(qbtMux)
	s.proculaSrv = httptest.NewServer(proculaMux)
	t.Cleanup(func() {
		s.radarrSrv.Close()
		s.sonarrSrv.Close()
		s.qbtSrv.Close()
		s.proculaSrv.Close()
	})
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// stubSvc implements Svc backed by the fixture's httptest servers.
type stubSvc struct{ up *stubUpstreams }

func (s *stubSvc) Keys() (sonarr, radarr, prowlarr string) {
	return "sonarr-key", "radarr-key", ""
}
func (s *stubSvc) SonarrClient() *arrclient.Client { return arrclient.New(s.up.sonarrSrv.URL, "k") }
func (s *stubSvc) RadarrClient() *arrclient.Client { return arrclient.New(s.up.radarrSrv.URL, "k") }
func (s *stubSvc) QbtClient() *qbtclient.Client    { return qbtclient.New(s.up.qbtSrv.URL) }

var _ Svc = (*stubSvc)(nil)

// ── in-memory stores (schemas copied from the repo store tests) ───────────────

func newRequestsStore(t *testing.T) *reporeqs.Store {
	t.Helper()
	db := openMemDB(t)
	if _, err := db.Exec(`
		CREATE TABLE requests (
			id           TEXT PRIMARY KEY,
			type         TEXT NOT NULL,
			tmdb_id      INTEGER NOT NULL DEFAULT 0,
			tvdb_id      INTEGER NOT NULL DEFAULT 0,
			title        TEXT NOT NULL,
			year         INTEGER NOT NULL DEFAULT 0,
			poster       TEXT,
			requested_by TEXT NOT NULL DEFAULT '',
			state        TEXT NOT NULL DEFAULT 'pending',
			reason       TEXT,
			arr_id       INTEGER,
			created_at   TEXT NOT NULL,
			updated_at   TEXT NOT NULL,
			seasons      TEXT NOT NULL DEFAULT '',
			available_seen_at TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE request_events (
			request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
			at         TEXT NOT NULL,
			state      TEXT NOT NULL,
			actor      TEXT NOT NULL DEFAULT '',
			note       TEXT NOT NULL DEFAULT ''
		);
	`); err != nil {
		t.Fatalf("create requests tables: %v", err)
	}
	return reporeqs.New(db)
}

func newCatalogStore(t *testing.T) *repocatalog.Store {
	t.Helper()
	db := openMemDB(t)
	if _, err := db.Exec(`
		CREATE TABLE catalog_items (
			id                 TEXT PRIMARY KEY,
			type               TEXT NOT NULL,
			parent_id          TEXT NOT NULL DEFAULT '',
			tmdb_id            INTEGER NOT NULL DEFAULT 0,
			tvdb_id            INTEGER NOT NULL DEFAULT 0,
			arr_id             INTEGER NOT NULL DEFAULT 0,
			arr_type           TEXT NOT NULL DEFAULT '',
			jellyfin_id        TEXT NOT NULL DEFAULT '',
			episode_id         INTEGER NOT NULL DEFAULT 0,
			season_number      INTEGER NOT NULL DEFAULT 0,
			episode_number     INTEGER NOT NULL DEFAULT 0,
			title              TEXT NOT NULL,
			year               INTEGER NOT NULL DEFAULT 0,
			tier               TEXT NOT NULL,
			artwork_url        TEXT NOT NULL DEFAULT '',
			synopsis           TEXT NOT NULL DEFAULT '',
			metadata_synced_at TEXT NOT NULL DEFAULT '',
			procula_job_id     TEXT NOT NULL DEFAULT '',
			file_path          TEXT NOT NULL DEFAULT '',
			source             TEXT NOT NULL DEFAULT 'arr',
			created_at         TEXT NOT NULL,
			updated_at         TEXT NOT NULL
		)
	`); err != nil {
		t.Fatalf("create catalog table: %v", err)
	}
	return repocatalog.New(db)
}

func openMemDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// ── handler construction ──────────────────────────────────────────────────────

// newHandler wires a Handler against the stub upstreams with fresh in-memory
// stores. The default session is an admin ("admin", true); tests that
// exercise scoping override h.SessionFor.
func newHandler(t *testing.T, up *stubUpstreams) *Handler {
	t.Helper()
	up.start(t)
	svc := &stubSvc{up: up}
	return &Handler{
		Svc: svc,
		ArrCache: catalog.NewCatalogCache(
			func(ctx context.Context) ([]byte, error) {
				return svc.RadarrClient().Get(ctx, "/api/v3/movie")
			},
			func(ctx context.Context) ([]byte, error) {
				return svc.SonarrClient().Get(ctx, "/api/v3/series")
			},
		),
		Procula:  proculaclient.New(up.proculaSrv.URL, ""),
		Requests: newRequestsStore(t),
		Catalog:  newCatalogStore(t),
		SessionFor: func(r *http.Request) (string, bool) {
			return "admin", true
		},
	}
}

// get performs a GET against HandleJourney and returns the recorder.
func get(t *testing.T, h *Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	h.HandleJourney(w, req)
	return w
}

// decode unmarshals a 200 response body.
func decode(t *testing.T, w *httptest.ResponseRecorder) Response {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return resp
}

// stageByName finds a stage in the rail, failing the test if absent.
func stageByName(t *testing.T, resp Response, name string) Stage {
	t.Helper()
	for _, s := range resp.Stages {
		if s.Stage == name {
			return s
		}
	}
	t.Fatalf("stage %q not in rail %+v", name, resp.Stages)
	return Stage{}
}

// assertRail asserts the canonical six-stage order and per-stage statuses.
// want maps stage name → status for every stage.
func assertRail(t *testing.T, resp Response, want map[string]string) {
	t.Helper()
	canonical := []string{stageRequested, stageApproved, stageSearching, stageDownloading, stageProcessing, stageAvailable}
	if len(resp.Stages) != len(canonical) {
		t.Fatalf("rail has %d stages, want %d: %+v", len(resp.Stages), len(canonical), resp.Stages)
	}
	for i, name := range canonical {
		if resp.Stages[i].Stage != name {
			t.Errorf("stages[%d].stage = %q, want %q", i, resp.Stages[i].Stage, name)
		}
		if resp.Stages[i].Status != want[name] {
			t.Errorf("stage %q status = %q, want %q", name, resp.Stages[i].Status, want[name])
		}
	}
}

// canned *arr fixtures shared across tests.
const (
	movieMonitoredNoFile = `[{"id":7,"tmdbId":550,"title":"Fight Club","year":1999,"monitored":true,"hasFile":false}]`
	movieWithFile        = `[{"id":7,"tmdbId":550,"title":"Fight Club","year":1999,"monitored":true,"hasFile":true}]`
	seriesPartial        = `[{"id":3,"tvdbId":9000,"title":"The Wire","year":2002,"monitored":true,"statistics":{"episodeFileCount":12,"totalEpisodeCount":20}}]`
	queueOneMovie        = `{"totalRecords":1,"records":[{"id":1,"movieId":7,"downloadId":"ABCDEF123456","size":100,"sizeleft":60,"timeleft":"00:10:00"}]}`
	torrentMidway        = `[{"hash":"abcdef123456","name":"Fight.Club.mkv","progress":0.45,"state":"downloading","eta":300,"size":100}]`
)

// seedRequest inserts a request row (plus a pending event) and returns it.
func seedRequest(t *testing.T, store *reporeqs.Store, reqType string, key int, state reporeqs.State, requestedBy string) *reporeqs.Request {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	req := &reporeqs.Request{
		ID:          "req_test_1",
		Type:        reqType,
		Title:       "Fight Club",
		Year:        1999,
		RequestedBy: requestedBy,
		State:       state,
		CreatedAt:   now.Add(-time.Hour),
		UpdatedAt:   now,
	}
	if reqType == "movie" {
		req.TmdbID = key
	} else {
		req.TvdbID = key
	}
	if err := store.Insert(context.Background(), req); err != nil {
		t.Fatalf("seed request: %v", err)
	}
	events := []reporeqs.Event{
		{RequestID: req.ID, At: req.CreatedAt, State: reporeqs.StatePending, Actor: requestedBy},
	}
	if state == reporeqs.StateGrabbed || state == reporeqs.StateAvailable {
		events = append(events, reporeqs.Event{
			RequestID: req.ID, At: now.Add(-30 * time.Minute),
			State: reporeqs.StateGrabbed, Actor: "boss", Note: "approved and added to *arr",
		})
	}
	for _, ev := range events {
		if err := store.InsertEvent(context.Background(), ev); err != nil {
			t.Fatalf("seed event: %v", err)
		}
	}
	return req
}

// ── param validation ──────────────────────────────────────────────────────────

func TestHandleJourney_BadParams(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{"no params", "/api/pelicula/journey"},
		{"type without id", "/api/pelicula/journey?type=movie"},
		{"series without tvdb", "/api/pelicula/journey?type=series&tmdb_id=550"},
		{"movie without tmdb", "/api/pelicula/journey?type=movie&tvdb_id=9000"},
		{"bad type", "/api/pelicula/journey?type=album&tmdb_id=1"},
		{"arr_type without arr_id", "/api/pelicula/journey?arr_type=radarr"},
		{"bad arr_type", "/api/pelicula/journey?arr_type=lidarr&arr_id=1"},
		{"arr_id alone", "/api/pelicula/journey?arr_id=7"},
		{"non-numeric id", "/api/pelicula/journey?type=movie&tmdb_id=abc"},
	}
	h := newHandler(t, &stubUpstreams{})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := get(t, h, tc.target)
			if w.Code != http.StatusBadRequest {
				t.Errorf("code = %d, want 400; body = %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestHandleJourney_MethodNotAllowed(t *testing.T) {
	h := newHandler(t, &stubUpstreams{})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/journey?type=movie&tmdb_id=1", nil)
	w := httptest.NewRecorder()
	h.HandleJourney(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("code = %d, want 405", w.Code)
	}
}

// ── 404 ───────────────────────────────────────────────────────────────────────

func TestHandleJourney_UnknownTitle404(t *testing.T) {
	h := newHandler(t, &stubUpstreams{})

	for _, target := range []string{
		"/api/pelicula/journey?type=movie&tmdb_id=999999",
		"/api/pelicula/journey?arr_type=radarr&arr_id=999999",
		"/api/pelicula/journey?type=series&tvdb_id=999999",
	} {
		w := get(t, h, target)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: code = %d, want 404; body = %s", target, w.Code, w.Body.String())
		}
	}
}

// ── stage derivation ──────────────────────────────────────────────────────────

// TestHandleJourney_MovieMidDownload: queue record + torrent → downloading
// active with qbt progress/state/eta; searching done; processing/available
// pending; requested/approved skipped (no request row).
func TestHandleJourney_MovieMidDownload(t *testing.T) {
	h := newHandler(t, &stubUpstreams{
		movies:      movieMonitoredNoFile,
		radarrQueue: queueOneMovie,
		torrents:    torrentMidway,
	})

	resp := decode(t, get(t, h, "/api/pelicula/journey?type=movie&tmdb_id=550"))

	assertRail(t, resp, map[string]string{
		stageRequested: statusSkipped, stageApproved: statusSkipped,
		stageSearching: statusDone, stageDownloading: statusActive,
		stageProcessing: statusPending, stageAvailable: statusPending,
	})
	if resp.CurrentStage != stageDownloading {
		t.Errorf("current_stage = %q, want downloading", resp.CurrentStage)
	}
	dl := stageByName(t, resp, stageDownloading)
	if dl.Progress == nil || *dl.Progress != 0.45 {
		t.Errorf("downloading progress = %v, want 0.45 (from qbt, not queue fallback)", dl.Progress)
	}
	if dl.Detail != "downloading" {
		t.Errorf("downloading detail = %q, want qbt state 'downloading'", dl.Detail)
	}
	if dl.ETA != 300 {
		t.Errorf("downloading eta = %d, want 300", dl.ETA)
	}
	if resp.Progress == nil || *resp.Progress != 0.45 {
		t.Errorf("top-level progress = %v, want 0.45", resp.Progress)
	}
	if resp.Title != "Fight Club" || resp.Year != 1999 || resp.ArrID != 7 ||
		resp.ArrType != "radarr" || resp.TmdbID != 550 || !resp.Monitored || resp.HasFile {
		t.Errorf("metadata mismatch: %+v", resp)
	}
	if len(resp.Degraded) != 0 {
		t.Errorf("degraded = %v, want empty", resp.Degraded)
	}
	if resp.Request != nil {
		t.Errorf("request = %+v, want absent (no request row)", resp.Request)
	}
}

// TestHandleJourney_ArrIDForm: the arr_type/arr_id query form resolves the
// same movie.
func TestHandleJourney_ArrIDForm(t *testing.T) {
	h := newHandler(t, &stubUpstreams{movies: movieMonitoredNoFile})

	resp := decode(t, get(t, h, "/api/pelicula/journey?arr_type=radarr&arr_id=7"))
	if resp.TmdbID != 550 || resp.Title != "Fight Club" {
		t.Errorf("arr_id form: tmdb_id = %d title = %q, want 550/Fight Club", resp.TmdbID, resp.Title)
	}
	if resp.CurrentStage != stageSearching {
		t.Errorf("current_stage = %q, want searching (monitored, no file, nothing in flight)", resp.CurrentStage)
	}
}

// TestHandleJourney_ProcessingByArrID: procula job matched via
// source.arr_id+arr_type → processing active, searching+downloading done.
func TestHandleJourney_ProcessingByArrID(t *testing.T) {
	h := newHandler(t, &stubUpstreams{
		movies: movieMonitoredNoFile,
		jobs: `[{"id":"job_1","state":"processing","stage":"transcode","progress":0.5,
		         "updated_at":"2026-07-11T10:00:00Z",
		         "source":{"arr_id":7,"arr_type":"radarr","type":"movie"}}]`,
	})

	resp := decode(t, get(t, h, "/api/pelicula/journey?type=movie&tmdb_id=550"))

	assertRail(t, resp, map[string]string{
		stageRequested: statusSkipped, stageApproved: statusSkipped,
		stageSearching: statusDone, stageDownloading: statusDone,
		stageProcessing: statusActive, stageAvailable: statusPending,
	})
	proc := stageByName(t, resp, stageProcessing)
	if proc.Detail != "transcode" {
		t.Errorf("processing detail = %q, want transcode", proc.Detail)
	}
	if proc.Progress == nil || *proc.Progress != 0.5 {
		t.Errorf("processing progress = %v, want 0.5", proc.Progress)
	}
	if resp.CurrentStage != stageProcessing {
		t.Errorf("current_stage = %q, want processing", resp.CurrentStage)
	}
}

// TestHandleJourney_ProcessingByDownloadHash: job carries no arr_id but its
// source.download_hash matches the queue record's downloadId (case-insensitive).
func TestHandleJourney_ProcessingByDownloadHash(t *testing.T) {
	h := newHandler(t, &stubUpstreams{
		movies:      movieMonitoredNoFile,
		radarrQueue: queueOneMovie,
		torrents:    torrentMidway,
		jobs: `[{"id":"job_2","state":"queued","stage":"validate","progress":0,
		         "updated_at":"2026-07-11T10:00:00Z",
		         "source":{"arr_id":0,"arr_type":"","download_hash":"abcdef123456"}}]`,
	})

	resp := decode(t, get(t, h, "/api/pelicula/journey?type=movie&tmdb_id=550"))

	proc := stageByName(t, resp, stageProcessing)
	if proc.Status != statusActive {
		t.Errorf("processing status = %q, want active (matched by download_hash)", proc.Status)
	}
	if proc.Detail != "validate" {
		t.Errorf("processing detail = %q, want validate", proc.Detail)
	}
	// Processing supersedes downloading on the rail.
	if s := stageByName(t, resp, stageDownloading); s.Status != statusDone {
		t.Errorf("downloading status = %q, want done under active processing", s.Status)
	}
}

// TestHandleJourney_AvailableViaHasFile: hasFile with nothing in flight →
// the whole rail is done and current_stage is available.
func TestHandleJourney_AvailableViaHasFile(t *testing.T) {
	h := newHandler(t, &stubUpstreams{movies: movieWithFile})

	resp := decode(t, get(t, h, "/api/pelicula/journey?type=movie&tmdb_id=550"))

	assertRail(t, resp, map[string]string{
		stageRequested: statusSkipped, stageApproved: statusSkipped,
		stageSearching: statusDone, stageDownloading: statusDone,
		stageProcessing: statusDone, stageAvailable: statusDone,
	})
	if resp.CurrentStage != stageAvailable {
		t.Errorf("current_stage = %q, want available", resp.CurrentStage)
	}
	if !resp.HasFile {
		t.Error("has_file = false, want true")
	}
	if resp.Progress == nil || *resp.Progress != 1.0 {
		t.Errorf("progress = %v, want 1.0", resp.Progress)
	}
}

// TestHandleJourney_AvailableViaRequestState: the request row alone (state
// available) marks the title available even without *arr file evidence, and
// the availability signal is viewer-independent (asserted via a non-owner).
func TestHandleJourney_AvailableViaRequestState(t *testing.T) {
	h := newHandler(t, &stubUpstreams{movies: movieMonitoredNoFile})
	seedRequest(t, h.Requests, "movie", 550, reporeqs.StateAvailable, "alice")
	h.SessionFor = func(r *http.Request) (string, bool) { return "bob", false } // non-owner

	resp := decode(t, get(t, h, "/api/pelicula/journey?type=movie&tmdb_id=550"))

	if s := stageByName(t, resp, stageAvailable); s.Status != statusDone {
		t.Errorf("available status = %q, want done (from request state)", s.Status)
	}
	if resp.CurrentStage != stageAvailable {
		t.Errorf("current_stage = %q, want available", resp.CurrentStage)
	}
	// Scoping still hides the request itself from the non-owner.
	if resp.Request != nil {
		t.Errorf("request = %+v, want absent for non-owner", resp.Request)
	}
	if s := stageByName(t, resp, stageRequested); s.Status != statusSkipped {
		t.Errorf("requested status = %q, want skipped for non-owner", s.Status)
	}
}

// TestHandleJourney_SeriesByTvdbID: series lookup by tvdb_id; partial
// episode files with nothing in flight → available with an episode-count
// detail.
func TestHandleJourney_SeriesByTvdbID(t *testing.T) {
	h := newHandler(t, &stubUpstreams{series: seriesPartial})

	resp := decode(t, get(t, h, "/api/pelicula/journey?type=series&tvdb_id=9000"))

	if resp.Type != "series" || resp.TvdbID != 9000 || resp.ArrType != "sonarr" || resp.ArrID != 3 {
		t.Errorf("series identity mismatch: %+v", resp)
	}
	if !resp.HasFile {
		t.Error("has_file = false, want true (episodeFileCount > 0)")
	}
	avail := stageByName(t, resp, stageAvailable)
	if avail.Status != statusDone {
		t.Errorf("available status = %q, want done", avail.Status)
	}
	if avail.Detail != "12/20 episodes" {
		t.Errorf("available detail = %q, want '12/20 episodes'", avail.Detail)
	}
}

// TestHandleJourney_SeriesDownloadSupersedesPartialFiles: a series with some
// episode files but an episode still in the sonarr queue is downloading, not
// available.
func TestHandleJourney_SeriesDownloadSupersedesPartialFiles(t *testing.T) {
	h := newHandler(t, &stubUpstreams{
		series: seriesPartial,
		sonarrQueue: `{"totalRecords":1,"records":[
			{"id":9,"seriesId":3,"episodeId":41,"downloadId":"FEEDBEEF0001","size":200,"sizeleft":50,"timeleft":"00:05:00"}]}`,
		// no matching torrent: qbt returns an empty list → queue fallback
	})

	resp := decode(t, get(t, h, "/api/pelicula/journey?type=series&tvdb_id=9000"))

	dl := stageByName(t, resp, stageDownloading)
	if dl.Status != statusActive {
		t.Errorf("downloading status = %q, want active", dl.Status)
	}
	// Queue-record fallback: (200-50)/200 = 0.75, eta from timeleft.
	if dl.Progress == nil || *dl.Progress != 0.75 {
		t.Errorf("downloading progress = %v, want 0.75 (sizeleft fallback)", dl.Progress)
	}
	if dl.ETA != 300 {
		t.Errorf("downloading eta = %d, want 300 (from timeleft 00:05:00)", dl.ETA)
	}
	if s := stageByName(t, resp, stageAvailable); s.Status != statusPending {
		t.Errorf("available status = %q, want pending while downloading", s.Status)
	}
}

// TestHandleJourney_DegradedQbt: qBittorrent 500 degrades — still 200, the
// downloading stage falls back to queue-record progress, and "qbt" is listed.
func TestHandleJourney_DegradedQbt(t *testing.T) {
	h := newHandler(t, &stubUpstreams{
		movies:      movieMonitoredNoFile,
		radarrQueue: queueOneMovie,
		qbtStatus:   http.StatusInternalServerError,
	})

	resp := decode(t, get(t, h, "/api/pelicula/journey?type=movie&tmdb_id=550"))

	found := false
	for _, d := range resp.Degraded {
		if d == "qbt" {
			found = true
		}
	}
	if !found {
		t.Errorf("degraded = %v, want to contain 'qbt'", resp.Degraded)
	}
	dl := stageByName(t, resp, stageDownloading)
	if dl.Status != statusActive {
		t.Errorf("downloading status = %q, want active despite qbt failure", dl.Status)
	}
	// Fallback progress from size/sizeleft: (100-60)/100 = 0.4.
	if dl.Progress == nil || *dl.Progress != 0.4 {
		t.Errorf("downloading progress = %v, want 0.4 queue fallback", dl.Progress)
	}
	if dl.ETA != 600 {
		t.Errorf("downloading eta = %d, want 600 (timeleft 00:10:00 fallback)", dl.ETA)
	}
}

// ── viewer scoping matrix ─────────────────────────────────────────────────────

func TestHandleJourney_ScopingMatrix(t *testing.T) {
	cases := []struct {
		name     string
		username string
		isAdmin  bool
		visible  bool
	}{
		{"owner viewer sees own request", "alice", false, true},
		{"non-owner viewer is scoped out", "bob", false, false},
		{"admin sees all", "boss", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandler(t, &stubUpstreams{movies: movieMonitoredNoFile})
			seedRequest(t, h.Requests, "movie", 550, reporeqs.StateGrabbed, "alice")
			h.SessionFor = func(r *http.Request) (string, bool) { return tc.username, tc.isAdmin }

			resp := decode(t, get(t, h, "/api/pelicula/journey?type=movie&tmdb_id=550"))

			requested := stageByName(t, resp, stageRequested)
			approved := stageByName(t, resp, stageApproved)

			if tc.visible {
				if resp.Request == nil {
					t.Fatal("request object absent, want present")
				}
				if resp.Request.RequestedBy != "alice" || resp.Request.State != "grabbed" {
					t.Errorf("request = %+v, want alice/grabbed", resp.Request)
				}
				if len(resp.Request.History) != 2 {
					t.Errorf("history has %d events, want 2", len(resp.Request.History))
				}
				if requested.Status != statusDone || requested.By != "alice" || requested.At == "" {
					t.Errorf("requested = %+v, want done with at/by", requested)
				}
				if approved.Status != statusDone || approved.By != "boss" || approved.At == "" {
					t.Errorf("approved = %+v, want done with at/by from the grabbed event", approved)
				}
			} else {
				if resp.Request != nil {
					t.Errorf("request = %+v, want absent for non-owner", resp.Request)
				}
				for _, s := range []Stage{requested, approved} {
					if s.Status != statusSkipped {
						t.Errorf("%s status = %q, want skipped", s.Stage, s.Status)
					}
					if s.At != "" || s.By != "" {
						t.Errorf("%s carries at/by (%q/%q), must be empty for non-owner", s.Stage, s.At, s.By)
					}
				}
			}
		})
	}
}

// TestHandleJourney_PendingRequestNoArrItem: a request-only title (nothing in
// the *arr yet) resolves 200 for its owner with requested done and approved
// pending; current_stage is the last done stage.
func TestHandleJourney_PendingRequestNoArrItem(t *testing.T) {
	h := newHandler(t, &stubUpstreams{})
	seedRequest(t, h.Requests, "movie", 550, reporeqs.StatePending, "alice")
	h.SessionFor = func(r *http.Request) (string, bool) { return "alice", false }

	resp := decode(t, get(t, h, "/api/pelicula/journey?type=movie&tmdb_id=550"))

	assertRail(t, resp, map[string]string{
		stageRequested: statusDone, stageApproved: statusPending,
		stageSearching: statusPending, stageDownloading: statusPending,
		stageProcessing: statusPending, stageAvailable: statusPending,
	})
	if resp.CurrentStage != stageRequested {
		t.Errorf("current_stage = %q, want requested (last done)", resp.CurrentStage)
	}
	if resp.Title != "Fight Club" {
		t.Errorf("title = %q, want Fight Club from the request row", resp.Title)
	}
}

// ── snapshot caches ───────────────────────────────────────────────────────────

// TestHandleJourney_SnapshotCaches10s: with an injected clock, a second
// request inside the 10s window re-hits no queue/qbt/procula stub; advancing
// past the TTL refetches. (The *arr library fetch is governed by the shared
// ArrCache's own 2-minute TTL, so it stays at one call throughout.)
func TestHandleJourney_SnapshotCaches10s(t *testing.T) {
	up := &stubUpstreams{
		movies:      movieMonitoredNoFile,
		radarrQueue: queueOneMovie,
		torrents:    torrentMidway,
		jobs:        `[]`,
	}
	h := newHandler(t, up)

	t0 := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	now := t0
	h.now = func() time.Time { return now }

	assertCounts := func(label string, queue, torrents, jobs int32) {
		t.Helper()
		if got := up.radarrQueueCalls.Load(); got != queue {
			t.Errorf("%s: radarr queue calls = %d, want %d", label, got, queue)
		}
		if got := up.torrentCalls.Load(); got != torrents {
			t.Errorf("%s: torrent calls = %d, want %d", label, got, torrents)
		}
		if got := up.jobCalls.Load(); got != jobs {
			t.Errorf("%s: procula job calls = %d, want %d", label, got, jobs)
		}
	}

	decode(t, get(t, h, "/api/pelicula/journey?type=movie&tmdb_id=550"))
	assertCounts("first call", 1, 1, 1)

	now = t0.Add(5 * time.Second)
	decode(t, get(t, h, "/api/pelicula/journey?type=movie&tmdb_id=550"))
	assertCounts("within TTL", 1, 1, 1)

	now = t0.Add(11 * time.Second)
	decode(t, get(t, h, "/api/pelicula/journey?type=movie&tmdb_id=550"))
	assertCounts("past TTL", 2, 2, 2)

	if got := up.movieCalls.Load(); got != 1 {
		t.Errorf("movie library calls = %d, want 1 (ArrCache 2-min TTL)", got)
	}
	// The movie path must never touch sonarr's queue.
	if got := up.sonarrQueueCalls.Load(); got != 0 {
		t.Errorf("sonarr queue calls = %d, want 0 for a movie journey", got)
	}
}

// ── unit: timeleft parsing ────────────────────────────────────────────────────

func TestParseTimeleft(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"00:10:00", 600},
		{"01:00:30", 3630},
		{"1.02:00:00", 93600}, // 1 day 2 hours
		{"", 0},
		{"garbage", 0},
		{"10:00", 0},
	}
	for _, tc := range cases {
		if got := parseTimeleft(tc.in); got != tc.want {
			t.Errorf("parseTimeleft(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
