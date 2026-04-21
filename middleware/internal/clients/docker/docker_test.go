package docker_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pelicula-api/internal/clients/docker"
)

// TestStats_HappyPath verifies that Stats decodes rx_bytes/tx_bytes correctly
// when the container has a non-null networks map.
func TestStats_HappyPath(t *testing.T) {
	payload := map[string]any{
		"read": "2026-04-21T12:00:00Z",
		"networks": map[string]any{
			"eth0": map[string]any{"rx_bytes": 1234, "tx_bytes": 5678},
			"eth1": map[string]any{"rx_bytes": 100, "tx_bytes": 200},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(payload) //nolint:errcheck
	}))
	defer srv.Close()

	c := docker.New(srv.URL, "pelicula")
	stats, err := c.Stats("sonarr")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats == nil {
		t.Fatal("expected non-nil StatsResponse")
	}
	if stats.Networks == nil {
		t.Fatal("expected non-nil Networks map")
	}
	if got := stats.Networks["eth0"].RxBytes; got != 1234 {
		t.Errorf("eth0 RxBytes: want 1234, got %d", got)
	}
	if got := stats.Networks["eth0"].TxBytes; got != 5678 {
		t.Errorf("eth0 TxBytes: want 5678, got %d", got)
	}
	if got := stats.Networks["eth1"].RxBytes; got != 100 {
		t.Errorf("eth1 RxBytes: want 100, got %d", got)
	}
	if stats.Read.IsZero() {
		t.Error("expected non-zero Read time")
	}
}

// TestStats_NullNetworks verifies that a container with networks:null (e.g.
// qbittorrent/prowlarr sharing gluetun's network namespace) decodes cleanly
// with a nil Networks map rather than an error.
func TestStats_NullNetworks(t *testing.T) {
	payload := map[string]any{
		"read":     time.Now().UTC().Format(time.RFC3339),
		"networks": nil,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(payload) //nolint:errcheck
	}))
	defer srv.Close()

	c := docker.New(srv.URL, "pelicula")
	stats, err := c.Stats("qbittorrent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats == nil {
		t.Fatal("expected non-nil StatsResponse")
	}
	if stats.Networks != nil {
		t.Errorf("expected nil Networks for null JSON, got %v", stats.Networks)
	}
}

// TestStats_NonTwoXX verifies that a non-2xx response returns an error.
func TestStats_NonTwoXX(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := docker.New(srv.URL, "pelicula")
	_, err := c.Stats("sonarr")
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
}
