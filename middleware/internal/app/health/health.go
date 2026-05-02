// Package health implements health check probes and VPN status queries
// for the pelicula-api service.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"pelicula-api/internal/clients/gluetun"
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
	Services    ServiceChecker
	GetWatchdog WatchdogStateFunc
	Gluetun     *gluetun.Client
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	vpn := h.queryVPNStatus(r.Context())
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

// queryVPNStatus queries the Gluetun control API for public IP and forwarded
// port in parallel, then annotates the result from watchdog state.
func (h *Handler) queryVPNStatus(ctx context.Context) VPNStatus {
	vpn := VPNStatus{Status: "unknown"}

	if h.Gluetun == nil {
		return vpn
	}

	var ipResult *gluetun.VPNStatus
	var portResult *gluetun.PortForward
	var ipErr, portErr error

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ipResult, ipErr = h.Gluetun.GetPublicIP(ctx)
	}()
	go func() {
		defer wg.Done()
		portResult, portErr = h.Gluetun.GetForwardedPort(ctx)
	}()
	wg.Wait()

	if ipErr != nil {
		slog.Debug("gluetun public IP unavailable", "component", "health", "error", ipErr)
	} else if ipResult != nil && ipResult.PublicIP != "" {
		vpn.IP = ipResult.PublicIP
		vpn.Country = ipResult.Country
		vpn.Status = "healthy"
	}

	if portErr != nil {
		slog.Debug("gluetun port forward status unavailable", "component", "health", "error", portErr)
	} else if portResult != nil {
		vpn.Port = portResult.Port
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
