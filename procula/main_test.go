package procula

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	db := testDB(t)
	q, err := NewQueue(db)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	return &Server{queue: q, db: db, configDir: t.TempDir()}
}

func TestHandlePing(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/ping", nil)
	w := httptest.NewRecorder()
	srv.handlePing(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want %q", body["status"], "ok")
	}
}

func TestHandleStatus(t *testing.T) {
	srv := newTestServer(t)

	// Create a job so queue is non-empty
	srv.queue.Create(testSource("/downloads/test.mkv"))

	req := httptest.NewRequest("GET", "/api/procula/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want ok", body["status"])
	}
	if body["queue"] == nil {
		t.Error("expected queue key in response")
	}
}

func TestHandleListJobs(t *testing.T) {
	srv := newTestServer(t)
	srv.queue.Create(testSource("/downloads/a.mkv"))
	srv.queue.Create(testSource("/downloads/b.mkv"))

	req := httptest.NewRequest("GET", "/api/procula/jobs", nil)
	w := httptest.NewRecorder()
	srv.handleListJobs(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var jobs []Job
	if err := json.Unmarshal(w.Body.Bytes(), &jobs); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("expected 2 jobs, got %d", len(jobs))
	}
}

func TestHandleCreateJob(t *testing.T) {
	srv := newTestServer(t)
	src := JobSource{
		Type:    "movie",
		Title:   "Alien",
		Year:    1979,
		Path:    "/media/movies/Alien (1979)/alien.mkv",
		Size:    5_000_000_000,
		ArrType: "radarr",
	}
	body, _ := json.Marshal(src)

	req := httptest.NewRequest("POST", "/api/procula/jobs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleCreateJob(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	var job Job
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if job.ID == "" {
		t.Error("expected non-empty job ID")
	}
	if job.State != StateQueued {
		t.Errorf("State = %q, want %q", job.State, StateQueued)
	}
}

func TestHandleCreateJob_MissingPath(t *testing.T) {
	srv := newTestServer(t)
	src := JobSource{Type: "movie", ArrType: "radarr"} // no path
	body, _ := json.Marshal(src)

	req := httptest.NewRequest("POST", "/api/procula/jobs", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleCreateJob(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleCreateJob_DisallowedPath(t *testing.T) {
	srv := newTestServer(t)
	src := JobSource{Path: "/etc/passwd", ArrType: "radarr"}
	body, _ := json.Marshal(src)

	req := httptest.NewRequest("POST", "/api/procula/jobs", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleCreateJob(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleCreateJob_InvalidJSON(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("POST", "/api/procula/jobs", bytes.NewReader([]byte("not json{")))
	w := httptest.NewRecorder()
	srv.handleCreateJob(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleGetJob(t *testing.T) {
	srv := newTestServer(t)
	job, _ := srv.queue.Create(testSource("/downloads/test.mkv"))

	req := httptest.NewRequest("GET", "/api/procula/jobs/"+job.ID, nil)
	req.SetPathValue("id", job.ID)
	w := httptest.NewRecorder()
	srv.handleGetJob(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var got Job
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.ID != job.ID {
		t.Errorf("ID = %q, want %q", got.ID, job.ID)
	}
}

func TestHandleGetJob_NotFound(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/procula/jobs/bogus", nil)
	req.SetPathValue("id", "bogus")
	w := httptest.NewRecorder()
	srv.handleGetJob(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleRetryJob(t *testing.T) {
	srv := newTestServer(t)
	job, _ := srv.queue.Create(testSource("/downloads/test.mkv"))

	// Move to failed state
	srv.queue.Update(job.ID, func(j *Job) { j.State = StateFailed })

	req := httptest.NewRequest("POST", "/api/procula/jobs/"+job.ID+"/retry", nil)
	req.SetPathValue("id", job.ID)
	w := httptest.NewRecorder()
	srv.handleRetryJob(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	got, _ := srv.queue.Get(job.ID)
	if got.State != StateQueued {
		t.Errorf("State after retry = %q, want %q", got.State, StateQueued)
	}
}

func TestHandleRetryJob_NotRetryable(t *testing.T) {
	srv := newTestServer(t)
	job, _ := srv.queue.Create(testSource("/downloads/test.mkv"))
	// still queued — not retryable

	req := httptest.NewRequest("POST", "/api/procula/jobs/"+job.ID+"/retry", nil)
	req.SetPathValue("id", job.ID)
	w := httptest.NewRecorder()
	srv.handleRetryJob(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleCancelJob(t *testing.T) {
	srv := newTestServer(t)
	job, _ := srv.queue.Create(testSource("/downloads/test.mkv"))

	req := httptest.NewRequest("POST", "/api/procula/jobs/"+job.ID+"/cancel", nil)
	req.SetPathValue("id", job.ID)
	w := httptest.NewRecorder()
	srv.handleCancelJob(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	got, _ := srv.queue.Get(job.ID)
	if got.State != StateCancelled {
		t.Errorf("State = %q, want %q", got.State, StateCancelled)
	}
}

func TestHandleCancelJob_NotFound(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("POST", "/api/procula/jobs/bogus/cancel", nil)
	req.SetPathValue("id", "bogus")
	w := httptest.NewRecorder()
	srv.handleCancelJob(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func testJobBody() []byte {
	src := JobSource{
		Type:    "movie",
		Title:   "Alien",
		Year:    1979,
		Path:    "/media/movies/Alien (1979)/alien.mkv",
		Size:    5_000_000_000,
		ArrType: "radarr",
	}
	b, _ := json.Marshal(src)
	return b
}

func TestPostJobsReturns503WhenCritical(t *testing.T) {
	srv := newTestServer(t)

	// Inject critical state; restore on cleanup.
	setStorageState(StorageStateCritical)
	t.Cleanup(func() { setStorageState(StorageStateOk) })

	req := httptest.NewRequest("POST", "/api/procula/jobs", bytes.NewReader(testJobBody()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleCreateJob(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", w.Code, w.Body.String())
	}
	if ra := w.Header().Get("Retry-After"); ra != "300" {
		t.Errorf("Retry-After = %q, want %q", ra, "300")
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != "storage_critical" {
		t.Errorf("error = %q, want storage_critical", body["error"])
	}
	if body["message"] == "" {
		t.Error("message must not be empty")
	}
}

func TestPostJobsAcceptedWhenOk(t *testing.T) {
	srv := newTestServer(t)

	setStorageState(StorageStateOk)
	t.Cleanup(func() { setStorageState(StorageStateOk) })

	req := httptest.NewRequest("POST", "/api/procula/jobs", bytes.NewReader(testJobBody()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleCreateJob(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
}

func TestPostJobsAcceptedWhenWarning(t *testing.T) {
	srv := newTestServer(t)

	// Warning is notification-only — must NOT pause admission.
	setStorageState(StorageStateWarning)
	t.Cleanup(func() { setStorageState(StorageStateOk) })

	req := httptest.NewRequest("POST", "/api/procula/jobs", bytes.NewReader(testJobBody()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleCreateJob(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 (warning should not block); body: %s", w.Code, w.Body.String())
	}
}

func TestHandleCatalogFlagsReturnsAll(t *testing.T) {
	tmp := t.TempDir()
	db, err := OpenDB(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	flags := []Flag{{Code: "validation_failed", Severity: FlagSeverityError}}
	if err := UpsertFlagsForPath(db, "/movies/A/A.mkv", "job_a", flags); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	s := &Server{db: db, configDir: tmp}
	req := httptest.NewRequest(http.MethodGet, "/api/procula/catalog/flags", nil)
	w := httptest.NewRecorder()
	s.handleCatalogFlags(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Rows []CatalogFlagRow `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Rows) != 1 || resp.Rows[0].Path != "/movies/A/A.mkv" {
		t.Fatalf("rows = %+v", resp.Rows)
	}
}
