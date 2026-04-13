package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"pelicula-api/httputil"
	"time"
)

type VPNStatus struct {
	Status     string `json:"status"`       // "healthy", "unhealthy", "unknown"
	IP         string `json:"ip,omitempty"` // public IP via gluetun
	Country    string `json:"country,omitempty"`
	Port       int    `json:"port,omitempty"`        // forwarded port, 0 if not active
	PortStatus string `json:"port_status,omitempty"` // "ok" or "degraded"
}

type HealthResponse struct {
	VPN          VPNStatus         `json:"vpn"`
	Services     map[string]string `json:"services"`
	Wired        bool              `json:"wired"`
	ChecksPassed int               `json:"checks_passed"`
	ChecksTotal  int               `json:"checks_total"`
}

// handleHealth performs a comprehensive health check of the stack.
// Called by bash `./pelicula check-vpn` and can also be polled by the dashboard.
// No auth required — bash calls this without a session cookie.
func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	vpn := queryVPNStatus()
	svcs := services.CheckHealth()
	wired := services.IsWired()

	passed := 0
	total := 0

	// VPN checks (3 points)
	total += 3
	if vpn.Status == "healthy" {
		passed++
	}
	if vpn.IP != "" {
		passed++
	}
	if vpn.Port > 0 {
		passed++
	}

	// Service checks (one per service)
	for _, status := range svcs {
		total++
		if status == "up" {
			passed++
		}
	}

	resp := HealthResponse{
		VPN:          vpn,
		Services:     svcs,
		Wired:        wired,
		ChecksPassed: passed,
		ChecksTotal:  total,
	}

	slog.Info("health check", "component", "health", "passed", passed, "total", total)
	httputil.WriteJSON(w, resp)
}

// queryVPNStatus queries the Gluetun control API (port 8000) for VPN status,
// public IP, and forwarded port. All reachable via the Docker internal network.
func queryVPNStatus() VPNStatus {
	client := &http.Client{Timeout: 5 * time.Second}
	vpn := VPNStatus{Status: "unknown"}

	// Public IP and country
	if body, ok := gluetunGet(client, "/v1/publicip/ip"); ok {
		var data struct {
			PublicIP string `json:"public_ip"`
			Country  string `json:"country"`
		}
		if json.Unmarshal(body, &data) == nil && data.PublicIP != "" {
			vpn.IP = data.PublicIP
			vpn.Country = data.Country
			vpn.Status = "healthy"
		}
	}

	// Forwarded port
	if body, ok := gluetunGet(client, "/v1/openvpn/portforwarded"); ok {
		var data struct {
			Port int `json:"port"`
		}
		if json.Unmarshal(body, &data) == nil {
			vpn.Port = data.Port
		}
	}

	// Annotate port_status from watchdog state. The watchdog is the authority —
	// transient states (grace, restarting, unknown) leave port_status empty.
	ws := GetWatchdogState()
	switch ws.PortForwardStatus {
	case string(wdDegraded):
		vpn.PortStatus = "degraded"
	case string(wdSynced):
		if vpn.Port > 0 {
			vpn.PortStatus = "ok"
		}
	}

	return vpn
}

// gluetunControlURL is the base URL for the Gluetun control API (port 8000).
var gluetunControlURL = envOr("GLUETUN_CONTROL_URL", "http://gluetun:8000")

// gluetunGet makes a GET request to the Gluetun control API and returns the
// response body. Returns (nil, false) on any error or non-200 status.
// Includes HTTP Basic Auth when GLUETUN_HTTP_PASS is set.
func gluetunGet(client *http.Client, path string) ([]byte, bool) {
	req, err := http.NewRequest("GET", gluetunControlURL+path, nil)
	if err != nil {
		return nil, false
	}
	if pass := os.Getenv("GLUETUN_HTTP_PASS"); pass != "" {
		user := envOr("GLUETUN_HTTP_USER", "pelicula")
		req.SetBasicAuth(user, pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false
	}
	return body, true
}
