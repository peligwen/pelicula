package main

import (
	"context"
	"log/slog"
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
