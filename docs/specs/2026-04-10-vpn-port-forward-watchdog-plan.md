# VPN Port Forward Watchdog — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect VPN port forwarding failures, auto-restart gluetun once to recover, sync the forwarded port to qBittorrent on every change, and surface a persistent dashboard warning when recovery fails.

**Architecture:** A background goroutine (`StartVPNWatchdog`) polls gluetun every 30s and drives a pure state-machine function (`wdTick`) that handles grace periods, one restart attempt, and degraded-state signaling. A package-level mutex-guarded struct exposes state to the health endpoint. Dashboard reads `port_status` from `/api/pelicula/health` and renders a banner + red VPN card entry when degraded.

**Tech Stack:** Go stdlib (sync, net/http, time), qBittorrent Web API v2, existing `dockerRestart`/`gluetunGet`/`QbtPost` helpers, vanilla JS + CSS.

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `middleware/vpn_watchdog.go` | Create | State types, `wdTick`, `syncQbtListenPort`, `StartVPNWatchdog`, package-level state |
| `middleware/vpn_watchdog_test.go` | Create | Unit tests for `wdTick` and `syncQbtListenPort` |
| `middleware/health.go` | Modify | Add `PortStatus string` to `VPNStatus`; read watchdog state in `queryVPNStatus` |
| `middleware/health_watchdog_test.go` | Create | Tests for `port_status` in health response |
| `middleware/admin_ops.go` | Modify | Add `handleVPNRestart` |
| `middleware/vpn_restart_test.go` | Create | Tests for `handleVPNRestart` |
| `middleware/main.go` | Modify | Launch `StartVPNWatchdog`; register `POST /api/pelicula/admin/vpn/restart` |
| `nginx/dashboard.js` | Modify | Modify `checkVPN` to read `port_status`; add `updateVPNPortBanner`, `restartVPN` |
| `nginx/styles.css` | Modify | Add `.vpn-port-warn-banner` CSS |

---

## Task 1: State machine types and `wdTick` pure function

**Files:**
- Create: `middleware/vpn_watchdog.go`
- Create: `middleware/vpn_watchdog_test.go`

- [ ] **Step 1.1 — Write the failing tests**

Create `middleware/vpn_watchdog_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Ensure test constants match the real ones defined in vpn_watchdog.go.
const testGrace           = gracePolls
const testCooldown        = restartCooldownPolls
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

	origURL := qbtBaseURL
	qbtBaseURL = srv.URL
	t.Cleanup(func() { qbtBaseURL = origURL })

	s := &ServiceClients{client: &http.Client{}}
	syncQbtListenPort(s, 51413)

	wantJSON := `{"listen_port":51413}`
	if gotJSON != wantJSON {
		t.Fatalf("json pref = %q, want %q", gotJSON, wantJSON)
	}
}

func TestSyncQbtListenPort_ToleratesError(t *testing.T) {
	origURL := qbtBaseURL
	qbtBaseURL = "http://127.0.0.1:0" // nothing listening
	t.Cleanup(func() { qbtBaseURL = origURL })

	s := &ServiceClients{client: &http.Client{}}
	syncQbtListenPort(s, 51413) // must not panic
}
```

- [ ] **Step 1.2 — Run tests to confirm they fail**

```bash
cd /Users/gwen/workspace/pelicula/middleware
go test -run "TestWdTick|TestSyncQbt" -v ./...
```

Expected: compilation error — `wdTick`, `wdInternalState`, constants not defined yet.

- [ ] **Step 1.3 — Implement `vpn_watchdog.go`**

