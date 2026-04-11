package main

import (
	"context"
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
	if got := arrTypeFromPath("/tv/Show/S01/ep.mkv"); got != "sonarr" {
		t.Errorf("arrTypeFromPath(/tv/...) = %q, want sonarr", got)
	}
	if got := arrTypeFromPath("/movies/Foo/foo.mkv"); got != "radarr" {
		t.Errorf("arrTypeFromPath(/movies/...) = %q, want radarr", got)
	}
	if got := mediaTypeFromPath("/tv/Show/ep.mkv"); got != "episode" {
		t.Errorf("mediaTypeFromPath(/tv/...) = %q, want episode", got)
	}
	if got := mediaTypeFromPath("/movies/foo.mkv"); got != "movie" {
		t.Errorf("mediaTypeFromPath(/movies/...) = %q, want movie", got)
	}
}

func TestSubtitleRefreshRegistered(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}
	registerBuiltinActions()
	if Lookup("subtitle_refresh") == nil {
		t.Fatal("subtitle_refresh not registered")
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
