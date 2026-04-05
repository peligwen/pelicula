package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	q := newTestQueue(t)
	return &Server{queue: q, configDir: t.TempDir()}
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
		Path:    "/movies/Alien (1979)/alien.mkv",
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
