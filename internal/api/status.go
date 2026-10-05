package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"pelicula/internal/store"
)

const (
	statusPingTimeout = 2 * time.Second
	statusCacheTTL    = 5 * time.Second
)

type serviceStatus struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Path string `json:"path"`
}

type vpnInfo struct {
	Enabled       bool   `json:"enabled"`
	Tunnel        string `json:"tunnel"`
	PublicIP      string `json:"public_ip"`
	Country       string `json:"country"`
	ForwardedPort int    `json:"forwarded_port"`
}

// statusSnapshot is the part of /api/status that costs upstream calls and so
// is cached; counts and the wired flag are read fresh on every request.
type statusSnapshot struct {
	Services []serviceStatus
	VPN      vpnInfo
}

type statusResponse struct {
	Services        []serviceStatus `json:"services"`
	VPN             vpnInfo         `json:"vpn"`
	Wired           bool            `json:"wired"`
	Version         string          `json:"version"`
	PendingRequests int             `json:"pending_requests"`
	QueuedJobs      int             `json:"queued_jobs"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	snap := s.statusSnapshot(r.Context())
	resp := statusResponse{
		Services: snap.Services,
		VPN:      snap.VPN,
		Wired:    s.wired(),
		Version:  s.Cfg.Version,
	}
	if sess := s.user(r); sess != nil && sess.Role.AtLeast(store.RoleManager) {
		if n, err := s.Store.CountRequests(r.Context(), store.RequestPending); err == nil {
			resp.PendingRequests = n
		}
	}
	if n, err := s.Store.CountJobs(r.Context(), store.JobQueued); err == nil {
		resp.QueuedJobs = n
	}
	writeJSON(w, http.StatusOK, resp)
}

// statusSnapshot returns the cached service/VPN probe, refreshing it when it
// is older than statusCacheTTL. The lock is held while refreshing so a burst
// of dashboards triggers one probe, not one each.
func (s *Server) statusSnapshot(ctx context.Context) statusSnapshot {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if !s.statusAt.IsZero() && time.Since(s.statusAt) < statusCacheTTL {
		return s.statusSnap
	}
	// The result is shared, so one caller going away must not abort it.
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), statusPingTimeout)
	defer cancel()
	s.statusSnap = s.probe(probeCtx)
	s.statusAt = time.Now()
	return s.statusSnap
}

type pinger interface {
	Ping(ctx context.Context) error
}

// probe pings every service and, with the VPN on, asks gluetun for the tunnel
// state, all in parallel.
func (s *Server) probe(ctx context.Context) statusSnapshot {
	// A nil ArrClient/QBTClient/JellyfinClient converts to a nil pinger, which
	// is reported as down rather than dereferenced.
	type target struct {
		name, path string
		client     pinger
	}
	targets := []target{
		{"sonarr", "/sonarr/", s.Sonarr},
		{"radarr", "/radarr/", s.Radarr},
		{"jellyfin", "/jellyfin/", s.Jellyfin},
	}
	if s.Cfg.VPNEnabled {
		targets = append(targets,
			target{"prowlarr", "/prowlarr/", s.Prowlarr},
			target{"qbittorrent", "/qbt/", s.QBT},
		)
	}

	snap := statusSnapshot{
		Services: make([]serviceStatus, len(targets)),
		VPN:      vpnInfo{Enabled: s.Cfg.VPNEnabled},
	}
	var wg sync.WaitGroup
	for i, t := range targets {
		snap.Services[i] = serviceStatus{Name: t.name, Path: t.path}
		if t.client == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			snap.Services[i].OK = t.client.Ping(ctx) == nil
		}()
	}

	if s.Cfg.VPNEnabled {
		snap.VPN.Tunnel = "unknown"
		if g := s.Gluetun; g != nil {
			wg.Add(3)
			go func() {
				defer wg.Done()
				if st, err := g.GetTunnelStatus(ctx); err == nil && st != "" {
					snap.VPN.Tunnel = st
				}
			}()
			go func() {
				defer wg.Done()
				if ip, err := g.GetPublicIP(ctx); err == nil && ip != nil {
					snap.VPN.PublicIP = ip.PublicIP
					snap.VPN.Country = ip.Country
				}
			}()
			go func() {
				defer wg.Done()
				if port, err := g.GetForwardedPort(ctx); err == nil {
					snap.VPN.ForwardedPort = port
				}
			}()
		}
	}
	wg.Wait()
	return snap
}
