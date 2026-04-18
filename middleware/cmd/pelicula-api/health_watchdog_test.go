package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pelicula-api/internal/app/vpnwatchdog"
	gluetunclient "pelicula-api/internal/clients/gluetun"
)

func TestQueryVPNStatus_PortStatusDegraded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/publicip/ip":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"public_ip":"1.2.3.4","country":"Netherlands"}`))
		case "/v1/openvpn/portforwarded":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"port":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	old := gluetunClient
	gluetunClient = gluetunclient.New(srv.URL, "", "")
	t.Cleanup(func() { gluetunClient = old })

	state := vpnwatchdog.State{PortForwardStatus: "degraded", RestartAttempts: 1}
	vpn := queryVPNStatus(func() vpnwatchdog.State { return state })

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
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"port":51413}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	old := gluetunClient
	gluetunClient = gluetunclient.New(srv.URL, "", "")
	t.Cleanup(func() { gluetunClient = old })

	state := vpnwatchdog.State{
		PortForwardStatus: "synced",
		ForwardedPort:     51413,
		LastSyncedAt:      time.Now(),
	}
	vpn := queryVPNStatus(func() vpnwatchdog.State { return state })

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
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out["port_status"] != "ok" {
		t.Fatalf("port_status = %v, want \"ok\"", out["port_status"])
	}
}
