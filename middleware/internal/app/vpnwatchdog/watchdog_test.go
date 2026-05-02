package vpnwatchdog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	appservices "pelicula-api/internal/app/services"
	qbtclient "pelicula-api/internal/clients/qbt"
	"pelicula-api/internal/config"
)

type notifySpy struct {
	calls []struct{ title, body string }
	mu    sync.Mutex
}

func (s *notifySpy) hook(t, b string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, struct{ title, body string }{t, b})
}

func (s *notifySpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *notifySpy) titleAt(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[i].title
}

// Ensure test constants match the real ones defined in watchdog.go.
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

	s := appservices.New(&config.Config{}, "")
	s.Qbt = qbtclient.New(srv.URL)
	w := New(s, nil, nil)
	if err := w.syncQbtListenPort(context.Background(), 51413); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantJSON := `{"listen_port":51413}`
	if gotJSON != wantJSON {
		t.Fatalf("json pref = %q, want %q", gotJSON, wantJSON)
	}
}

func TestSyncQbtListenPort_ToleratesError(t *testing.T) {
	s := appservices.New(&config.Config{}, "")
	s.Qbt = qbtclient.New("http://127.0.0.1:0") // nothing listening
	w := New(s, nil, nil)
	err := w.syncQbtListenPort(context.Background(), 51413)
	if err == nil {
		t.Fatal("expected error when nothing is listening, got nil")
	}
}

// TestSyncQbtListenPort_RespectsCtxCancel verifies that cancelling the context
// mid-call causes syncQbtListenPort to return promptly rather than waiting for
// the server to respond.
func TestWatchdog_NotifiesOnDegradedTransition(t *testing.T) {
	spy := &notifySpy{}
	w := New(nil, nil, nil)
	w.Notify = spy.hook

	// unknown → synced: no notification (not the degraded transition)
	w.notifyTransition(wdUnknown, wdSynced)
	if spy.count() != 0 {
		t.Fatalf("expected 0 calls after unknown→synced, got %d", spy.count())
	}

	// synced → degraded: must fire exactly once with "VPN degraded"
	w.notifyTransition(wdSynced, wdDegraded)
	if spy.count() != 1 {
		t.Fatalf("expected 1 call after synced→degraded, got %d", spy.count())
	}
	if spy.titleAt(0) != "VPN degraded" {
		t.Fatalf("title = %q, want %q", spy.titleAt(0), "VPN degraded")
	}

	// degraded → degraded (same state, shouldn't happen in Run but safe): no extra call
	w.notifyTransition(wdDegraded, wdDegraded)
	if spy.count() != 1 {
		t.Fatalf("expected still 1 call after degraded→degraded, got %d", spy.count())
	}
}

func TestWatchdog_NotifiesOnRecoveredTransition(t *testing.T) {
	spy := &notifySpy{}
	w := New(nil, nil, nil)
	w.Notify = spy.hook

	// degraded → synced: must fire exactly once with "VPN recovered"
	w.notifyTransition(wdDegraded, wdSynced)
	if spy.count() != 1 {
		t.Fatalf("expected 1 call after degraded→synced, got %d", spy.count())
	}
	if spy.titleAt(0) != "VPN recovered" {
		t.Fatalf("title = %q, want %q", spy.titleAt(0), "VPN recovered")
	}
}

func TestWatchdog_NoNotifyPerTick(t *testing.T) {
	spy := &notifySpy{}
	w := New(nil, nil, nil)
	w.Notify = spy.hook

	// Repeated same-state transitions (as would happen each tick with no change)
	// must never fire.
	for i := 0; i < 10; i++ {
		w.notifyTransition(wdSynced, wdSynced)
		w.notifyTransition(wdGrace, wdGrace)
		w.notifyTransition(wdRestarting, wdRestarting)
	}

	// Intermediate transitions that aren't degraded-entry or recovery must not fire.
	w.notifyTransition(wdUnknown, wdSynced)
	w.notifyTransition(wdSynced, wdGrace)
	w.notifyTransition(wdGrace, wdRestarting)
	w.notifyTransition(wdRestarting, wdGrace)

	if spy.count() != 0 {
		t.Fatalf("expected 0 notify calls for non-critical transitions, got %d", spy.count())
	}
}

func TestWatchdog_NotifyNilSafe(t *testing.T) {
	w := New(nil, nil, nil)
	// w.Notify is nil — must not panic
	w.notifyTransition(wdSynced, wdDegraded)
	w.notifyTransition(wdDegraded, wdSynced)
}

func TestSyncQbtListenPort_RespectsCtxCancel(t *testing.T) {
	ready := make(chan struct{})
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case ready <- struct{}{}:
		default:
		}
		// Block until test signals done or the test context cancels.
		select {
		case <-unblock:
		case <-time.After(10 * time.Second):
		}
	}))
	defer func() {
		close(unblock)
		srv.Close()
	}()

	s := appservices.New(&config.Config{}, "")
	s.Qbt = qbtclient.New(srv.URL)
	wd := New(s, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- wd.syncQbtListenPort(ctx, 51413)
	}()

	// Wait until the server has the request in-flight, then cancel.
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("server never received the request")
	}
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error after ctx cancel, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("syncQbtListenPort did not return after ctx cancel")
	}
}
