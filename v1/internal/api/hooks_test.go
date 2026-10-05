package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pelicula/internal/store"
)

const (
	radarrDownload = `{
		"eventType":"Download",
		"movie":{"id":12,"title":"Dune","year":2021,"tmdbId":438631},
		"movieFile":{"path":"/media/movies/Dune (2021)/Dune.mkv","size":8123456789},
		"downloadId":"ABCDEF0123456789ABCDEF0123456789ABCDEF01",
		"isUpgrade":false
	}`
	sonarrDownload = `{
		"eventType":"Download",
		"series":{"id":5,"title":"Severance","tvdbId":371980},
		"episodes":[{"id":901,"seasonNumber":1,"episodeNumber":2},{"id":902}],
		"episodeFile":{"path":"/media/tv/Severance/S01E02.mkv","size":1234567},
		"downloadId":"FEDCBA9876543210FEDCBA9876543210FEDCBA98"
	}`
)

// hook posts a webhook body with the given secret header ("" omits it).
func (e *env) hook(secret, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest("POST", "/api/hooks/import", strings.NewReader(body))
	if secret != "" {
		req.Header.Set("X-Webhook-Secret", secret)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func TestHookRejectsBadSecret(t *testing.T) {
	e := newEnv(t)
	for name, secret := range map[string]string{"missing": "", "wrong": "nope", "prefix": "s3cre", "longer": "s3cret!"} {
		if got := e.hook(secret, radarrDownload).Code; got != 401 {
			t.Errorf("%s secret: status = %d, want 401", name, got)
		}
	}
	if n, _ := e.store.CountJobs(t.Context(), store.JobQueued); n != 0 {
		t.Fatalf("%d jobs queued by unauthorized calls", n)
	}
	if e.kicks != 0 {
		t.Fatal("unauthorized call kicked the worker")
	}
}

func TestHookRefusesWhenSecretUnset(t *testing.T) {
	e := newEnv(t)
	e.srv.Cfg.WebhookSecret = ""
	// Neither a missing header nor an empty one gets through.
	if got := e.hook("", radarrDownload).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("no header: status = %d, want 503", got)
	}
	req := httptest.NewRequest("POST", "/api/hooks/import", strings.NewReader(radarrDownload))
	req.Header.Set("X-Webhook-Secret", "")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty header: status = %d, want 503", rec.Code)
	}
	if n, _ := e.store.CountJobs(t.Context(), store.JobQueued); n != 0 {
		t.Fatalf("%d jobs queued with no secret configured", n)
	}
}

func TestHookTestAndIgnoredEvents(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{`{"eventType":"Test"}`, `{"eventType":"test"}`} {
		rec := e.hook("s3cret", body)
		wantStatus(t, rec, 200)
		if decode(t, rec)["status"] != "ok" {
			t.Errorf("%s -> %s", body, rec.Body)
		}
	}
	for _, body := range []string{`{"eventType":"Grab"}`, `{"eventType":"Rename"}`, `{}`} {
		rec := e.hook("s3cret", body)
		wantStatus(t, rec, 200)
		if decode(t, rec)["status"] != "ignored" {
			t.Errorf("%s -> %s", body, rec.Body)
		}
	}
	if n, _ := e.store.CountJobs(t.Context(), store.JobQueued); n != 0 || e.kicks != 0 {
		t.Fatalf("non-import events queued %d jobs, kicks=%d", n, e.kicks)
	}
}

func TestHookRadarrDownloadEnqueues(t *testing.T) {
	e := newEnv(t)
	e.radarr.movie = jm(t, `{"id":12,"runtime":155}`)

	rec := e.hook("s3cret", radarrDownload)
	wantStatus(t, rec, 200)
	got := decode(t, rec)
	if got["status"] != "queued" || got["job_id"] == nil {
		t.Fatalf("response = %v", got)
	}
	job, err := e.store.GetJob(t.Context(), int64(got["job_id"].(float64)))
	if err != nil {
		t.Fatal(err)
	}
	want := store.Job{
		ArrType: "radarr", ArrID: 12, EpisodeID: 0, Title: "Dune",
		Path: "/media/movies/Dune (2021)/Dune.mkv", Size: 8123456789,
		DownloadID: "ABCDEF0123456789ABCDEF0123456789ABCDEF01", RuntimeMin: 155, Status: store.JobQueued,
	}
	if job.ArrType != want.ArrType || job.ArrID != want.ArrID || job.EpisodeID != want.EpisodeID ||
		job.Title != want.Title || job.Path != want.Path || job.Size != want.Size ||
		job.DownloadID != want.DownloadID || job.RuntimeMin != want.RuntimeMin || job.Status != want.Status {
		t.Fatalf("job = %+v\nwant %+v", job, want)
	}
	if e.kicks != 1 {
		t.Fatalf("kicks = %d, want 1", e.kicks)
	}
}