Create `middleware/vpn_watchdog.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// ── Timing constants ───────────────────────────────────────────────────────────

const (
	watchdogInterval     = 30 * time.Second
	gracePolls           = 10 // 10 × 30s = 5 min before first restart
	restartCooldownPolls = 3  // 3 × 30s = 90s wait after restart
	postRestartGrace     = 2  // 2 × 30s = 60s post-cooldown grace before degraded
)

// ── State types ────────────────────────────────────────────────────────────────

type watchdogStatus string

const (
	wdUnknown    watchdogStatus = "unknown"
	wdSynced     watchdogStatus = "synced"
	wdGrace      watchdogStatus = "grace"
	wdRestarting watchdogStatus = "restarting"
	wdDegraded   watchdogStatus = "degraded"
)

type watchdogAction int

const (
	wdActNone    watchdogAction = iota
	wdActSync                   // call syncQbtListenPort
	wdActRestart                // restart gluetun + qbittorrent + prowlarr
)

// wdInternalState is mutable state threaded through the pure wdTick function.
type wdInternalState struct {
	status          watchdogStatus
	lastKnownPort   int
	consecutiveZero int
	restartAttempts int
	restartCooldown int // ticks remaining in post-restart cooldown
}

// VPNWatchdogState is the package-level public state read by handleHealth.
type VPNWatchdogState struct {
	PortForwardStatus string
	ForwardedPort     int
	LastSyncedAt      time.Time
	RestartAttempts   int
}

var (
	watchdogMu    sync.RWMutex
	watchdogState VPNWatchdogState
)

// GetWatchdogState returns a snapshot of the current watchdog state.
func GetWatchdogState() VPNWatchdogState {
	watchdogMu.RLock()
	defer watchdogMu.RUnlock()
	return watchdogState
}

// ── Pure state machine ─────────────────────────────────────────────────────────

// wdTick advances the watchdog state machine for one poll cycle.
// port is the currently forwarded port (0 = not forwarded).
// Returns the new internal state and the side-effect action to take.
func wdTick(port int, s wdInternalState) (wdInternalState, watchdogAction) {
	// While in post-restart cooldown: skip port checks entirely.
	if s.restartCooldown > 0 {
		s.restartCooldown--
		return s, wdActNone
	}

	if port > 0 {
		recovering := s.status == wdDegraded || s.status == wdRestarting
		needsSync := s.lastKnownPort != port || recovering
		s.consecutiveZero = 0
		s.lastKnownPort = port
		s.status = wdSynced
		if recovering {
			s.restartAttempts = 0
		}
		if needsSync {
			return s, wdActSync
		}
		return s, wdActNone
	}

	// port == 0

	// Degraded: stay put until port comes back naturally.
	if s.status == wdDegraded {
		return s, wdActNone
	}

	s.consecutiveZero++

	// Post-restart path: shorter tolerance before declaring degraded.
	if s.restartAttempts >= 1 {
		if s.consecutiveZero >= postRestartGrace {
			s.status = wdDegraded
			slog.Warn("port forwarding still unavailable after restart — entering degraded state",
				"component", "vpn_watchdog")
		}
		return s, wdActNone
	}

	// First-time grace period.
	if s.consecutiveZero >= gracePolls {
		s.restartAttempts++
		s.status = wdRestarting
		s.restartCooldown = restartCooldownPolls
		s.consecutiveZero = 0 // reset for post-restart counting
		return s, wdActRestart
	}

	s.status = wdGrace
	return s, wdActNone
}

// ── Port sync ──────────────────────────────────────────────────────────────────

// syncQbtListenPort tells qBittorrent to listen on port via the preferences API.
// Uses form encoding: POST /api/v2/app/setPreferences with json={"listen_port":N}
func syncQbtListenPort(s *ServiceClients, port int) {
	prefs := fmt.Sprintf(`{"listen_port":%d}`, port)
	if err := s.QbtPost("/api/v2/app/setPreferences", "json="+url.QueryEscape(prefs)); err != nil {
		slog.Error("failed to sync qBittorrent listen port",
			"component", "vpn_watchdog", "port", port, "error", err)
		return
	}
	slog.Info("synced qBittorrent listen port", "component", "vpn_watchdog", "port", port)
}

// ── Watchdog goroutine ─────────────────────────────────────────────────────────

// StartVPNWatchdog monitors VPN port forwarding and keeps qBittorrent's listen
// port in sync. Call as a goroutine from main(). Only active when VPN is
// configured (caller should guard on WIREGUARD_PRIVATE_KEY).
func StartVPNWatchdog(s *ServiceClients) {
	ticker := time.NewTicker(watchdogInterval)
	defer ticker.Stop()

	client := &http.Client{Timeout: 5 * time.Second}
	var internal wdInternalState

	slog.Info("started", "component", "vpn_watchdog", "poll_interval", watchdogInterval)

	for range ticker.C {
		port := fetchForwardedPort(client)
		newInternal, action := wdTick(port, internal)
		internal = newInternal

		// Publish state for health endpoint.
		watchdogMu.Lock()
		watchdogState.PortForwardStatus = string(internal.status)
		watchdogState.ForwardedPort = internal.lastKnownPort
		watchdogState.RestartAttempts = internal.restartAttempts
		if action == wdActSync {
			watchdogState.LastSyncedAt = time.Now()
		}
		watchdogMu.Unlock()

		switch action {
		case wdActSync:
			syncQbtListenPort(s, port)
		case wdActRestart:
			slog.Warn("port forwarding unavailable after grace period — restarting VPN containers",
				"component", "vpn_watchdog")
			for _, svc := range []string{"gluetun", "qbittorrent", "prowlarr"} {
				if err := dockerRestart(svc); err != nil {
					slog.Error("vpn watchdog restart failed",
						"component", "vpn_watchdog", "svc", svc, "error", err)
				}
			}
		}
	}
}

// fetchForwardedPort queries gluetun for the currently forwarded port.
// Returns 0 on any error or when no port is assigned.
func fetchForwardedPort(client *http.Client) int {
	body, ok := gluetunGet(client, "/v1/portforward")
	if !ok {
		return 0
	}
	var data struct {
		Port int `json:"port"`
	}
	if json.Unmarshal(body, &data) != nil {
		return 0
	}
	return data.Port
}
```

