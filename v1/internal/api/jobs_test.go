package api

import (
	"fmt"
	"testing"

	"pelicula/internal/store"
)

func seedJob(t *testing.T, e *env, path string, status store.JobStatus) int64 {
	t.Helper()
	j := &store.Job{ArrType: "radarr", ArrID: 1, Title: "Job " + path, Path: path, Size: 100}
	if err := e.store.EnqueueJob(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	if status == store.JobQueued {
		return j.ID
	}
	claimed, err := e.store.ClaimNextJob(t.Context())
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if status != store.JobRunning {
		if err := e.store.FinishJob(t.Context(), claimed.ID, status, `{"passed":false}`, "bad"); err != nil {
			t.Fatal(err)
		}
	}
	return claimed.ID
}

func TestListJobs(t *testing.T) {
	e := newEnv(t)
	rec := e.do(viewer, "GET", "/api/jobs", nil)
	wantStatus(t, rec, 200)
	if rec.Body.String() != "{\"jobs\":[]}\n" {
		t.Fatalf("empty body = %q", rec.Body)
	}

	for i := 0; i < 5; i++ {
		seedJob(t, e, fmt.Sprintf("/media/movies/%d.mkv", i), store.JobFailed)
	}
	got := decode(t, e.do(viewer, "GET", "/api/jobs", nil))["jobs"].([]any)
	if len(got) != 5 {
		t.Fatalf("jobs = %d, want 5", len(got))
	}
	first := got[0].(map[string]any)
	if first["path"] != "/media/movies/4.mkv" || first["status"] != "failed" || first["error"] != "bad" || first["arr_type"] != "radarr" {
		t.Errorf("newest job = %v", first)
	}

	got = decode(t, e.do(viewer, "GET", "/api/jobs?limit=2", nil))["jobs"].([]any)
	if len(got) != 2 {
		t.Fatalf("limit=2 returned %d", len(got))
	}
	for _, bad := range []string{"limit=0", "limit=-3", "limit=abc"} {
		wantStatus(t, e.do(viewer, "GET", "/api/jobs?"+bad, nil), 400)
	}
}

func TestRetryJob(t *testing.T) {
	e := newEnv(t)
	id := seedJob(t, e, "/media/movies/a.mkv", store.JobFailed)
	path := fmt.Sprintf("/api/jobs/%d/retry", id)

	wantStatus(t, e.do(viewer, "POST", path, nil), 403)
	if e.kicks != 0 {
		t.Fatal("forbidden retry kicked the worker")
	}

	wantStatus(t, e.do(manager, "POST", path, nil), 204)
	job, err := e.store.GetJob(t.Context(), id)
	if err != nil || job.Status != store.JobQueued {
		t.Fatalf("job = %+v, err %v; want queued", job, err)
	}
	if e.kicks != 1 {
		t.Fatalf("kicks = %d, want 1", e.kicks)
	}

	// Already queued: nothing to retry.
	wantStatus(t, e.do(manager, "POST", path, nil), 409)
	wantStatus(t, e.do(manager, "POST", "/api/jobs/999/retry", nil), 404)
	wantStatus(t, e.do(manager, "POST", "/api/jobs/x/retry", nil), 400)
	if e.kicks != 1 {
		t.Fatalf("kicks = %d after failed retries, want still 1", e.kicks)
	}

	// A nil Kick is fine. (Finish the requeued job first so the next claim
	// picks up the new one.)
	e.srv.Kick = nil
	if err := e.store.FinishJob(t.Context(), mustClaim(t, e), store.JobPassed, "", ""); err != nil {
		t.Fatal(err)
	}
	id2 := seedJob(t, e, "/media/movies/b.mkv", store.JobFailed)
	wantStatus(t, e.do(manager, "POST", fmt.Sprintf("/api/jobs/%d/retry", id2), nil), 204)
}

func mustClaim(t *testing.T, e *env) int64 {
	t.Helper()
	j, err := e.store.ClaimNextJob(t.Context())
	if err != nil || j == nil {
		t.Fatalf("claim: %v %v", j, err)
	}
	return j.ID
}
