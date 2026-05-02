package sysinfo_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"pelicula-api/internal/app/sysinfo"
	"pelicula-api/internal/clients/docker"
)

// TestFormatUptime is moved from cmd/pelicula-api/host_test.go.
// formatUptime is package-internal; we access it via the exported package
// by testing the observable output format.
func TestFormatUptime(t *testing.T) {
	cases := []struct {
		secs float64
		want string
	}{
		{0, "0h 0m"},
		{59, "0h 0m"},
		{60, "0h 1m"},
		{3599, "0h 59m"},
		{3600, "1h 0m"},
		{3661, "1h 1m"},
		{86399, "23h 59m"},
		{86400, "1d 0h"},
		{86400 + 3600, "1d 1h"},
		{3*86400 + 4*3600 + 30*60, "3d 4h"},
		{7 * 86400, "7d 0h"},
	}
	for _, tc := range cases {
		got := sysinfo.FormatUptime(tc.secs)
		if got != tc.want {
			t.Errorf("FormatUptime(%v) = %q, want %q", tc.secs, got, tc.want)
		}
	}
}

func TestParseLogTimestamp(t *testing.T) {
	ts, line := sysinfo.ParseLogTimestamp("2024-01-15T12:34:05.123456789Z sonarr started\n")
	if ts.IsZero() {
		t.Fatal("expected non-zero timestamp")
	}
	if line != "sonarr started\n" {
		t.Errorf("got line %q, want %q", line, "sonarr started\n")
	}

	// No timestamp prefix → zero time, line unchanged
	ts2, line2 := sysinfo.ParseLogTimestamp("plain log line")
	if !ts2.IsZero() {
		t.Error("expected zero time for line without timestamp")
	}
	if line2 != "plain log line" {
		t.Errorf("got %q", line2)
	}
}

func TestSortedLogEntries(t *testing.T) {
	entries := []sysinfo.LogEntry{
		{Service: "a", Line: "old", Timestamp: time.Unix(100, 0)},
		{Service: "b", Line: "new", Timestamp: time.Unix(200, 0)},
		{Service: "c", Line: "older", Timestamp: time.Unix(50, 0)},
	}
	got := sysinfo.SortedLogEntries(entries, 2)
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	if got[0].Line != "new" || got[1].Line != "old" {
		t.Errorf("wrong order: got %v %v", got[0].Line, got[1].Line)
	}
}

// TestSpeedTest_ConcurrentReturnsBusy fires two concurrent speedtest requests
// against a slow httptest server and expects exactly one 200 (the winner) and
// one 429 (busy) — no mutex held across the I/O.
func TestSpeedTest_ConcurrentReturnsBusy(t *testing.T) {
	// Slow server: holds the connection open for 200ms to ensure concurrency.
	started := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer ts.Close()

	sysinfo.ResetSpeedTestState()
	sysinfo.SetSpeedTestURL(ts.URL)
	sysinfo.SetSpeedTestProxyDirect()
	defer func() {
		sysinfo.ResetSpeedTestState()
		sysinfo.SetSpeedTestURL("https://speed.cloudflare.com/__down?bytes=10000000")
		sysinfo.ClearSpeedTestProxyOverride()
	}()

	h := &sysinfo.Handler{}

	var (
		mu    sync.Mutex
		codes []int
		wg    sync.WaitGroup
	)

	fire := func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodPost, "/api/pelicula/speedtest", nil)
		w := httptest.NewRecorder()
		h.ServeSpeedtest(w, req)
		mu.Lock()
		codes = append(codes, w.Code)
		mu.Unlock()
	}

	wg.Add(1)
	go fire()
	// Wait until the slow server has accepted the first request before firing
	// the second, so the inflight flag is definitely set.
	<-started
	wg.Add(1)
	go fire()

	wg.Wait()

	got200, got429 := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			got200++
		case http.StatusTooManyRequests:
			got429++
		}
	}
	if got200 != 1 || got429 != 1 {
		t.Errorf("want 1×200 and 1×429; got codes %v", codes)
	}
}

