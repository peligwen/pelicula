package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	appservices "pelicula-api/internal/app/services"
)

// ── Timing constants ───────────────────────────────────────────────────────────

const (
	watchdogInterval     = 30 * time.Second
	gracePolls           = 10 // 10 × 30s = 5 min before first restart
	restartCooldownPolls = 3  // 3 × 30s = 90s wait after restart
	postRestartGrace     = 2  // 2 × 30s = 60s post-cooldown grace before degraded
)

// ── State types ────────────────────────────────────────────────────────────────

type watchdogStatus string

const (
	wdUnknown    watchdogStatus = "unknown"
	wdSynced     watchdogStatus = "synced"
	wdGrace      watchdogStatus = "grace"
	wdRestarting watchdogStatus = "restarting"
	wdDegraded   watchdogStatus = "degraded"
)

type watchdogAction int

const (
	wdActNone    watchdogAction = iota
	wdActSync                   // call syncQbtListenPort
	wdActRestart                // restart gluetun + qbittorrent + prowlarr
)

// wdInternalState is mutable state threaded through the pure wdTick function.
type wdInternalState struct {
	status          watchdogStatus
	lastKnownPort   int
	consecutiveZero int
	restartAttempts int
	restartCooldown int // ticks remaining in post-restart cooldown
}

// VPNWatchdogState is the package-level public state read by internal/app/health.Handler.
type VPNWatchdogState struct {
	PortForwardStatus string
	ForwardedPort     int
	LastSyncedAt      time.Time
	RestartAttempts   int
	// Diagnostic fields surfaced in the health API.
	ConsecutiveZero   int
	GraceRemaining    int // polls remaining in grace period (gracePolls - consecutiveZero)
	CooldownRemaining int // ticks remaining in post-restart cooldown
	LastTransitionAt  time.Time
	VPNTunnelStatus   string // last known gluetun tunnel status ("running", "stopped", "unknown")
}

var (
	watchdogMu    sync.RWMutex
	watchdogState VPNWatchdogState
)

// GetWatchdogState returns a snapshot of the current watchdog state.
func GetWatchdogState() VPNWatchdogState {
	watchdogMu.RLock()
	defer watchdogMu.RUnlock()
	return watchdogState
}

// ── Pure state machine ─────────────────────────────────────────────────────────

// wdTick advances the watchdog state machine for one poll cycle.
// port is the currently forwarded port (0 = not forwarded).
// Returns the new internal state and the side-effect action to take.
func wdTick(port int, s wdInternalState) (wdInternalState, watchdogAction) {
	// While in post-restart cooldown: skip port checks entirely.
	if s.restartCooldown > 0 {
		s.restartCooldown--
		return s, wdActNone
	}

	if port > 0 {
		recovering := s.status == wdDegraded || s.status == wdRestarting
		needsSync := s.lastKnownPort != port || recovering
		s.consecutiveZero = 0
		s.lastKnownPort = port
		s.status = wdSynced
		if recovering {
			s.restartAttempts = 0
		}
		if needsSync {
			return s, wdActSync
		}
		return s, wdActNone
	}

	// port == 0

	// Degraded: stay put until port comes back naturally.
	if s.status == wdDegraded {
		return s, wdActNone
	}

	s.consecutiveZero++

	// Post-restart path: shorter tolerance before declaring degraded.
	if s.restartAttempts >= 1 {
		if s.consecutiveZero >= postRestartGrace {
			s.status = wdDegraded
			slog.Warn("port forwarding still unavailable after restart — entering degraded state",
				"component", "vpn_watchdog")
		}
		return s, wdActNone
	}

	// First-time grace period.
	if s.consecutiveZero >= gracePolls {
		s.restartAttempts++
		s.status = wdRestarting
		s.restartCooldown = restartCooldownPolls
		s.consecutiveZero = 0 // reset for post-restart counting
		return s, wdActRestart
	}

	s.status = wdGrace
	return s, wdActNone
}

// ── Port sync ──────────────────────────────────────────────────────────────────

// syncQbtListenPort tells qBittorrent to listen on port via the preferences API.
// Uses form encoding: POST /api/v2/app/setPreferences with json={"listen_port":N}
func syncQbtListenPort(s *appservices.Clients, port int) error {
	if err := s.Qbt.SetPreferences(context.Background(), port); err != nil {
		slog.Error("failed to sync qBittorrent listen port",
			"component", "vpn_watchdog", "port", port, "error", err)
		return err
	}
	slog.Info("synced qBittorrent listen port", "component", "vpn_watchdog", "port", port)
	return nil
}

// ── Watchdog goroutine ─────────────────────────────────────────────────────────

