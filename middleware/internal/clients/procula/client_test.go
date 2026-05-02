// Package procula — tests for the typed Procula HTTP client.
package procula

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(srvURL string) *Client {
	return New(srvURL, "prockey")
}

func TestEnqueueAction_PostsJSON(t *testing.T) {
	var gotContentType, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotMethod = r.Method
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	payload := map[string]any{"action": "transcode", "target": map[string]any{"id": "abc"}}
	_, err := c.EnqueueAction(context.Background(), payload, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %q", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", gotContentType)
	}
	if gotBody["action"] != "transcode" {
		t.Errorf("expected action=transcode in body, got %v", gotBody["action"])
	}
}

func TestEnqueueAction_WithWaitQuery(t *testing.T) {
	var gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.EnqueueAction(context.Background(), map[string]any{"action": "noop"}, "?wait=true") //nolint:errcheck
	if gotRawQuery != "wait=true" {
		t.Errorf("expected raw query=wait=true, got %q", gotRawQuery)
	}
}

func TestCreateJob_PostsToJobsEndpoint(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"job-1"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.CreateJob(context.Background(), map[string]any{"source": "/media/foo.mkv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/procula/jobs" {
		t.Errorf("expected path /api/procula/jobs, got %q", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %q", gotMethod)
	}
}

func TestDeleteProfile_PathEscape(t *testing.T) {
	var gotRequestURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// r.RequestURI is the raw wire form and retains percent-encoding.
		gotRequestURI = r.RequestURI
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	profileName := "HD 1080p Best"
	err := c.DeleteProfile(context.Background(), profileName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Spaces must be percent-encoded on the wire.
	if strings.Contains(gotRequestURI, " ") {
		t.Errorf("expected spaces to be path-escaped in %q", gotRequestURI)
	}
	if !strings.Contains(gotRequestURI, "HD%201080p%20Best") {
		t.Errorf("expected path-escaped name in request URI, got %q", gotRequestURI)
	}
}

func TestApiKeyHeaderSet(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.GetStatus(context.Background()) //nolint:errcheck
	if gotKey != "prockey" {
		t.Errorf("expected X-API-Key=prockey, got %q", gotKey)
	}
}

func TestGetJob_PathEscape(t *testing.T) {
	var gotRequestURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequestURI = r.RequestURI
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"job with spaces"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.GetJob(context.Background(), "job with spaces")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(gotRequestURI, " ") {
		t.Errorf("expected spaces to be path-escaped in request URI %q", gotRequestURI)
	}
}

func TestEnqueueAction_RequestBody(t *testing.T) {
	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.EnqueueAction(context.Background(), map[string]any{"action": "validate"}, "") //nolint:errcheck

	var decoded map[string]any
	if err := json.Unmarshal(rawBody, &decoded); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if decoded["action"] != "validate" {
		t.Errorf("expected action=validate in JSON body, got %v", decoded["action"])
	}
}
