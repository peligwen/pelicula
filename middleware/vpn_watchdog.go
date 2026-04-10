package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
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

// VPNWatchdogState is the package-level public state read by handleHealth.
type VPNWatchdogState struct {
	PortForwardStatus string
	ForwardedPort     int
	LastSyncedAt      time.Time
	RestartAttempts   int
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
func syncQbtListenPort(s *ServiceClients, port int) {
	prefs := fmt.Sprintf(`{"listen_port":%d}`, port)
	if err := s.QbtPost("/api/v2/app/setPreferences", "json="+url.QueryEscape(prefs)); err != nil {
		slog.Error("failed to sync qBittorrent listen port",
			"component", "vpn_watchdog", "port", port, "error", err)
		return
	}
	slog.Info("synced qBittorrent listen port", "component", "vpn_watchdog", "port", port)
}

// ── Watchdog goroutine ─────────────────────────────────────────────────────────

// StartVPNWatchdog monitors VPN port forwarding and keeps qBittorrent's listen
// port in sync. Call as a goroutine from main(). Only active when VPN is
// configured (caller should guard on WIREGUARD_PRIVATE_KEY).
func StartVPNWatchdog(s *ServiceClients) {
	ticker := time.NewTicker(watchdogInterval)
	defer ticker.Stop()

	client := &http.Client{Timeout: 5 * time.Second}
	internal := wdInternalState{status: wdUnknown}

	// Publish initial state so health endpoint never returns an empty string.
	watchdogMu.Lock()
	watchdogState.PortForwardStatus = string(wdUnknown)
	watchdogMu.Unlock()

	slog.Info("started", "component", "vpn_watchdog", "poll_interval", watchdogInterval)

	for range ticker.C {
		port := fetchForwardedPort(client)
		newInternal, action := wdTick(port, internal)
		internal = newInternal

		// Publish state for health endpoint.
		watchdogMu.Lock()
		watchdogState.PortForwardStatus = string(internal.status)
		watchdogState.ForwardedPort = internal.lastKnownPort
		watchdogState.RestartAttempts = internal.restartAttempts
		if action == wdActSync {
			watchdogState.LastSyncedAt = time.Now()
		}
		watchdogMu.Unlock()

		switch action {
		case wdActSync:
			syncQbtListenPort(s, port)
		case wdActRestart:
			slog.Warn("port forwarding unavailable after grace period — restarting VPN containers",
				"component", "vpn_watchdog")
			for _, svc := range []string{"gluetun", "qbittorrent", "prowlarr"} {
				if err := dockerRestart(svc); err != nil {
					slog.Error("vpn watchdog restart failed",
						"component", "vpn_watchdog", "svc", svc, "error", err)
				}
			}
		}
	}
}

// fetchForwardedPort queries gluetun for the currently forwarded port.
// Returns 0 on any error or when no port is assigned.
func fetchForwardedPort(client *http.Client) int {
	body, ok := gluetunGet(client, "/v1/portforward")
	if !ok {
		return 0
	}
	var data struct {
		Port int `json:"port"`
	}
	if json.Unmarshal(body, &data) != nil {
		return 0
	}
	return data.Port
}
