package procula

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultRefreshDebounceMs is how long we wait after the last refresh request
// before firing the actual POST to pelicula-api. A download burst (10 items in
// 5s) produces one Jellyfin scan instead of ten.
const defaultRefreshDebounceMs = 5000

// refreshDebounceMs is the resolved debounce window, loaded once at startup
// from JELLYFIN_REFRESH_DEBOUNCE_MS. Hot-path reads use this package var.
var refreshDebounceMs = time.Duration(defaultRefreshDebounceMs) * time.Millisecond

var (
	refreshMu     sync.Mutex
	refreshTimer  *time.Timer
	refreshTarget string // most recent peliculaAPI URL passed in
)

// scheduleJellyfinRefresh debounces refresh requests: each call resets a timer,
// and a single POST fires once the timer expires. Configurable via
// JELLYFIN_REFRESH_DEBOUNCE_MS (default 5000ms). Set to 0 to disable debouncing
// — every call fires immediately and synchronously. Tests that assert exact
// refresh counts (to verify pipeline ordering) opt out by setting the env to 0.
func scheduleJellyfinRefresh(peliculaAPI string) error {
	delay := refreshDebounceDelay()
	if delay <= 0 {
		return doJellyfinRefresh(peliculaAPI)
	}
	refreshMu.Lock()
	defer refreshMu.Unlock()
	refreshTarget = peliculaAPI
	if refreshTimer != nil {
		refreshTimer.Stop()
	}
	refreshTimer = time.AfterFunc(delay, func() {
		refreshMu.Lock()
		target := refreshTarget
		refreshTimer = nil
		refreshTarget = ""
		refreshMu.Unlock()
		if target == "" {
			return
		}
		if err := doJellyfinRefresh(target); err != nil {
			slog.Warn("debounced Jellyfin refresh failed (non-fatal)", "component", "catalog", "error", err)
		}
	})
	return nil
}

// FlushJellyfinRefresh forces any pending debounced refresh to fire synchronously.
// Call this on shutdown so a queued refresh isn't lost. Safe to call when no
// refresh is pending — does nothing.
func FlushJellyfinRefresh() {
	refreshMu.Lock()
	target := refreshTarget
	if refreshTimer != nil {
		refreshTimer.Stop()
		refreshTimer = nil
	}
	refreshTarget = ""
	refreshMu.Unlock()
	if target == "" {
		return
	}
	if err := doJellyfinRefresh(target); err != nil {
		slog.Warn("flushed Jellyfin refresh failed (non-fatal)", "component", "catalog", "error", err)
	}
}

func refreshDebounceDelay() time.Duration { return refreshDebounceMs }

// parseRefreshDebounceMs converts a raw env string to a Duration.
// Empty or invalid input returns the default (5000ms).
func parseRefreshDebounceMs(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Duration(defaultRefreshDebounceMs) * time.Millisecond
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return time.Duration(defaultRefreshDebounceMs) * time.Millisecond
	}
	return time.Duration(n) * time.Millisecond
}

// doJellyfinRefresh issues the actual POST to pelicula-api. Used directly when
// debouncing is disabled and indirectly via the timer goroutine in scheduleJellyfinRefresh.
func doJellyfinRefresh(peliculaAPI string) error {
	target := peliculaAPI + "/api/pelicula/jellyfin/refresh"
	client := newProculaClient(10 * time.Second)
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(nil))
	if err != nil {
		return err
	}
	if proculaAPIKey != "" {
		req.Header.Set("X-API-Key", proculaAPIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	slog.Info("triggered Jellyfin library refresh", "component", "catalog")
	return nil
}
