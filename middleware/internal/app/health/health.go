// Package health implements health check probes and VPN status queries
// for the pelicula-api service.
package health

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// ServiceChecker is the subset of ServiceClients that health checks need.
type ServiceChecker interface {
	CheckHealth() map[string]string
	IsWired() bool
}

// WatchdogState is the public state snapshot provided by the VPN watchdog.
// Mirrors the VPNWatchdogState struct from the main package.
type WatchdogState struct {
	PortForwardStatus string
	ForwardedPort     int
	LastSyncedAt      time.Time
	RestartAttempts   int
	ConsecutiveZero   int
	GraceRemaining    int
	CooldownRemaining int
	LastTransitionAt  time.Time
	VPNTunnelStatus   string
}

// WatchdogStateFunc is a function that returns the current watchdog state.
// The main package wires in GetWatchdogState() here.
type WatchdogStateFunc func() WatchdogState

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

// VPNStatus describes the VPN health result.
type VPNStatus struct {
	Status     string        `json:"status"`
	IP         string        `json:"ip,omitempty"`
	Country    string        `json:"country,omitempty"`
	Port       int           `json:"port,omitempty"`
	PortStatus string        `json:"port_status,omitempty"`
	Watchdog   *WatchdogInfo `json:"watchdog,omitempty"`
}

// Response is the full health check response.
type Response struct {
	VPN          VPNStatus         `json:"vpn"`
	Services     map[string]string `json:"services"`
	Wired        bool              `json:"wired"`
	ChecksPassed int               `json:"checks_passed"`
	ChecksTotal  int               `json:"checks_total"`
}

// Handler is the health check HTTP handler. It holds injected dependencies
// to avoid package-level globals.
type Handler struct {
	Services       ServiceChecker
	GetWatchdog    WatchdogStateFunc
	GluetunBaseURL string
	GluetunUser    string
	GluetunPass    string
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	vpn := h.queryVPNStatus()
	svcs := h.Services.CheckHealth()
	wired := h.Services.IsWired()

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

	resp := Response{
		VPN:          vpn,
		Services:     svcs,
		Wired:        wired,
		ChecksPassed: passed,
		ChecksTotal:  total,
	}

	slog.Info("health check", "component", "health", "passed", passed, "total", total)
	writeJSON(w, resp)
}

// queryVPNStatus queries the Gluetun control API for VPN status, public IP,
// and forwarded port.
func (h *Handler) queryVPNStatus() VPNStatus {
	client := &http.Client{Timeout: 5 * time.Second}
	vpn := VPNStatus{Status: "unknown"}

	gluetunURL := h.GluetunBaseURL
	if gluetunURL == "" {
		gluetunURL = "http://gluetun:8000"
	}

	gluetunGet := func(path string) ([]byte, error) {
		return h.gluetunGet(client, gluetunURL+path)
	}

	// Public IP and country
	if body, err := gluetunGet("/v1/publicip/ip"); err == nil {
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
	if body, err := gluetunGet("/v1/openvpn/portforwarded"); err == nil {
		var data struct {
			Port int `json:"port"`
		}
		if json.Unmarshal(body, &data) == nil {
			vpn.Port = data.Port
		}
	} else {
		slog.Debug("gluetun port forward status unavailable", "component", "health", "error", err)
	}

	// Annotate port_status from watchdog state.
	if h.GetWatchdog != nil {
		ws := h.GetWatchdog()
		switch ws.PortForwardStatus {
		case "degraded":
			vpn.PortStatus = "degraded"
		case "synced":
			if vpn.Port > 0 {
				vpn.PortStatus = "ok"
			}
		}

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
	}

	return vpn
}

// gluetunGet makes a GET request to the Gluetun control API.
func (h *Handler) gluetunGet(client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	pass := h.GluetunPass
	if pass == "" {
		pass = os.Getenv("GLUETUN_HTTP_PASS")
	}
	if pass != "" {
		user := h.GluetunUser
		if user == "" {
			user = os.Getenv("GLUETUN_HTTP_USER")
		}
		if user == "" {
			user = "pelicula"
		}
		req.SetBasicAuth(user, pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
