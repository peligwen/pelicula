package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestNewHTTPClient_UserAgent verifies that newHTTPClient injects the expected
// User-Agent header on outbound requests.
func TestNewHTTPClient_UserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newHTTPClient(5 * time.Second)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	resp.Body.Close()

	want := "PeliculaCLI/" + version + " (+https://github.com/peligwen/pelicula)"
	if gotUA != want {
		t.Errorf("User-Agent = %q, want %q", gotUA, want)
	}
}

// TestNewHTTPClient_Timeout verifies that newHTTPClient respects the timeout.
func TestNewHTTPClient_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newHTTPClient(50 * time.Millisecond)
	start := time.Now()
	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Errorf("client took %v to time out, want < 400ms", elapsed)
	}
}