- [ ] **Step 1.4 — Run tests and confirm they pass**

```bash
cd /Users/gwen/workspace/pelicula/middleware
go test -run "TestWdTick|TestSyncQbt" -v ./...
```

Expected: all tests PASS.

- [ ] **Step 1.5 — Compile check**

```bash
cd /Users/gwen/workspace/pelicula/middleware
go build ./...
```

Expected: no errors.

- [ ] **Step 1.6 — Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add middleware/vpn_watchdog.go middleware/vpn_watchdog_test.go
git commit -m "feat(middleware): add VPN watchdog state machine and port sync"
```

---

## Task 2: Extend `VPNStatus` with `PortStatus` and wire watchdog state into health

**Files:**
- Modify: `middleware/health.go`
- Create: `middleware/health_watchdog_test.go`

- [ ] **Step 2.1 — Write the failing tests**

Create `middleware/health_watchdog_test.go`:

```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQueryVPNStatus_PortStatusDegraded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/publicip/ip":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"public_ip":"1.2.3.4","country":"Netherlands"}`))
		case "/v1/openvpn/portforwarded":
			http.Redirect(w, r, "/v1/portforward", http.StatusMovedPermanently)
		case "/v1/portforward":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"port":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origURL := gluetunControlURL
	gluetunControlURL = srv.URL
	t.Cleanup(func() { gluetunControlURL = origURL })

	watchdogMu.Lock()
	watchdogState = VPNWatchdogState{PortForwardStatus: string(wdDegraded), RestartAttempts: 1}
	watchdogMu.Unlock()
	t.Cleanup(func() {
		watchdogMu.Lock()
		watchdogState = VPNWatchdogState{}
		watchdogMu.Unlock()
	})

	vpn := queryVPNStatus()

	if vpn.PortStatus != "degraded" {
		t.Fatalf("PortStatus = %q, want %q", vpn.PortStatus, "degraded")
	}
	if vpn.Status != "healthy" {
		t.Fatalf("Status = %q, want healthy (tunnel is up even without port forwarding)", vpn.Status)
	}
}

