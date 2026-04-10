# VPN Port Forward Watchdog — Design Spec

**Date:** 2026-04-10
**Status:** Approved

## Problem

ProtonVPN port forwarding fails silently on startup when gluetun connects to a server whose NAT-PMP gateway is unresponsive. gluetun tries once, gives up, and leaves `port=0` indefinitely. Without a forwarded port, qBittorrent can only make outbound peer connections — no inbound — which limits download speeds to well under 1 Mbps.

Two gaps exist today:

1. **No recovery**: nothing detects port=0 persisting and restarts gluetun to try a different server.
2. **No port sync**: even when port forwarding works, nothing tells qBittorrent to listen on the VPN-assigned port. qBittorrent defaults to 6881; ProtonVPN assigns an arbitrary port (e.g., 51413). Inbound connections fail silently even on a good startup.

## Goals

- Detect port=0 and auto-recover by restarting gluetun (one attempt).
- Keep qBittorrent's listen port in sync with the VPN-assigned port at all times.
- Surface a visible dashboard warning (VPN card + dismissible banner) when recovery fails.
- No new external dependencies; all logic in middleware.

## Non-Goals

- Retry more than once (avoids restart loops on persistently broken VPN configs).
- Notify via Apprise (out of scope; this is an ops-level alert, not a media event).

---

## Architecture

### New file: `middleware/vpn_watchdog.go`

One exported function: `StartVPNWatchdog(s *ServiceClients)`, launched as a goroutine from `main()` alongside `StartMissingWatcher`.

### State machine

```
synced ←──────────────────────────────────────────┐
  │ port becomes 0                                 │
  ↓                                               │ port appears
no_port_grace (5 min window, ~10 polls)            │
  │ still 0 after grace                           │
  ↓                                              synced
restarting (restart gluetun+qbt, wait 90s)         ↑
  │ still 0                                       │
  ↓                                               │ port appears
degraded ──────────────────────────────────────────┘
```

**Timing:**
- Poll interval: 30s
- Grace period: 5 minutes (10 consecutive port=0 polls before triggering restart)
- Cooldown after restart: 90s (gluetun + qBittorrent need time to come up)
- Max restart attempts: 1 — after second failure, enter `degraded` permanently until port recovers naturally

### Port sync

On every poll where `port > 0`, compare to `lastSyncedPort`. If changed, call:

```
POST http://gluetun:8080/api/v2/app/setPreferences
prefs={"listen_port": <port>}
```

This handles initial sync on a clean startup and any port renewals (ProtonVPN rotates forwarded ports periodically).

### State struct

```go
type VPNWatchdogState struct {
    PortForwardStatus string    // "unknown", "synced", "grace", "restarting", "degraded"
    ForwardedPort     int
    LastSyncedAt      time.Time
    RestartAttempts   int
}
```

Package-level, mutex-guarded, readable by `handleHealth`.

---

## API changes

### `VPNStatus` (health.go)

Add `port_status string` field: `"ok"` or `"degraded"`. Port=0 with no watchdog state defaults to `"unknown"` (not `"degraded"`) until the grace period expires.

```json
{
  "vpn": {
    "status": "healthy",
    "ip": "...",
    "country": "Netherlands",
    "port": 0,
    "port_status": "degraded"
  }
}
```

`pelicula check-vpn` already reads this response — the existing `vpnPort > 0` check will print `fail("Port forwarding: not active")` unchanged. No CLI changes needed.

---

## Dashboard changes

### VPN status card

When `vpn.port_status === "degraded"`:
- Port field turns red, shows `"No port forwarding"` instead of `—`
- Inline "Restart VPN" button calls `POST /api/pelicula/admin/vpn/restart` (new endpoint — see Files touched)
- Button shows spinner while restarting; card refreshes on next health poll (~15s)

When `vpn.port_status === "ok"` (port > 0): no change from today.

### Dismissible banner

When `vpn.port_status === "degraded"`, render a yellow banner above the pipeline board:

> Port forwarding is unavailable — download speeds will be limited. [Restart VPN]

- Dismissed state is session-only (localStorage not needed — re-show on reload if still broken)
- Clears automatically within one health poll cycle (~15s) after port forwarding recovers

---

## Testing

- Unit test the state machine transitions with a fake gluetun client (inject `getPort func() int`)
- Unit test port sync: verify qBittorrent preferences API is called when port changes, not called when port is unchanged
- Unit test that restart is attempted exactly once, not on second failure
- Manual: verify banner + red card appear when `port_status` is `"degraded"`; verify both clear when port recovers

---

## Files touched

| File | Change |
|------|--------|
| `middleware/vpn_watchdog.go` | New — watchdog goroutine + state machine |
| `middleware/health.go` | Add `port_status` to `VPNStatus`; read watchdog state |
| `middleware/admin_ops.go` | New `handleVPNRestart` handler — restarts gluetun + qbittorrent + prowlarr only |
| `middleware/main.go` | Launch `StartVPNWatchdog` goroutine; register `POST /api/pelicula/admin/vpn/restart` |
| `nginx/dashboard.js` | Banner + VPN card degraded state; wire "Restart VPN" button to new endpoint |
