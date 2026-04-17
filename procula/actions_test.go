package procula

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterAndLookupAction(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}

	def := &ActionDef{
		Name:        "test_noop",
		Label:       "Test No-op",
		AppliesTo:   []string{"movie", "episode"},
		Sync:        true,
		Description: "unit test",
		Handler: func(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	}
	Register(def)

	got := Lookup("test_noop")
	if got == nil {
		t.Fatal("Lookup returned nil")
	}
	if got.Label != "Test No-op" {
		t.Errorf("Label = %q", got.Label)
	}

	all := List()
	if len(all) == 0 {
		t.Fatal("List returned empty")
	}
}

func TestLookupUnknownAction(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}
	if Lookup("nope") != nil {
		t.Error("Lookup(nope) should be nil")
	}
}

func TestValidateActionHandler(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}
	registerBuiltinActions()

	def := Lookup("validate")
	if def == nil {
		t.Fatal("validate not registered")
	}
	if !def.Sync {
		t.Error("validate should be sync")
	}
	wantMovie, wantEp := false, false
	for _, a := range def.AppliesTo {
		if a == "movie" {
			wantMovie = true
		}
		if a == "episode" {
			wantEp = true
		}
	}
	if !wantMovie || !wantEp {
		t.Errorf("AppliesTo = %v", def.AppliesTo)
	}
}

func TestTranscodeActionDetectsTVFromPath(t *testing.T) {
	// Seed the library registry with defaults so lookups work without a live API.
	setLibraries(defaultLibraries())

	if got := arrTypeFromPath("/media/tv/Show/S01/ep.mkv"); got != "sonarr" {
		t.Errorf("arrTypeFromPath(/media/tv/...) = %q, want sonarr", got)
	}
	if got := arrTypeFromPath("/media/movies/Foo/foo.mkv"); got != "radarr" {
		t.Errorf("arrTypeFromPath(/media/movies/...) = %q, want radarr", got)
	}
	if got := mediaTypeFromPath("/media/tv/Show/ep.mkv"); got != "episode" {
		t.Errorf("mediaTypeFromPath(/media/tv/...) = %q, want episode", got)
	}
	if got := mediaTypeFromPath("/media/movies/foo.mkv"); got != "movie" {
		t.Errorf("mediaTypeFromPath(/media/movies/...) = %q, want movie", got)
	}
}

func TestSubtitleSearchRegistered(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}
	registerBuiltinActions()
	if Lookup("subtitle_search") == nil {
		t.Fatal("subtitle_search not registered")
	}
}

func TestHandleCreateActionSync(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}
	registerBuiltinActions()

	q := newTestQueue(t)
	srv := &Server{queue: q, db: q.db, configDir: t.TempDir()}

	// Need a background worker so the action actually runs.
	go RunWorker(q, t.TempDir(), "http://localhost:0")

	body := `{"action":"subtitle_search","target":{"arr_type":"radarr","arr_id":1},"params":{"arr_type":"radarr","arr_id":1,"languages":["en"]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/procula/actions?wait=3", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleCreateAction(w, req)

	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var resp ActionResult
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, w.Body.String())
	}
	if resp.JobID == "" {
		t.Error("JobID empty")
	}
}

func TestHandleCreateActionUnknown(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}
	q := newTestQueue(t)
	srv := &Server{queue: q, db: q.db, configDir: t.TempDir()}

	body := `{"action":"bogus","target":{}}`
	req := httptest.NewRequest(http.MethodPost, "/api/procula/actions", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleCreateAction(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSubtitleSearchActionValidatesParams(t *testing.T) {
	job := &Job{
		ID:     "job_test",
		Params: map[string]any{},
	}
	_, err := runSubtitleSearchAction(context.Background(), nil, job)
	if err == nil {
		t.Fatalf("expected error for missing params")
	}
}

func TestSubtitleSearchActionRegistered(t *testing.T) {
	registerBuiltinActions()
	def := Lookup("subtitle_search")
	if def == nil {
		t.Fatalf("subtitle_search not registered")
	}
	if !def.Sync {
		t.Errorf("subtitle_search should be sync")
	}
}

func TestRunDualSubAction_MissingPath(t *testing.T) {
	job := &Job{Params: map[string]any{}}
	_, err := runDualSubAction(context.Background(), nil, job)
	if err == nil || err.Error() != "dualsub: path required" {
		t.Errorf("expected 'dualsub: path required', got %v", err)
	}
}

func TestRunDualSubAction_MissingPairs(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Movie.mkv")
	os.WriteFile(mediaPath, []byte(""), 0644)
	job := &Job{Params: map[string]any{"path": mediaPath}}
	_, err := runDualSubAction(context.Background(), nil, job)
	if err == nil || err.Error() != "dualsub: at least one track pair required" {
		t.Errorf("expected pairs error, got %v", err)
	}
}

func TestWorkerDispatchesRegisteredAction(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}
	Register(&ActionDef{
		Name: "noop_for_test",
		Sync: true,
		Handler: func(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
			return map[string]any{"hello": "world"}, nil
		},
	})
	q := newTestQueue(t)
	job, err := q.Create(JobSource{Path: "/movies/noop.mkv"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = q.Update(job.ID, func(j *Job) {
		j.ActionType = "noop_for_test"
		j.Params = map[string]any{"k": "v"}
	})
	processJob(q, job.ID, t.TempDir(), "http://localhost:0")

	got, _ := q.Get(job.ID)
	if got.State != StateCompleted {
		t.Errorf("State = %q, want %q; err=%q", got.State, StateCompleted, got.Error)
	}
	if got.Result["hello"] != "world" {
		t.Errorf("Result = %v", got.Result)
	}
}

func TestLangTagFromBase(t *testing.T) {
	cases := []struct{ base, want string }{
		{"Movie.en", "en"},
		{"Movie.en.hi", "en"},
		{"Movie.es.forced", "es"},
		{"Movie.en.sdh", "en"},
		{"forced.hi", "und"},
	}
	for _, c := range cases {
		if got := langTagFromBase(c.base); got != c.want {
			t.Errorf("langTagFromBase(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}
