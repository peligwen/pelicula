// Package bazarr — tests for the typed Bazarr HTTP client.
package bazarr

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func newTestClient(srvURL string) *Client {
	return New(srvURL, "bazarrkey")
}

func TestGetSettings_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"general":{"language":"en"}}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["general"] == nil {
		t.Error("expected general key in settings response")
	}
}

func TestGetSettings_RequestPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.GetSettings(context.Background()) //nolint:errcheck
	if gotPath != "/api/system/settings" {
		t.Errorf("expected path /api/system/settings, got %q", gotPath)
	}
}

func TestSaveSettings_FormEncoded(t *testing.T) {
	var gotContentType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	form := url.Values{"enabled": {"true"}, "language": {"en"}}
	if err := c.SaveSettings(context.Background(), form); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("expected Content-Type application/x-www-form-urlencoded, got %q", gotContentType)
	}
	// Verify the form fields survive the encode/decode round-trip.
	parsed, err := url.ParseQuery(gotBody)
	if err != nil {
		t.Fatalf("could not parse form body: %v", err)
	}
	if parsed.Get("language") != "en" {
		t.Errorf("expected language=en in form body, got %q", parsed.Get("language"))
	}
}

func TestGetLanguageProfiles_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"profileId":1,"name":"English"},{"profileId":2,"name":"French"}]`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.GetLanguageProfiles(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(got))
	}
	if got[0]["name"] != "English" {
		t.Errorf("expected first profile name English, got %v", got[0]["name"])
	}
}

func TestApiKeyHeaderName(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// r.Header.Get is case-insensitive, which is the correct contract to test.
		gotKey = r.Header.Get("X-API-KEY")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotKey != "bazarrkey" {
		t.Errorf("expected X-API-KEY=bazarrkey, got %q", gotKey)
	}
}

func TestDefaultTimeout_Is30s(t *testing.T) {
	// Pins that the timeout bump was applied and not reverted.
	if defaultTimeout.Seconds() != 30 {
		t.Errorf("expected defaultTimeout=30s, got %v", defaultTimeout)
	}
}

func TestGetLanguageProfiles_RequestPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.GetLanguageProfiles(context.Background()) //nolint:errcheck
	if !strings.HasSuffix(gotPath, "/profiles") {
		t.Errorf("expected path ending in /profiles, got %q", gotPath)
	}
}
