package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWatchdogState_NewFieldsReadable verifies the new diagnostic fields on
// VPNWatchdogState are readable via GetWatchdogState.
func TestWatchdogState_NewFieldsReadable(t *testing.T) {
	watchdogMu.Lock()
	watchdogState = VPNWatchdogState{
		PortForwardStatus: string(wdGrace),
		ConsecutiveZero:   4,
		GraceRemaining:    6,
		CooldownRemaining: 0,
		VPNTunnelStatus:   "running",
	}
	watchdogMu.Unlock()
	t.Cleanup(func() {
		watchdogMu.Lock()
		watchdogState = VPNWatchdogState{}
		watchdogMu.Unlock()
	})

	st := GetWatchdogState()
	if st.ConsecutiveZero != 4 {
		t.Fatalf("ConsecutiveZero = %d, want 4", st.ConsecutiveZero)
	}
	if st.GraceRemaining != 6 {
		t.Fatalf("GraceRemaining = %d, want 6", st.GraceRemaining)
	}
	if st.VPNTunnelStatus != "running" {
		t.Fatalf("VPNTunnelStatus = %q, want running", st.VPNTunnelStatus)
	}
}

// TestQueryVPNStatus_WatchdogDetailsPopulated verifies that watchdog internals
// (grace progress, tunnel status) are surfaced in the health response.
func TestQueryVPNStatus_WatchdogDetailsPopulated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/publicip/ip":
			w.Write([]byte(`{"public_ip":"1.2.3.4","country":"Netherlands"}`))
		case "/v1/openvpn/portforwarded", "/v1/portforward":
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
	watchdogState = VPNWatchdogState{
		PortForwardStatus: string(wdGrace),
		ConsecutiveZero:   4,
		GraceRemaining:    6,
		VPNTunnelStatus:   "running",
	}
	watchdogMu.Unlock()
	t.Cleanup(func() {
		watchdogMu.Lock()
		watchdogState = VPNWatchdogState{}
		watchdogMu.Unlock()
	})

	vpn := queryVPNStatus()

	if vpn.Watchdog == nil {
		t.Fatal("Watchdog is nil, want non-nil")
	}
	if vpn.Watchdog.Status != "grace" {
		t.Fatalf("Watchdog.Status = %q, want grace", vpn.Watchdog.Status)
	}
	if vpn.Watchdog.ConsecutiveZero != 4 {
		t.Fatalf("Watchdog.ConsecutiveZero = %d, want 4", vpn.Watchdog.ConsecutiveZero)
	}
	if vpn.Watchdog.GraceRemaining != 6 {
		t.Fatalf("Watchdog.GraceRemaining = %d, want 6", vpn.Watchdog.GraceRemaining)
	}
	if vpn.Watchdog.TunnelStatus != "running" {
		t.Fatalf("Watchdog.TunnelStatus = %q, want running", vpn.Watchdog.TunnelStatus)
	}
}

// TestVPNStatusJSON_WatchdogObjectIncluded verifies the watchdog sub-object
// appears in the JSON health response with the expected field names.
func TestVPNStatusJSON_WatchdogObjectIncluded(t *testing.T) {
	v := VPNStatus{
		Status: "healthy",
		Watchdog: &WatchdogInfo{
			Status:          "grace",
			ConsecutiveZero: 4,
			GraceRemaining:  6,
			TunnelStatus:    "running",
		},
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	wd, ok := out["watchdog"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'watchdog' key in JSON output, got keys: %v", out)
	}
	if wd["status"] != "grace" {
		t.Fatalf("watchdog.status = %v, want grace", wd["status"])
	}
	if wd["consecutive_zero"].(float64) != 4 {
		t.Fatalf("watchdog.consecutive_zero = %v, want 4", wd["consecutive_zero"])
	}
	if wd["tunnel_status"] != "running" {
		t.Fatalf("watchdog.tunnel_status = %v, want running", wd["tunnel_status"])
	}
}

// TestQueryVPNStatus_WatchdogNilWhenUnknown verifies Watchdog is omitted
// (nil) when the watchdog hasn't started or state is empty.
func TestQueryVPNStatus_WatchdogNilWhenUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/publicip/ip":
			w.Write([]byte(`{"public_ip":"1.2.3.4","country":"Netherlands"}`))
		case "/v1/openvpn/portforwarded", "/v1/portforward":
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
	watchdogState = VPNWatchdogState{} // empty / VPN not configured
	watchdogMu.Unlock()
	t.Cleanup(func() {
		watchdogMu.Lock()
		watchdogState = VPNWatchdogState{}
		watchdogMu.Unlock()
	})

	vpn := queryVPNStatus()

	if vpn.Watchdog != nil {
		t.Fatalf("expected Watchdog to be nil when watchdog state is empty, got %+v", vpn.Watchdog)
	}
}