func TestQueryVPNStatus_PortStatusOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/publicip/ip":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"public_ip":"1.2.3.4","country":"Netherlands"}`))
		case "/v1/openvpn/portforwarded":
			http.Redirect(w, r, "/v1/portforward", http.StatusMovedPermanently)
		case "/v1/portforward":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"port":51413}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origURL := gluetunControlURL
	gluetunControlURL = srv.URL
	t.Cleanup(func() { gluetunControlURL = origURL })

	watchdogMu.Lock()
	watchdogState = VPNWatchdogState{
		PortForwardStatus: string(wdSynced),
		ForwardedPort:     51413,
		LastSyncedAt:      time.Now(),
	}
	watchdogMu.Unlock()
	t.Cleanup(func() {
		watchdogMu.Lock()
		watchdogState = VPNWatchdogState{}
		watchdogMu.Unlock()
	})

	vpn := queryVPNStatus()

	if vpn.PortStatus != "ok" {
		t.Fatalf("PortStatus = %q, want %q", vpn.PortStatus, "ok")
	}
	if vpn.Port != 51413 {
		t.Fatalf("Port = %d, want 51413", vpn.Port)
	}
}

func TestVPNStatusJSON_PortStatusField(t *testing.T) {
	v := VPNStatus{Status: "healthy", IP: "1.2.3.4", Port: 51413, PortStatus: "ok"}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal(b, &out)
	if out["port_status"] != "ok" {
		t.Fatalf("port_status = %v, want \"ok\"", out["port_status"])
	}
}
```

- [ ] **Step 2.2 — Run tests to confirm they fail**

```bash
cd /Users/gwen/workspace/pelicula/middleware
go test -run "TestQueryVPNStatus|TestVPNStatusJSON" -v ./...
```

Expected: FAIL — `VPNStatus` has no `PortStatus` field.

- [ ] **Step 2.3 — Update `VPNStatus` and `queryVPNStatus` in `health.go`**

In `middleware/health.go`, replace the `VPNStatus` struct:

```go
type VPNStatus struct {
	Status     string `json:"status"`                // "healthy", "unhealthy", "unknown"
	IP         string `json:"ip,omitempty"`          // public IP via gluetun
	Country    string `json:"country,omitempty"`
	Port       int    `json:"port,omitempty"`        // forwarded port, 0 if not active
	PortStatus string `json:"port_status,omitempty"` // "ok" or "degraded"
}
```

In `queryVPNStatus`, replace the final `return vpn` with:

```go
	// Annotate port_status from watchdog state.
	ws := GetWatchdogState()
	if ws.PortForwardStatus == string(wdDegraded) {
		vpn.PortStatus = "degraded"
	} else if vpn.Port > 0 {
		vpn.PortStatus = "ok"
	}

	return vpn
```

- [ ] **Step 2.4 — Run tests and confirm they pass**

```bash
cd /Users/gwen/workspace/pelicula/middleware
go test -run "TestQueryVPNStatus|TestVPNStatusJSON" -v ./...
```

Expected: all pass.

- [ ] **Step 2.5 — Full compile + all tests**

```bash
cd /Users/gwen/workspace/pelicula/middleware
go build ./... && go test ./...
```

Expected: build OK, all tests pass.

- [ ] **Step 2.6 — Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add middleware/health.go middleware/health_watchdog_test.go
git commit -m "feat(middleware): add port_status to VPN health response"
```

---

## Task 3: `handleVPNRestart` admin handler

**Files:**
- Modify: `middleware/admin_ops.go`
- Create: `middleware/vpn_restart_test.go`

- [ ] **Step 3.1 — Write the failing tests**

Create `middleware/vpn_restart_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleVPNRestart_RejectsGET(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/admin/vpn/restart", nil)
	w := httptest.NewRecorder()
	handleVPNRestart(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestHandleVPNRestart_Post_ReturnsOK(t *testing.T) {
	// With no docker proxy available, dockerRestart errors but handler still
	// returns 200 with ok:true and errors listed — never a 5xx.
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/admin/vpn/restart", nil)
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	handleVPNRestart(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("body does not contain ok:true: %s", w.Body.String())
	}
}
```

- [ ] **Step 3.2 — Run tests to confirm they fail**

```bash
cd /Users/gwen/workspace/pelicula/middleware
go test -run TestHandleVPNRestart -v ./...
```