// TestSpeedTest_RespectsCtx cancels the request context after 50ms and
// confirms the handler returns promptly rather than waiting for the full
// slow server response.
func TestSpeedTest_RespectsCtx(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hold for longer than the context deadline.
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer ts.Close()

	sysinfo.ResetSpeedTestState()
	sysinfo.SetSpeedTestURL(ts.URL)
	sysinfo.SetSpeedTestProxyDirect()
	defer func() {
		sysinfo.ResetSpeedTestState()
		sysinfo.SetSpeedTestURL("https://speed.cloudflare.com/__down?bytes=10000000")
		sysinfo.ClearSpeedTestProxyOverride()
	}()

	h := &sysinfo.Handler{}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/speedtest", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	start := time.Now()
	h.ServeSpeedtest(w, req)
	elapsed := time.Since(start)

	// Handler should return well before the server's 500ms sleep.
	if elapsed > 300*time.Millisecond {
		t.Errorf("handler took %v; expected prompt return on ctx cancellation", elapsed)
	}

	// Response must be JSON with an error field.
	var result struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v — body: %s", err, w.Body.String())
	}
	if result.Error == "" {
		t.Error("expected non-empty error in result on ctx cancellation")
	}
}

// TestSpeedTest_CachedResultWithinCooldown verifies that a second request
// within the cooldown window returns the cached result without hitting upstream.
func TestSpeedTest_CachedResultWithinCooldown(t *testing.T) {
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer ts.Close()

	sysinfo.ResetSpeedTestState()
	sysinfo.SetSpeedTestURL(ts.URL)
	sysinfo.SetSpeedTestProxyDirect()
	defer func() {
		sysinfo.ResetSpeedTestState()
		sysinfo.SetSpeedTestURL("https://speed.cloudflare.com/__down?bytes=10000000")
		sysinfo.ClearSpeedTestProxyOverride()
	}()

	h := &sysinfo.Handler{}

	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/pelicula/speedtest", nil)
		w := httptest.NewRecorder()
		h.ServeSpeedtest(w, req)
		return w
	}

	w1 := call()
	if w1.Code != http.StatusOK {
		t.Fatalf("first call: code = %d, body = %s", w1.Code, w1.Body.String())
	}

	w2 := call()
	if w2.Code != http.StatusOK {
		t.Fatalf("second call: code = %d, body = %s", w2.Code, w2.Body.String())
	}

	if hits != 1 {
		t.Errorf("upstream hit %d times; want 1 (second call should use cache)", hits)
	}

	// Both responses should decode to a valid result with no error.
	for i, w := range []*httptest.ResponseRecorder{w1, w2} {
		var result struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatalf("call %d: unmarshal: %v", i+1, err)
		}
		if result.Error != "" {
			t.Errorf("call %d: unexpected error: %s", i+1, result.Error)
		}
	}
}

func TestHandleLogsAggregateFansOut(t *testing.T) {
	dc := docker.New("http://docker-proxy:2375", "pelicula")
	dc.LogsFunc = func(ctx context.Context, name string, tail int, ts bool) ([]byte, error) {
		switch name {
		case "sonarr":
			return []byte("sonarr line 1\nsonarr line 2\n"), nil
		case "radarr":
			return []byte("radarr line 1\n"), nil
		}
		return []byte(""), nil
	}

	h := &sysinfo.Handler{
		DockerClient: dc,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/logs/aggregate?tail=50&services=sonarr,radarr", nil)
	w := httptest.NewRecorder()
	h.ServeLogs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Entries []struct {
			Service string `json:"service"`
			Line    string `json:"line"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var services []string
	for _, e := range resp.Entries {
		services = append(services, e.Service)
	}
	joined := strings.Join(services, ",")
	if !strings.Contains(joined, "sonarr") || !strings.Contains(joined, "radarr") {
		t.Fatalf("entries missing services: %v", services)
	}
	if len(resp.Entries) != 3 {
		t.Errorf("entries = %d, want 3", len(resp.Entries))
	}
}
