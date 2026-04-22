package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// stubServices implements ServiceChecker for tests.
type stubServices struct {
	health map[string]string
	wired  bool
}

func (s *stubServices) CheckHealth() map[string]string { return s.health }
func (s *stubServices) IsWired() bool                  { return s.wired }

// newGluetunServer returns a test server that responds to the Gluetun
// /v1/publicip/ip and /v1/openvpn/portforwarded endpoints.
func newGluetunServer(t *testing.T, ip, country string, port int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/publicip/ip":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"public_ip": ip,
				"country":   country,
			})
		case "/v1/openvpn/portforwarded":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int{"port": port})
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestHealthHandler_Healthy verifies the happy path: all services up,
// watchdog synced, VPN healthy with a forwarded port.
func TestHealthHandler_Healthy(t *testing.T) {
	gluetun := newGluetunServer(t, "1.2.3.4", "Netherlands", 51413)
	defer gluetun.Close()

	h := &Handler{
		Services: &stubServices{
			health: map[string]string{"sonarr": "up", "radarr": "up"},
			wired:  true,
		},
		GetWatchdog: func() WatchdogState {
			return WatchdogState{
				PortForwardStatus: "synced",
				ForwardedPort:     51413,
				LastSyncedAt:      time.Now(),
			}
		},
		GluetunBaseURL: gluetun.URL,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.VPN.Status != "healthy" {
		t.Errorf("vpn.status = %q, want \"healthy\"", resp.VPN.Status)
	}
	if resp.VPN.IP != "1.2.3.4" {
		t.Errorf("vpn.ip = %q, want \"1.2.3.4\"", resp.VPN.IP)
	}
	if resp.VPN.Port != 51413 {
		t.Errorf("vpn.port = %d, want 51413", resp.VPN.Port)
	}
	if resp.VPN.PortStatus != "ok" {
		t.Errorf("vpn.port_status = %q, want \"ok\"", resp.VPN.PortStatus)
	}
	if !resp.Wired {
		t.Error("wired = false, want true")
	}
	if resp.Services["sonarr"] != "up" {
		t.Errorf("services[sonarr] = %q, want \"up\"", resp.Services["sonarr"])
	}

	// VPN (3) + 2 services = 5 total; all pass.
	if resp.ChecksTotal != 5 {
		t.Errorf("checks_total = %d, want 5", resp.ChecksTotal)
	}
	if resp.ChecksPassed != 5 {
		t.Errorf("checks_passed = %d, want 5", resp.ChecksPassed)
	}

	if resp.VPN.Watchdog == nil {
		t.Fatal("watchdog should be present when GetWatchdog returns non-empty status")
	}
	if resp.VPN.Watchdog.Status != "synced" {
		t.Errorf("watchdog.status = %q, want \"synced\"", resp.VPN.Watchdog.Status)
	}
}

// TestHealthHandler_Degraded verifies that a service returning "down" is
// reflected in checks_passed / checks_total.
func TestHealthHandler_Degraded(t *testing.T) {
	gluetun := newGluetunServer(t, "1.2.3.4", "Netherlands", 51413)
	defer gluetun.Close()

	h := &Handler{
		Services: &stubServices{
			health: map[string]string{"sonarr": "up", "radarr": "down"},
			wired:  true,
		},
		GetWatchdog:    nil,
		GluetunBaseURL: gluetun.URL,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// VPN (3) + 2 services = 5 total; radarr down → only 4 pass.
	if resp.ChecksTotal != 5 {
		t.Errorf("checks_total = %d, want 5", resp.ChecksTotal)
	}
	if resp.ChecksPassed != 4 {
		t.Errorf("checks_passed = %d, want 4 (radarr down)", resp.ChecksPassed)
	}
	if resp.Services["radarr"] != "down" {
		t.Errorf("services[radarr] = %q, want \"down\"", resp.Services["radarr"])
	}
	if resp.VPN.Watchdog != nil {
		t.Error("watchdog should be nil when GetWatchdog is nil")
	}
}

// TestHealthHandler_VPNDown verifies that a watchdog reporting "degraded"
// surfaces the degraded port_status and watchdog object.
func TestHealthHandler_VPNDown(t *testing.T) {
	gluetun := newGluetunServer(t, "5.6.7.8", "Sweden", 0)
	defer gluetun.Close()

	h := &Handler{
		Services: &stubServices{
			health: map[string]string{"sonarr": "up"},
			wired:  true,
		},
		GetWatchdog: func() WatchdogState {
			return WatchdogState{
				PortForwardStatus: "degraded",
				RestartAttempts:   2,
				ConsecutiveZero:   10,
				VPNTunnelStatus:   "running",
			}
		},
		GluetunBaseURL: gluetun.URL,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.VPN.PortStatus != "degraded" {
		t.Errorf("vpn.port_status = %q, want \"degraded\"", resp.VPN.PortStatus)
	}
	if resp.VPN.Watchdog == nil {
		t.Fatal("watchdog should be present")
	}
	if resp.VPN.Watchdog.Status != "degraded" {
		t.Errorf("watchdog.status = %q, want \"degraded\"", resp.VPN.Watchdog.Status)
	}
	if resp.VPN.Watchdog.RestartAttempts != 2 {
		t.Errorf("watchdog.restart_attempts = %d, want 2", resp.VPN.Watchdog.RestartAttempts)
	}
	if resp.VPN.Watchdog.TunnelStatus != "running" {
		t.Errorf("watchdog.tunnel_status = %q, want \"running\"", resp.VPN.Watchdog.TunnelStatus)
	}
	// Port = 0 → vpn.port check fails; VPN IP present → healthy + IP pass.
	// VPN (3) + 1 service = 4 total; port=0 fails → 3 pass.
	if resp.ChecksPassed != 3 {
		t.Errorf("checks_passed = %d, want 3", resp.ChecksPassed)
	}
}

// TestHealthHandler_NoWatchdog verifies that when GetWatchdog is nil
// the watchdog field is absent from the response.
func TestHealthHandler_NoWatchdog(t *testing.T) {
	gluetun := newGluetunServer(t, "9.9.9.9", "Germany", 12345)
	defer gluetun.Close()

	h := &Handler{
		Services: &stubServices{
			health: map[string]string{"jellyfin": "up"},
			wired:  false,
		},
		GetWatchdog:    nil,
		GluetunBaseURL: gluetun.URL,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var resp Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.VPN.Watchdog != nil {
		t.Error("watchdog should be nil when GetWatchdog is nil")
	}
	// port_status not set from watchdog path — should be empty string.
	if resp.VPN.PortStatus != "" {
		t.Errorf("vpn.port_status = %q, want empty when no watchdog", resp.VPN.PortStatus)
	}
	if resp.Wired {
		t.Error("wired = true, want false")
	}
}

// TestHealthHandler_MethodNotAllowed verifies POST returns 405.
func TestHealthHandler_MethodNotAllowed(t *testing.T) {
	h := &Handler{
		Services: &stubServices{
			health: map[string]string{},
		},
		GluetunBaseURL: "http://127.0.0.1:0", // won't be reached
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// TestHealthHandler_GluetunUnavailable verifies graceful handling when
// Gluetun cannot be reached (VPN not configured or down).
func TestHealthHandler_GluetunUnavailable(t *testing.T) {
	h := &Handler{
		Services: &stubServices{
			health: map[string]string{"sonarr": "up"},
			wired:  true,
		},
		GetWatchdog:    nil,
		GluetunBaseURL: "http://127.0.0.1:0", // nothing listening
	}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (graceful degradation)", rec.Code)
	}

	var resp Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// VPN status stays "unknown" when Gluetun is unreachable.
	if resp.VPN.Status != "unknown" {
		t.Errorf("vpn.status = %q, want \"unknown\"", resp.VPN.Status)
	}
	if resp.VPN.IP != "" {
		t.Errorf("vpn.ip = %q, want empty when Gluetun unreachable", resp.VPN.IP)
	}
	// VPN checks: status != healthy (0), IP empty (0), port=0 (0) → 0 VPN points.
	// 1 service up → checks_passed = 1, checks_total = 4.
	if resp.ChecksTotal != 4 {
		t.Errorf("checks_total = %d, want 4", resp.ChecksTotal)
	}
	if resp.ChecksPassed != 1 {
		t.Errorf("checks_passed = %d, want 1", resp.ChecksPassed)
	}
}

// TestHealthHandler_InjectedClient verifies that when Handler.Client is set
// the injected client is used (the request reaches the server) rather than
// creating a new one. This is the regression test for the F21 migration that
// moved client construction out of queryVPNStatus.
func TestHealthHandler_InjectedClient(t *testing.T) {
	gluetun := newGluetunServer(t, "2.3.4.5", "Canada", 55000)
	defer gluetun.Close()

	// Inject a custom client with a short timeout — must still succeed.
	injected := &http.Client{Timeout: 5 * time.Second}

	h := &Handler{
		Services:       &stubServices{health: map[string]string{}},
		GluetunBaseURL: gluetun.URL,
		Client:         injected,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp Response
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.VPN.IP != "2.3.4.5" {
		t.Errorf("vpn.ip = %q, want %q (injected client should reach server)", resp.VPN.IP, "2.3.4.5")
	}
}
