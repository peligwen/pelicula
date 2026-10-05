package api

import (
	"errors"
	"testing"
	"time"

	"pelicula/internal/store"
)

func serviceMap(t *testing.T, resp map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, raw := range resp["services"].([]any) {
		svc := raw.(map[string]any)
		out[svc["name"].(string)] = svc
	}
	return out
}

func TestStatusVPNOff(t *testing.T) {
	e := newEnv(t)
	e.radarr.pingErr = errors.New("connection refused")
	if err := e.store.EnqueueJob(t.Context(), &store.Job{ArrType: "radarr", ArrID: 1, Title: "A", Path: "/media/a.mkv"}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.CreateRequest(t.Context(), &store.Request{MediaType: "movie", TmdbID: 1, Title: "A", RequestedBy: "vera"}); err != nil {
		t.Fatal(err)
	}

	rec := e.do(viewer, "GET", "/api/status", nil)
	wantStatus(t, rec, 200)
	got := decode(t, rec)

	svcs := serviceMap(t, got)
	if len(svcs) != 3 {
		t.Fatalf("services = %v, want sonarr, radarr, jellyfin only", svcs)
	}
	if svcs["sonarr"]["ok"] != true || svcs["sonarr"]["path"] != "/sonarr/" {
		t.Errorf("sonarr = %v", svcs["sonarr"])
	}
	if svcs["radarr"]["ok"] != false {
		t.Errorf("radarr should be down: %v", svcs["radarr"])
	}
	if svcs["jellyfin"]["path"] != "/jellyfin/" {
		t.Errorf("jellyfin = %v", svcs["jellyfin"])
	}
	vpn := got["vpn"].(map[string]any)
	if vpn["enabled"] != false || vpn["public_ip"] != "" || vpn["forwarded_port"] != float64(0) {
		t.Errorf("vpn = %v", vpn)
	}
	if got["wired"] != true || got["version"] != "test-1.0" {
		t.Errorf("wired/version = %v / %v", got["wired"], got["version"])
	}
	if got["queued_jobs"] != float64(1) {
		t.Errorf("queued_jobs = %v, want 1", got["queued_jobs"])
	}
	if got["pending_requests"] != float64(0) {
		t.Errorf("viewer must not see pending_requests, got %v", got["pending_requests"])
	}
}

func TestStatusPendingRequestsForManagers(t *testing.T) {
	e := newEnv(t)
	if err := e.store.CreateRequest(t.Context(), &store.Request{MediaType: "movie", TmdbID: 1, Title: "A", RequestedBy: "vera"}); err != nil {
		t.Fatal(err)
	}
	got := decode(t, e.do(manager, "GET", "/api/status", nil))
	if got["pending_requests"] != float64(1) {
		t.Fatalf("pending_requests = %v, want 1", got["pending_requests"])
	}
}

func TestStatusVPNOn(t *testing.T) {
	e := newEnv(t)
	e.srv.Cfg.VPNEnabled = true
	e.srv.Prowlarr = &fakeArr{}
	e.srv.QBT = &fakeQBT{pingErr: errors.New("down")}
	e.srv.Gluetun = &fakeGluetun{tunnel: "running", ip: &VPNStatus{PublicIP: "203.0.113.9", Country: "Netherlands", City: "Amsterdam"}, port: 51820}

	got := decode(t, e.do(viewer, "GET", "/api/status", nil))
	svcs := serviceMap(t, got)
	if len(svcs) != 5 {
		t.Fatalf("services = %v, want 5", svcs)
	}
	if svcs["prowlarr"]["path"] != "/prowlarr/" || svcs["prowlarr"]["ok"] != true {
		t.Errorf("prowlarr = %v", svcs["prowlarr"])
	}
	if svcs["qbittorrent"]["path"] != "/qbt/" || svcs["qbittorrent"]["ok"] != false {
		t.Errorf("qbittorrent = %v", svcs["qbittorrent"])
	}
	vpn := got["vpn"].(map[string]any)
	if vpn["enabled"] != true || vpn["tunnel"] != "running" || vpn["public_ip"] != "203.0.113.9" ||
		vpn["country"] != "Netherlands" || vpn["forwarded_port"] != float64(51820) {
		t.Errorf("vpn = %v", vpn)
	}
}

func TestStatusVPNOnWithMissingClients(t *testing.T) {
	e := newEnv(t)
	e.srv.Cfg.VPNEnabled = true // Prowlarr, QBT, Gluetun left nil

	got := decode(t, e.do(viewer, "GET", "/api/status", nil))
	svcs := serviceMap(t, got)
	if svcs["qbittorrent"]["ok"] != false || svcs["prowlarr"]["ok"] != false {
		t.Errorf("nil clients must report down: %v", svcs)
	}
	if got["vpn"].(map[string]any)["tunnel"] != "unknown" {
		t.Errorf("vpn = %v", got["vpn"])
	}
}

func TestStatusIsCachedBriefly(t *testing.T) {
	e := newEnv(t)
	e.do(viewer, "GET", "/api/status", nil)
	e.do(viewer, "GET", "/api/status", nil)
	if e.sonarr.pings != 1 {
		t.Fatalf("sonarr pinged %d times across two requests, want 1 (5s cache)", e.sonarr.pings)
	}

	e.srv.statusMu.Lock()
	e.srv.statusAt = time.Now().Add(-statusCacheTTL - time.Second)
	e.srv.statusMu.Unlock()
	e.do(viewer, "GET", "/api/status", nil)
	if e.sonarr.pings != 2 {
		t.Fatalf("sonarr pings = %d after expiry, want 2", e.sonarr.pings)
	}
}
