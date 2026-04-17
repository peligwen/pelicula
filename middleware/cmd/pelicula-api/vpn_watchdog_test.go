package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	qbtclient "pelicula-api/internal/clients/qbt"
)

// Ensure test constants match the real ones defined in vpn_watchdog.go.
const testGrace = gracePolls
const testCooldown = restartCooldownPolls
const testPostRestartGrace = postRestartGrace

func TestWdTick_FirstPortSync(t *testing.T) {
	s := wdInternalState{}
	s2, act := wdTick(51413, s)
	if act != wdActSync {
		t.Fatalf("expected wdActSync on first port, got %v", act)
	}
	if s2.status != wdSynced {
		t.Fatalf("expected wdSynced, got %v", s2.status)
	}
	if s2.lastKnownPort != 51413 {
		t.Fatalf("lastKnownPort = %d, want 51413", s2.lastKnownPort)
	}
}

func TestWdTick_SamePortNoAction(t *testing.T) {
	s := wdInternalState{status: wdSynced, lastKnownPort: 51413}
	_, act := wdTick(51413, s)
	if act != wdActNone {
		t.Fatalf("expected wdActNone for unchanged port, got %v", act)
	}
}

func TestWdTick_PortChangeSyncs(t *testing.T) {
	s := wdInternalState{status: wdSynced, lastKnownPort: 51413}
	s2, act := wdTick(60000, s)
	if act != wdActSync {
		t.Fatalf("expected wdActSync on port change, got %v", act)
	}
	if s2.lastKnownPort != 60000 {
		t.Fatalf("lastKnownPort = %d, want 60000", s2.lastKnownPort)
	}
}

func TestWdTick_GracePeriodBeforeRestart(t *testing.T) {
	s := wdInternalState{status: wdSynced, lastKnownPort: 51413}
	for i := 0; i < testGrace-1; i++ {
		s, _ = wdTick(0, s)
		if s.status != wdGrace {
			t.Fatalf("poll %d: expected wdGrace, got %v", i+1, s.status)
		}
	}
	if s.restartAttempts != 0 {
		t.Fatalf("restartAttempts should be 0 during grace, got %d", s.restartAttempts)
	}
}

func TestWdTick_RestartAfterGrace(t *testing.T) {
	s := wdInternalState{status: wdSynced, lastKnownPort: 51413}
	var act watchdogAction
	for i := 0; i < testGrace; i++ {
		s, act = wdTick(0, s)
	}
	if act != wdActRestart {
		t.Fatalf("expected wdActRestart after grace, got %v", act)
	}
	if s.status != wdRestarting {
		t.Fatalf("expected wdRestarting, got %v", s.status)
	}
	if s.restartAttempts != 1 {
		t.Fatalf("restartAttempts = %d, want 1", s.restartAttempts)
	}
	if s.restartCooldown != testCooldown {
		t.Fatalf("restartCooldown = %d, want %d", s.restartCooldown, testCooldown)
	}
}

func TestWdTick_CooldownSkipsPortCheck(t *testing.T) {
	s := wdInternalState{
		status:          wdRestarting,
		restartAttempts: 1,
		restartCooldown: testCooldown,
	}
	for i := 0; i < testCooldown; i++ {
		var act watchdogAction
		s, act = wdTick(0, s)
		if act != wdActNone {
			t.Fatalf("cooldown tick %d: expected wdActNone, got %v", i+1, act)
		}
	}
	if s.restartCooldown != 0 {
		t.Fatalf("restartCooldown = %d, want 0 after cooldown", s.restartCooldown)
	}
}

func TestWdTick_DegradedAfterPostRestartGrace(t *testing.T) {
	// Cooldown already expired; restartAttempts=1; port still 0.
	s := wdInternalState{
		status:          wdRestarting,
		restartAttempts: 1,
		restartCooldown: 0,
	}
	for i := 0; i < testPostRestartGrace; i++ {
		s, _ = wdTick(0, s)
	}
	if s.status != wdDegraded {
		t.Fatalf("expected wdDegraded after post-restart grace, got %v", s.status)
	}
}

func TestWdTick_DegradedStaysUntilPortReturns(t *testing.T) {
	s := wdInternalState{status: wdDegraded, restartAttempts: 1}
	for i := 0; i < 5; i++ {
		s, _ = wdTick(0, s)
		if s.status != wdDegraded {
			t.Fatalf("tick %d: expected wdDegraded to persist", i+1)
		}
	}
}

func TestWdTick_RecoveryFromDegraded(t *testing.T) {
	s := wdInternalState{status: wdDegraded, restartAttempts: 1, lastKnownPort: 51413}
	s2, act := wdTick(51413, s)
	if act != wdActSync {
		t.Fatalf("expected wdActSync on recovery, got %v", act)
	}
	if s2.status != wdSynced {
		t.Fatalf("expected wdSynced after recovery, got %v", s2.status)
	}
	if s2.restartAttempts != 0 {
		t.Fatalf("restartAttempts should reset on recovery, got %d", s2.restartAttempts)
	}
}

func TestWdTick_NoDoubleRestart(t *testing.T) {
	s := wdInternalState{status: wdDegraded, restartAttempts: 1}
	for i := 0; i < testGrace*2; i++ {
		var act watchdogAction
		s, act = wdTick(0, s)
		if act == wdActRestart {
			t.Fatalf("unexpected second wdActRestart at tick %d", i+1)
		}
	}
}

func TestSyncQbtListenPort_CallsSetPreferences(t *testing.T) {
	var gotJSON string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/app/setPreferences" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		gotJSON = r.FormValue("json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := &ServiceClients{
		client: &http.Client{},
		Qbt:    qbtclient.New(srv.URL),
	}
	if err := syncQbtListenPort(s, 51413); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantJSON := `{"listen_port":51413}`
	if gotJSON != wantJSON {
		t.Fatalf("json pref = %q, want %q", gotJSON, wantJSON)
	}
}

func TestSyncQbtListenPort_ToleratesError(t *testing.T) {
	s := &ServiceClients{
		client: &http.Client{},
		Qbt:    qbtclient.New("http://127.0.0.1:0"), // nothing listening
	}
	err := syncQbtListenPort(s, 51413)
	if err == nil {
		t.Fatal("expected error when nothing is listening, got nil")
	}
}
