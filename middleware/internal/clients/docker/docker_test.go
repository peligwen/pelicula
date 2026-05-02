package docker_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	stats, err := c.Stats(context.Background(), "sonarr")
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
	stats, err := c.Stats(context.Background(), "qbittorrent")
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
	_, err := c.Stats(context.Background(), "sonarr")
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
}

// TestRestart_RespectsCtx verifies that cancelling the context causes Restart
// to return an error wrapping context.Canceled.
func TestRestart_RespectsCtx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := docker.New(srv.URL, "pelicula")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := c.Restart(ctx, "sonarr")
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Errorf("expected context-related error, got: %v", err)
	}
}

// TestRestart_UserAgent verifies that the docker client sends a User-Agent
// header with the Pelicula prefix.
func TestRestart_UserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := docker.New(srv.URL, "pelicula")
	if err := c.Restart(context.Background(), "sonarr"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(gotUA, "Pelicula/") {
		t.Errorf("User-Agent = %q, want prefix \"Pelicula/\"", gotUA)
	}
}

// TestDemuxDockerLogs verifies that demuxDockerLogs strips 8-byte framing
// headers and concatenates payloads from multiple frames.
// We test through Logs by installing a fake LogsFunc that uses a pre-built
// framed byte stream.
func TestDemuxDockerLogs(t *testing.T) {
	frame := func(streamType byte, payload string) []byte {
		b := make([]byte, 8+len(payload))
		b[0] = streamType
		binary.BigEndian.PutUint32(b[4:8], uint32(len(payload)))
		copy(b[8:], payload)
		return b
	}

	// Two stdout frames and one stderr frame.
	framed := append(append(
		frame(1, "hello "),
		frame(2, "world\n")...),
		frame(1, "foo\n")...)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(framed) //nolint:errcheck
	}))
	defer srv.Close()

	c := docker.New(srv.URL, "pelicula")
	got, err := c.Logs(context.Background(), "sonarr", 10, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "hello world\nfoo\n"
	if string(got) != want {
		t.Errorf("demux output = %q, want %q", string(got), want)
	}
}