func TestHookSonarrDownloadEnqueues(t *testing.T) {
	e := newEnv(t)
	e.sonarr.series = jm(t, `{"id":5,"runtime":47}`)

	rec := e.hook("s3cret", sonarrDownload)
	wantStatus(t, rec, 200)
	job, err := e.store.GetJob(t.Context(), int64(decode(t, rec)["job_id"].(float64)))
	if err != nil {
		t.Fatal(err)
	}
	if job.ArrType != "sonarr" || job.ArrID != 5 || job.EpisodeID != 901 || job.Title != "Severance" ||
		job.Path != "/media/tv/Severance/S01E02.mkv" || job.Size != 1234567 ||
		job.DownloadID != "FEDCBA9876543210FEDCBA9876543210FEDCBA98" || job.RuntimeMin != 47 {
		t.Fatalf("job = %+v", job)
	}
	if e.kicks != 1 {
		t.Fatalf("kicks = %d", e.kicks)
	}
}

func TestHookRuntimeLookupIsBestEffort(t *testing.T) {
	e := newEnv(t)
	e.radarr.getErr = errors.New("radarr down")
	rec := e.hook("s3cret", radarrDownload)
	wantStatus(t, rec, 200)
	job, _ := e.store.GetJob(t.Context(), int64(decode(t, rec)["job_id"].(float64)))
	if job == nil || job.RuntimeMin != 0 {
		t.Fatalf("job = %+v, want queued with runtime 0", job)
	}

	// No client at all behaves the same.
	e.srv.Sonarr = nil
	rec = e.hook("s3cret", sonarrDownload)
	wantStatus(t, rec, 200)
}

func TestHookDedupesRetries(t *testing.T) {
	e := newEnv(t)
	first := decode(t, e.hook("s3cret", radarrDownload))
	second := decode(t, e.hook("s3cret", radarrDownload))
	if first["job_id"] != second["job_id"] {
		t.Fatalf("retry created a second job: %v vs %v", first["job_id"], second["job_id"])
	}
	if second["status"] != "queued" {
		t.Errorf("second = %v", second)
	}
	if n, _ := e.store.CountJobs(t.Context(), store.JobQueued); n != 1 {
		t.Fatalf("%d queued jobs, want 1", n)
	}
}

func TestHookBadPayloads(t *testing.T) {
	e := newEnv(t)
	cases := map[string]string{
		"not json":           `{`,
		"no movie/series":    `{"eventType":"Download","downloadId":"x"}`,
		"no path":            `{"eventType":"Download","movie":{"id":1,"title":"x"},"movieFile":{}}`,
		"no file object":     `{"eventType":"Download","series":{"id":1,"title":"x"}}`,
		"no movie id":        `{"eventType":"Download","movie":{"title":"x"},"movieFile":{"path":"/media/x.mkv"}}`,
		"movie not object":   `{"eventType":"Download","movie":"Dune"}`,
		"event not a string": `{"eventType":5}`,
	}
	for name, body := range cases {
		code := e.hook("s3cret", body).Code
		want := 400
		if name == "event not a string" {
			want = 200 // ignored
		}
		if code != want {
			t.Errorf("%s: status = %d, want %d", name, code, want)
		}
	}
	if n, _ := e.store.CountJobs(t.Context(), store.JobQueued); n != 0 {
		t.Fatalf("%d jobs queued from bad payloads", n)
	}
}

func TestHookBodyLimit(t *testing.T) {
	e := newEnv(t)
	big := `{"eventType":"Test","pad":"` + strings.Repeat("a", 1<<20) + `"}`
	if got := e.hook("s3cret", big).Code; got != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", got)
	}
	// Just under the limit still works.
	ok := `{"eventType":"Test","pad":"` + strings.Repeat("a", 1<<19) + `"}`
	wantStatus(t, e.hook("s3cret", ok), 200)
}

func TestHookIsNotSessionGuarded(t *testing.T) {
	e := newEnv(t)
	// No session headers at all: the secret is the only credential.
	wantStatus(t, e.hook("s3cret", `{"eventType":"Test"}`), 200)
	// And a signed-in admin without the secret is still refused.
	req := httptest.NewRequest("POST", "/api/hooks/import", strings.NewReader(`{"eventType":"Test"}`))
	req.Header.Set("X-Test-User", "adam")
	req.Header.Set("X-Test-Role", "admin")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
