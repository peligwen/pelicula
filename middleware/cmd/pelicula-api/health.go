package main

import (
	"context"
	"log/slog"
	"net/http"
	"pelicula-api/httputil"
	gluetunclient "pelicula-api/internal/clients/gluetun"
	"time"
)

// gluetunClient is the typed control-API client for Gluetun.
// The optional HTTP Basic Auth credentials (GLUETUN_HTTP_PASS / GLUETUN_HTTP_USER)
// are read once at startup; the control-API URL comes from GLUETUN_CONTROL_URL.
var gluetunClient = gluetunclient.New(
	envOr("GLUETUN_CONTROL_URL", "http://gluetun:8000"),
	envOr("GLUETUN_HTTP_USER", "pelicula"),
	envOr("GLUETUN_HTTP_PASS", ""),
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
	ctx := context.Background()
	vpn := VPNStatus{Status: "unknown"}

	// Public IP and country
	if status, err := gluetunClient.GetPublicIP(ctx); err == nil && status.PublicIP != "" {
		vpn.IP = status.PublicIP
		vpn.Country = status.Country
		vpn.Status = "healthy"
	} else if err != nil {
		slog.Debug("gluetun public IP unavailable", "component", "health", "error", err)
	}

	// Forwarded port
	if pf, err := gluetunClient.GetForwardedPort(ctx); err == nil {
		vpn.Port = pf.Port
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
