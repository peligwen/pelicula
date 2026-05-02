// Package qbt — tests for the typed qBittorrent v5 HTTP client.
package qbt

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newTestClient(srvURL string) *Client {
	return New(srvURL)
}

func TestListTorrents_DecodesShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"hash":"abc123","name":"My Movie","progress":0.75,"state":"downloading","size":1073741824}]`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	torrents, err := c.ListTorrents(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(torrents) != 1 {
		t.Fatalf("expected 1 torrent, got %d", len(torrents))
	}
	got := torrents[0]
	if got.Hash != "abc123" {
		t.Errorf("expected hash abc123, got %q", got.Hash)
	}
	if got.Name != "My Movie" {
		t.Errorf("expected name My Movie, got %q", got.Name)
	}
	if got.Progress != 0.75 {
		t.Errorf("expected progress 0.75, got %v", got.Progress)
	}
	if got.Size != 1073741824 {
		t.Errorf("expected size 1073741824, got %d", got.Size)
	}
}

func TestStopTorrent_PostsForm(t *testing.T) {
	var gotPath, gotMethod, gotContentType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	if err := c.StopTorrent(context.Background(), "deadbeef"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v2/torrents/stop" {
		t.Errorf("expected /api/v2/torrents/stop (qBT v5), got %q", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %q", gotMethod)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("expected Content-Type application/x-www-form-urlencoded, got %q", gotContentType)
	}
	parsed, _ := url.ParseQuery(gotBody)
	if parsed.Get("hashes") != "deadbeef" {
		t.Errorf("expected hashes=deadbeef in form, got %q", parsed.Get("hashes"))
	}
}

func TestStartTorrent_PostsForm(t *testing.T) {
	var gotPath string
	var gotHashes string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		parsed, _ := url.ParseQuery(string(b))
		gotHashes = parsed.Get("hashes")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	if err := c.StartTorrent(context.Background(), "cafebabe"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v2/torrents/start" {
		t.Errorf("expected /api/v2/torrents/start (qBT v5 renamed from resume), got %q", gotPath)
	}
	if gotHashes != "cafebabe" {
		t.Errorf("expected hashes=cafebabe, got %q", gotHashes)
	}
}

func TestDeleteTorrent_DeleteFilesTrue(t *testing.T) {
	var gotDeleteFiles string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		parsed, _ := url.ParseQuery(string(b))
		gotDeleteFiles = parsed.Get("deleteFiles")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	if err := c.DeleteTorrent(context.Background(), "hash1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotDeleteFiles != "true" {
		t.Errorf("expected deleteFiles=true, got %q", gotDeleteFiles)
	}
}

func TestRemoveTorrent_DeleteFilesFalse(t *testing.T) {
	var gotDeleteFiles string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		parsed, _ := url.ParseQuery(string(b))
		gotDeleteFiles = parsed.Get("deleteFiles")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	if err := c.RemoveTorrent(context.Background(), "hash2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotDeleteFiles != "false" {
		t.Errorf("expected deleteFiles=false, got %q", gotDeleteFiles)
	}
}

func TestSetPreferences_JSONField(t *testing.T) {
	var gotJSON string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		parsed, _ := url.ParseQuery(string(b))
		gotJSON = parsed.Get("json")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	if err := c.SetPreferences(context.Background(), 51413); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(gotJSON, `"listen_port":51413`) {
		t.Errorf("expected json field to contain listen_port:51413, got %q", gotJSON)
	}
}

func TestNoApiKeyHeader(t *testing.T) {
	var gotAuthKey, gotApiKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthKey = r.Header.Get("Authorization")
		gotApiKey = r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer srv.Close()

	// qBittorrent uses subnet-bypass auth — no API key should be sent.
	c := newTestClient(srv.URL)
	c.ListTorrents(context.Background()) //nolint:errcheck
	if gotAuthKey != "" {
		t.Errorf("expected no Authorization header for qbt subnet-bypass, got %q", gotAuthKey)
	}
	if gotApiKey != "" {
		t.Errorf("expected no X-Api-Key header for qbt subnet-bypass, got %q", gotApiKey)
	}
}

func TestRetryOn5xx_Inherited(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.base.Retry.Delay = 1 * time.Millisecond

	_, err := c.ListTorrents(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts < 3 {
		t.Errorf("expected at least 3 attempts (retry on 5xx), got %d", attempts)
	}
}
