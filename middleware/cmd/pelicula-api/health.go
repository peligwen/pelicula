package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"pelicula-api/httputil"
	"time"
)

// WatchdogInfo carries watchdog diagnostic fields surfaced in the health
// response. Only included when the watchdog is active.
type WatchdogInfo struct {
	Status            string    `json:"status"`
	ConsecutiveZero   int       `json:"consecutive_zero,omitempty"`
	GraceRemaining    int       `json:"grace_remaining,omitempty"`
	CooldownRemaining int       `json:"cooldown_remaining,omitempty"`
	RestartAttempts   int       `json:"restart_attempts,omitempty"`
	LastTransition    time.Time `json:"last_transition,omitempty"`
	TunnelStatus      string    `json:"tunnel_status,omitempty"`
}

type VPNStatus struct {
	Status     string        `json:"status"`       // "healthy", "unhealthy", "unknown"
	IP         string        `json:"ip,omitempty"` // public IP via gluetun
	Country    string        `json:"country,omitempty"`
	Port       int           `json:"port,omitempty"`        // forwarded port, 0 if not active
	PortStatus string        `json:"port_status,omitempty"` // "ok" or "degraded"
	Watchdog   *WatchdogInfo `json:"watchdog,omitempty"`    // watchdog internals; nil when VPN not configured
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
	if body, err := gluetunGet(client, "/v1/publicip/ip"); err == nil {
		var data struct {
			PublicIP string `json:"public_ip"`
			Country  string `json:"country"`
		}
		if json.Unmarshal(body, &data) == nil && data.PublicIP != "" {
			vpn.IP = data.PublicIP
			vpn.Country = data.Country
			vpn.Status = "healthy"
		}
	} else {
		slog.Debug("gluetun public IP unavailable", "component", "health", "error", err)
	}

	// Forwarded port
	if body, err := gluetunGet(client, "/v1/openvpn/portforwarded"); err == nil {
		var data struct {
			Port int `json:"port"`
		}
		if json.Unmarshal(body, &data) == nil {
			vpn.Port = data.Port
		}
	} else {
		slog.Debug("gluetun port forward status unavailable", "component", "health", "error", err)
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

	// Populate watchdog diagnostics when the watchdog is active.
	if ws.PortForwardStatus != "" {
		vpn.Watchdog = &WatchdogInfo{
			Status:            ws.PortForwardStatus,
			ConsecutiveZero:   ws.ConsecutiveZero,
			GraceRemaining:    ws.GraceRemaining,
			CooldownRemaining: ws.CooldownRemaining,
			RestartAttempts:   ws.RestartAttempts,
			LastTransition:    ws.LastTransitionAt,
			TunnelStatus:      ws.VPNTunnelStatus,
		}
	}

	return vpn
}

// gluetunControlURL is the base URL for the Gluetun control API (port 8000).
var gluetunControlURL = envOr("GLUETUN_CONTROL_URL", "http://gluetun:8000")

// gluetunGet makes a GET request to the Gluetun control API and returns the
// response body. Returns a descriptive error on transport failures or non-200
// responses. Includes HTTP Basic Auth when GLUETUN_HTTP_PASS is set.
func gluetunGet(client *http.Client, path string) ([]byte, error) {
	req, err := http.NewRequest("GET", gluetunControlURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if pass := os.Getenv("GLUETUN_HTTP_PASS"); pass != "" {
		user := envOr("GLUETUN_HTTP_USER", "pelicula")
		req.SetBasicAuth(user, pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, path)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}
