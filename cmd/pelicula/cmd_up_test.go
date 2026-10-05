package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchHealth(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr bool
		wired   bool
	}{
		{"ok and wired", 200, `{"ok":true,"wired":true,"version":"v1.2.3"}`, false, true},
		{"ok not wired", 200, `{"ok":true,"wired":false,"version":"dev"}`, false, false},
		{"ok false", 200, `{"ok":false,"wired":false}`, true, false},
		{"server error", 503, `{"error":"starting"}`, true, false},
		{"not json", 200, `<html>bad gateway</html>`, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			h, err := fetchHealth(srv.Client(), srv.URL)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && h.Wired != tc.wired {
				t.Errorf("wired = %v, want %v", h.Wired, tc.wired)
			}
		})
	}
}

func TestWaitForHealthRetriesUntilOK(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		if calls.Add(1) < 3 {
			http.Error(w, "starting", http.StatusBadGateway) // what nginx answers while the API boots
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"wired":false,"version":"v9"}`))
	}))
	defer srv.Close()

	h, ok := waitForHealth(srv.Client(), srv.URL+"/api/health", 10, time.Millisecond)
	if !ok {
		t.Fatal("never became healthy")
	}
	if h.Version != "v9" || h.Wired {
		t.Errorf("health = %+v, want version v9 and wired=false", h)
	}
	if calls.Load() != 3 {
		t.Errorf("polled %d times, want 3", calls.Load())
	}
}

func TestWaitForHealthGivesUp(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if _, ok := waitForHealth(srv.Client(), srv.URL, 4, time.Millisecond); ok {
		t.Error("unhealthy server reported healthy")
	}
	if calls.Load() != 4 {
		t.Errorf("polled %d times, want exactly the 4 attempts", calls.Load())
	}
}

func TestWaitForHealthUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // connection refused from now on

	if _, ok := waitForHealth(&http.Client{Timeout: time.Second}, url, 2, time.Millisecond); ok {
		t.Error("closed server reported healthy")
	}
}

func TestPeliculaBaseURL(t *testing.T) {
	if got := peliculaBaseURL(EnvMap{}); got != "http://localhost:7354" {
		t.Errorf("default = %q", got)
	}
	if got := peliculaBaseURL(EnvMap{"PELICULA_PORT": "7399"}); got != "http://localhost:7399" {
		t.Errorf("custom = %q", got)
	}
}

func TestJSONValue(t *testing.T) {
	cases := []struct{ raw, key, want string }{
		{`{"public_ip":"1.2.3.4"}`, "public_ip", "1.2.3.4"},
		{`{"public_ip":"1.2.3.4","region":"NL"}` + "\n", "public_ip", "1.2.3.4"},
		{`{"port":51234}`, "port", "51234"},
		{`{"port":0}`, "port", "0"},
		{"1.2.3.4\n", "public_ip", "1.2.3.4"},
		{`{"other":"x"}`, "port", `{"other":"x"}`},
		{"", "port", ""},
	}
	for _, tc := range cases {
		if got := jsonValue(tc.raw, tc.key); got != tc.want {
			t.Errorf("jsonValue(%q, %q) = %q, want %q", tc.raw, tc.key, got, tc.want)
		}
	}
}