Expected: FAIL — `handleVPNRestart` not defined.

- [ ] **Step 3.3 — Implement `handleVPNRestart`**

Append to `middleware/admin_ops.go`:

```go
// handleVPNRestart restarts the VPN stack (gluetun, qbittorrent, prowlarr).
// qBittorrent and Prowlarr run on gluetun's network namespace and must be
// restarted alongside it.
// POST /api/pelicula/admin/vpn/restart
func handleVPNRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireAuthOrLocalOrigin(w, r) {
		return
	}
	if !checkAdminRate(w, r) {
		return
	}
	var errs []string
	for _, svc := range []string{"gluetun", "qbittorrent", "prowlarr"} {
		if err := dockerRestart(svc); err != nil {
			slog.Warn("vpn restart: container error",
				"component", "admin_ops", "svc", svc, "error", err)
			errs = append(errs, svc+": "+err.Error())
		}
	}
	result := "ok"
	if len(errs) > 0 {
		result = "partial"
	}
	auditLog(r, "vpn_restart", "gluetun+qbittorrent+prowlarr", result)
	writeJSON(w, map[string]any{"ok": true, "errors": errs})
}
```

- [ ] **Step 3.4 — Run tests and confirm they pass**

```bash
cd /Users/gwen/workspace/pelicula/middleware
go test -run TestHandleVPNRestart -v ./...
```

Expected: both tests PASS.

- [ ] **Step 3.5 — Compile check + full test suite**

```bash
cd /Users/gwen/workspace/pelicula/middleware
go build ./... && go test ./...
```

Expected: no errors, all pass.

- [ ] **Step 3.6 — Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add middleware/admin_ops.go middleware/vpn_restart_test.go
git commit -m "feat(middleware): add VPN restart admin endpoint"
```

---

## Task 4: Wire watchdog and endpoint into `main.go`

**Files:**
- Modify: `middleware/main.go`

- [ ] **Step 4.1 — Launch watchdog goroutine**

In `middleware/main.go`, after the `StartMissingWatcher` goroutine (around line 63):

```go
	// Watch for monitored content missing files and auto-search
	go StartMissingWatcher(services, 2*time.Minute)

	// Monitor VPN port forwarding; keep qBittorrent listen port in sync.
	// Only active when a WireGuard key is present (VPN profile enabled).
	if os.Getenv("WIREGUARD_PRIVATE_KEY") != "" {
		go StartVPNWatchdog(services)
	}
```

- [ ] **Step 4.2 — Register the new admin route**

In `middleware/main.go`, add the route adjacent to the existing admin restart route:

```go
	// admin only: container control via docker-socket-proxy sidecar
	mux.Handle("/api/pelicula/admin/stack/restart", auth.GuardAdmin(http.HandlerFunc(handleStackRestart)))
	mux.Handle("/api/pelicula/admin/vpn/restart", auth.GuardAdmin(http.HandlerFunc(handleVPNRestart)))
	mux.Handle("/api/pelicula/admin/logs", auth.GuardAdmin(http.HandlerFunc(handleServiceLogs)))
```

- [ ] **Step 4.3 — Compile check + full tests**

```bash
cd /Users/gwen/workspace/pelicula/middleware
go build ./... && go test ./...
```

Expected: no errors, all pass.

- [ ] **Step 4.4 — Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add middleware/main.go
git commit -m "feat(middleware): wire VPN watchdog and restart endpoint"
```

---

## Task 5: Dashboard banner, VPN card error state, and CSS

**Files:**
- Modify: `nginx/dashboard.js`
- Modify: `nginx/styles.css`

- [ ] **Step 5.1 — Add CSS for the port-forward warning banner**

In `nginx/styles.css`, after the `.vpn-banner a` block (around line 1256), add:

