// Package arr — tests for the typed *arr HTTP client.
package arr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newTestClient builds a Client pointed at srv with a fixed fake key.
func newTestClient(srvURL string) *Client {
	return New(srvURL, "fakeapikey")
}

func TestGetMovie_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":1,"title":"Foo"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.GetMovie(context.Background(), "/api/v3", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["id"].(float64) != 1 {
		t.Errorf("expected id=1, got %v", got["id"])
	}
	if got["title"] != "Foo" {
		t.Errorf("expected title=Foo, got %v", got["title"])
	}
}

func TestGetMovie_PathShape(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":42}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.GetMovie(context.Background(), "/api/v3", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/movie/42" {
		t.Errorf("expected path /api/v3/movie/42, got %q", gotPath)
	}
}

func TestGetSeries_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":1,"title":"Breaking Bad"},{"id":2,"title":"Fargo"}]`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.GetSeries(context.Background(), "/api/v3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 series, got %d", len(got))
	}
	if got[0]["title"] != "Breaking Bad" {
		t.Errorf("expected first title Breaking Bad, got %v", got[0]["title"])
	}
}

func TestTriggerCommand_PostsToCommand(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	payload := map[string]any{"name": "RescanMovie", "movieId": 7}
	err := c.TriggerCommand(context.Background(), "/api/v3", payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/command" {
		t.Errorf("expected path /api/v3/command, got %q", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %q", gotMethod)
	}
	if gotBody["name"] != "RescanMovie" {
		t.Errorf("expected name=RescanMovie in body, got %v", gotBody["name"])
	}
}

func TestSearch_QueryEscape(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	term := "Tom & Jerry"
	_, err := c.Search(context.Background(), term)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotQuery != term {
		t.Errorf("expected query round-trip to %q, got %q", term, gotQuery)
	}
	// also check that the raw query contains the URL-escaped form
}

func TestGetMoviesByPath_QueryEscape(t *testing.T) {
	var gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	mediaPath := "/media/My Movies/foo bar"
	_, err := c.GetMoviesByPath(context.Background(), "/api/v3", mediaPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// url.QueryEscape encodes spaces as +
	wantEncoded := url.QueryEscape(mediaPath)
	if !strings.Contains(gotRawQuery, wantEncoded) {
		t.Errorf("expected raw query to contain %q, got %q", wantEncoded, gotRawQuery)
	}
}

func TestApiKeyHeaderSet(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.GetMovie(context.Background(), "/api/v3", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotKey != "fakeapikey" {
		t.Errorf("expected X-Api-Key=fakeapikey, got %q", gotKey)
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
		w.Write([]byte(`{}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := New(srv.URL, "fakeapikey")
	c.base.Retry.Delay = 1 * time.Millisecond

	_, err := c.GetMovie(context.Background(), "/api/v3", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts < 3 {
		t.Errorf("expected at least 3 attempts, got %d", attempts)
	}
}

func TestErrorRedaction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "fakeapikey")
	c.base.Retry.Delay = 1 * time.Millisecond
	c.base.Retry.MaxAttempts = 1

	// The path contains a sensitive query param; httpx should redact it.
	_, err := c.Get(context.Background(), "/api/v3/movie?apikey=secretvalue123")
	if err == nil {
		t.Fatal("expected an error from 500, got nil")
	}
	errStr := err.Error()
	if strings.Contains(errStr, "secretvalue123") {
		t.Errorf("error string must not contain the raw secret: %q", errStr)
	}
	if !strings.Contains(errStr, "REDACTED") {
		t.Errorf("expected REDACTED in error string, got %q", errStr)
	}
}
