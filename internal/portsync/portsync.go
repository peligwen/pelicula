// Package portsync keeps qBittorrent's listen port equal to the port gluetun
// forwarded from the VPN provider, so peers can reach us through the tunnel.
package portsync

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// PortSource reports the port forwarded by the VPN (gluetun). 0 means none yet.
type PortSource interface {
	GetForwardedPort(ctx context.Context) (int, error)
}

// PortTarget is the torrent client whose listen port follows the forwarded one.
type PortTarget interface {
	GetListenPort(ctx context.Context) (int, error)
	SetListenPort(ctx context.Context, port int) error
}

const defaultInterval = 60 * time.Second

// Run syncs once immediately and then every interval until ctx is done. When
// the forwarded port is positive and differs from the listen port it sets the
// listen port. It logs only when it changes the port or when something fails
// (an unchanged repeated failure is logged once until it clears). A
// non-positive interval means 60s.
func Run(ctx context.Context, src PortSource, dst PortTarget, interval time.Duration, log *slog.Logger) {
	if interval <= 0 {
		interval = defaultInterval
	}
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "portsync")

	var lastErr string
	tick := func() {
		changed, port, err := syncOnce(ctx, src, dst)
		switch {
		case ctx.Err() != nil:
			// shutting down; whatever failed is not worth logging
		case err != nil:
			if msg := err.Error(); msg != lastErr {
				log.Warn("port sync failed", "error", err)
				lastErr = msg
			}
		default:
			lastErr = ""
			if changed {
				log.Info("qBittorrent listen port updated", "port", port)
			}
		}
	}

	tick()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// syncOnce makes one pass. It reports whether it changed the listen port and
// the forwarded port it saw.
func syncOnce(ctx context.Context, src PortSource, dst PortTarget) (changed bool, port int, err error) {
	port, err = src.GetForwardedPort(ctx)
	if err != nil {
		return false, 0, fmt.Errorf("forwarded port: %w", err)
	}
	if port <= 0 {
		return false, port, nil
	}
	cur, err := dst.GetListenPort(ctx)
	if err != nil {
		return false, port, fmt.Errorf("listen port: %w", err)
	}
	if cur == port {
		return false, port, nil
	}
	if err := dst.SetListenPort(ctx, port); err != nil {
		return false, port, fmt.Errorf("set listen port: %w", err)
	}
	return true, port, nil
}