```css
/* ── VPN port-forward warning banner ───────────────────────────────────── */
.vpn-port-warn-banner {
    padding: 0.6rem 1rem;
    background: rgba(224, 80, 80, 0.08);
    border: 1px solid rgba(224, 80, 80, 0.25);
    border-radius: 6px;
    font-size: 0.82rem;
    color: #e05050;
    margin-bottom: 1rem;
    display: flex;
    align-items: center;
    gap: 0.75rem;
    flex-wrap: wrap;
}
.vpn-port-warn-banner button {
    background: rgba(224, 80, 80, 0.12);
    border: 1px solid rgba(224, 80, 80, 0.3);
    color: #e05050;
    border-radius: 4px;
    padding: 0.2rem 0.6rem;
    font-size: 0.78rem;
    cursor: pointer;
    font-family: inherit;
}
.vpn-port-warn-banner button:hover { background: rgba(224, 80, 80, 0.2); }
.vpn-port-warn-banner button:disabled { opacity: 0.5; cursor: default; }
.vpn-port-warn-banner .banner-dismiss {
    margin-left: auto;
    font-size: 1rem;
    line-height: 1;
    padding: 0.1rem 0.4rem;
}
.vpn-v-error { color: #e05050 !important; }
```

- [ ] **Step 5.2 — Replace `checkVPN` in `dashboard.js`**

In `nginx/dashboard.js`, replace the existing `checkVPN` function (starting at `// ── VPN Telemetry` through the closing `}`) with:

```javascript
// ── VPN Telemetry ─────────────────────────
let _vpnBannerDismissed = false;

async function checkVPN() {
    try {
        const [ipResult, portResult, healthResult] = await Promise.allSettled([
            tfetch('/api/vpn/v1/publicip/ip'),
            tfetch('/api/vpn/v1/portforward'),
            tfetch('/api/pelicula/health')
        ]);

        // Region
        const ipRes = ipResult.status === 'fulfilled' ? ipResult.value : null;
        if (ipRes && ipRes.ok) {
            const data = await ipRes.json();
            setText('s-region', data.country || '\u2014');
        } else if (!ipRes) {
            throw new Error('VPN timeout');
        }

        // Port forwarding status from middleware watchdog
        let portDegraded = false;
        if (healthResult.status === 'fulfilled' && healthResult.value.ok) {
            const hd = await healthResult.value.json();
            portDegraded = hd.vpn?.port_status === 'degraded';
        }

        // Port display — show error text when degraded, numeric port otherwise
        const portEl = document.getElementById('s-port');
        if (portDegraded) {
            if (portEl) {
                portEl.textContent = 'No forwarding';
                portEl.classList.add('vpn-v-error');
            }
        } else {
            if (portEl) portEl.classList.remove('vpn-v-error');
            const portRes = portResult.status === 'fulfilled' ? portResult.value : null;
            if (portRes && portRes.ok) {
                const pd = await portRes.json();
                setText('s-port', pd.port || '\u2014');
            }
        }

        updateVPNPortBanner(portDegraded);
    } catch (e) {
        console.warn('[pelicula] VPN telemetry error:', e);
        setText('s-region', '\u2014');
        setText('s-port', '\u2014');
    }
}
```

- [ ] **Step 5.3 — Add `updateVPNPortBanner` and `restartVPN` after `checkVPN`**

Insert immediately after the closing `}` of the new `checkVPN`:

