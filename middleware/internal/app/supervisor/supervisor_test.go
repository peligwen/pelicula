package supervisor_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"pelicula-api/internal/app/missingwatcher"
	appservices "pelicula-api/internal/app/services"
	"pelicula-api/internal/app/vpnwatchdog"
	"pelicula-api/internal/clients/docker"
	gluetunclient "pelicula-api/internal/clients/gluetun"
	"pelicula-api/internal/config"
)

// TestSupervisor_GoroutinesExitOnCtxCancel starts a missingwatcher and a
// vpnwatchdog goroutine via their ctx-aware Run methods, cancels the context
// after 50ms, and asserts both goroutines exit within 500ms.
func TestSupervisor_GoroutinesExitOnCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// Build a minimal services.Clients that is already wired so the watcher
	// does not spin in the IsWired() poll.
	svc := appservices.New(&config.Config{}, "")
	svc.SetWired(true)

	// Create watcher with a very long interval — it should not fire during the test.
	watcher := missingwatcher.New(svc, "http://localhost:0", "http://localhost:0")

	// Create watchdog with nil Docker/Gluetun — the watchdog polls the gluetun
	// client each tick, but because JitteredTicker stops on ctx.Done() the
	// goroutine will exit before any tick fires (interval is 30s).
	watchdog := vpnwatchdog.New(svc, (*docker.Client)(nil), (*gluetunclient.Client)(nil))

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		watcher.Run(ctx, 10*time.Minute) // long interval — won't fire
	}()

	go func() {
		defer wg.Done()
		watchdog.Run(ctx)
	}()

	// Cancel after 50ms.
	time.AfterFunc(50*time.Millisecond, cancel)

	// Both goroutines must exit within 500ms.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(500 * time.Millisecond):
		t.Error("goroutines did not exit within 500ms of ctx cancellation")
	}
}