// StartVPNWatchdog monitors VPN port forwarding and keeps qBittorrent's listen
// port in sync. Call as a goroutine from main(). Only active when VPN is
// configured (caller should guard on WIREGUARD_PRIVATE_KEY).
func StartVPNWatchdog(s *appservices.Clients) {
	ticker := time.NewTicker(watchdogInterval)
	defer ticker.Stop()

	internal := wdInternalState{status: wdUnknown}
	prevStatus := wdUnknown

	// Publish initial state so health endpoint never returns an empty string.
	watchdogMu.Lock()
	watchdogState.PortForwardStatus = string(wdUnknown)
	watchdogMu.Unlock()

	slog.Info("started", "component", "vpn_watchdog", "poll_interval", watchdogInterval)

	for range ticker.C {
		port, err := fetchForwardedPort()
		if err != nil {
			slog.Warn("failed to query gluetun port forwarding — skipping tick",
				"component", "vpn_watchdog", "error", err)
			continue
		}

		slog.Debug("watchdog tick",
			"component", "vpn_watchdog",
			"port", port,
			"status", internal.status,
			"consecutive_zero", internal.consecutiveZero,
			"cooldown", internal.restartCooldown)

		// When port is 0, also fetch tunnel status to distinguish NAT-PMP
		// failure (tunnel up, port=0) from VPN not yet connected (tunnel down).
		tunnelStatus := ""
		if port == 0 {
			tunnelStatus = fetchTunnelStatus()
			if tunnelStatus != "" {
				slog.Debug("gluetun tunnel status",
					"component", "vpn_watchdog", "tunnel", tunnelStatus)
			}
		}

		prevPort := internal.lastKnownPort
		newInternal, action := wdTick(port, internal)

		// Log state transitions.
		if newInternal.status != prevStatus {
			slog.Info("watchdog state transition",
				"component", "vpn_watchdog",
				"from", prevStatus,
				"to", newInternal.status,
				"port", port)
			prevStatus = newInternal.status
		}

		// Log grace period progress when port is 0.
		if port == 0 && newInternal.status == wdGrace {
			remaining := gracePolls - newInternal.consecutiveZero
			slog.Warn("port forwarding unavailable",
				"component", "vpn_watchdog",
				"consecutive_zero", newInternal.consecutiveZero,
				"grace_remaining", remaining,
				"tunnel", tunnelStatus)
		}

		internal = newInternal

		// Compute derived fields for public state.
		graceRemaining := 0
		if internal.status == wdGrace {
			graceRemaining = gracePolls - internal.consecutiveZero
		}

		// Publish state for health endpoint.
		now := time.Now()
		watchdogMu.Lock()
		if string(internal.status) != watchdogState.PortForwardStatus {
			watchdogState.LastTransitionAt = now
		}
		watchdogState.PortForwardStatus = string(internal.status)
		watchdogState.ForwardedPort = internal.lastKnownPort
		watchdogState.RestartAttempts = internal.restartAttempts
		watchdogState.ConsecutiveZero = internal.consecutiveZero
		watchdogState.GraceRemaining = graceRemaining
		watchdogState.CooldownRemaining = internal.restartCooldown
		if tunnelStatus != "" {
			watchdogState.VPNTunnelStatus = tunnelStatus
		}
		watchdogMu.Unlock()

		switch action {
		case wdActSync:
			if err := syncQbtListenPort(s, port); err != nil {
				// Sync failed — revert lastKnownPort so next tick retries
				internal.lastKnownPort = prevPort
			} else {
				watchdogMu.Lock()
				watchdogState.LastSyncedAt = time.Now()
				watchdogMu.Unlock()
			}
		case wdActRestart:
			slog.Warn("port forwarding unavailable after grace period — restarting VPN containers",
				"component", "vpn_watchdog", "tunnel", tunnelStatus)
			for _, svc := range []string{"gluetun", "qbittorrent", "prowlarr"} {
				if !dockerCli.IsAllowed(svc) {
					continue
				}
				if err := dockerCli.Restart(svc); err != nil {
					slog.Error("vpn watchdog restart failed",
						"component", "vpn_watchdog", "svc", svc, "error", err)
				}
			}
		}
	}
}

// fetchForwardedPort queries gluetun for the currently forwarded port.
// Returns (port, nil) on success — port may be 0 if forwarding is inactive.
// Returns (0, err) on any transport/JSON error.
func fetchForwardedPort() (int, error) {
	return gluetunClient.GetPortForward(context.Background())
}

// fetchTunnelStatus queries gluetun for the VPN tunnel connection status.
// Returns the status string (e.g. "running", "stopped") or "" on any error.
// This is best-effort — callers should treat "" as unknown.
func fetchTunnelStatus() string {
	status, _ := gluetunClient.GetTunnelStatus(context.Background())
	return status
}