```javascript
function updateVPNPortBanner(degraded) {
    const bannerId = 'vpn-port-warn-banner';
    let banner = document.getElementById(bannerId);
    if (!degraded) {
        _vpnBannerDismissed = false; // reset so banner re-shows if port degrades again later
        if (banner) banner.remove();
        return;
    }
    if (_vpnBannerDismissed || banner) return; // already showing or dismissed this session

    // Build banner using safe DOM methods (no innerHTML).
    banner = document.createElement('div');
    banner.id = bannerId;
    banner.className = 'vpn-port-warn-banner';

    const msg = document.createTextNode(
        'Port forwarding is unavailable \u2014 download speeds will be limited. '
    );
    banner.appendChild(msg);

    const restartBtn = document.createElement('button');
    restartBtn.textContent = 'Restart VPN';
    restartBtn.addEventListener('click', function() { restartVPN(restartBtn); });
    banner.appendChild(restartBtn);

    const dismissBtn = document.createElement('button');
    dismissBtn.className = 'banner-dismiss';
    dismissBtn.textContent = '\u00d7';
    dismissBtn.addEventListener('click', function() {
        _vpnBannerDismissed = true;
        banner.remove();
    });
    banner.appendChild(dismissBtn);

    const pipelineSection = document.getElementById('pipeline-section');
    if (pipelineSection) {
        pipelineSection.insertAdjacentElement('beforebegin', banner);
    } else {
        (document.querySelector('.main-content') || document.body).prepend(banner);
    }
}

async function restartVPN(btn) {
    btn.disabled = true;
    btn.textContent = 'Restarting\u2026';
    try {
        const res = await tfetch('/api/pelicula/admin/vpn/restart', { method: 'POST' }, 35000);
        if (res && res.ok) {
            btn.textContent = 'Restarted';
        } else {
            btn.textContent = 'Failed';
            btn.disabled = false;
        }
    } catch (e) {
        btn.textContent = 'Failed';
        btn.disabled = false;
    }
}
```

- [ ] **Step 5.4 — Restart nginx to pick up static file changes**

```bash
cd /Users/gwen/workspace/pelicula
docker compose restart nginx
```

- [ ] **Step 5.5 — Rebuild middleware**

```bash
cd /Users/gwen/workspace/pelicula
docker compose build pelicula-api && docker compose up -d pelicula-api
```

- [ ] **Step 5.6 — Manual verification**

Open `http://localhost:7354` in a browser. Run `pelicula check-vpn` to confirm current port status.

To verify degraded UI without waiting 5 minutes for grace to expire, temporarily reduce `gracePolls` to `2` in `vpn_watchdog.go`, rebuild, and wait 60s with port=0. Confirm:
- `#s-port` shows "No forwarding" in red
- Yellow-red banner appears above the pipeline board
- Banner "×" button removes it; banner does not re-appear within the same page session
- Banner "Restart VPN" button shows spinner → "Restarted"
- After restoring `gracePolls = 10`, rebuild

Then simulate recovery: manually restart gluetun (`docker compose restart gluetun`) and confirm:
- Port number appears in `#s-port` within one 30s watchdog cycle
- Banner disappears within one 15s refresh cycle

- [ ] **Step 5.7 — Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add nginx/dashboard.js nginx/styles.css
git commit -m "feat(dashboard): VPN port-forward degraded banner and red card state"
```

---

## Self-Review

**Spec coverage:**

| Requirement | Task |
|-------------|------|
| Detect port=0 persisting | Task 1 — `wdTick` grace period |
| Auto-restart once after grace | Task 1 — `wdActRestart` |
| 90s cooldown after restart | Task 1 — `restartCooldownPolls` |
| Degrade after failed restart | Task 1 — `wdDegraded` state |
| Stay degraded until port returns | Task 1 — `TestWdTick_DegradedStaysUntilPortReturns` |
| Recover from degraded, reset restart counter | Task 1 — `TestWdTick_RecoveryFromDegraded` |
| No double restart | Task 1 — `TestWdTick_NoDoubleRestart` |
| Sync qBittorrent listen port on change | Task 1 — `syncQbtListenPort` + `TestSyncQbt*` |
| `port_status` in health response | Task 2 |
| `handleVPNRestart` endpoint | Task 3 |
| Only fires watchdog when VPN configured | Task 4 — `WIREGUARD_PRIVATE_KEY` guard |
| Dashboard VPN card red on degraded | Task 5 — `.vpn-v-error` class on `#s-port` |
| Dismissible banner above pipeline | Task 5 — `updateVPNPortBanner` |
| Banner auto-clears on recovery | Task 5 — `_vpnBannerDismissed = false` reset |
| Restart VPN button in banner | Task 5 — `restartVPN` |
| Safe DOM construction (no innerHTML) | Task 5 — `createElement`/`createTextNode` |

All requirements covered. No placeholders or TBDs. Type names (`wdInternalState`, `watchdogAction`, `wdActSync`, etc.) consistent across all tasks.
