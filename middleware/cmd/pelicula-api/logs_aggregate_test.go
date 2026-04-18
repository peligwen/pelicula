package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseLogTimestamp(t *testing.T) {
	ts, line := parseLogTimestamp("2024-01-15T12:34:05.123456789Z sonarr started\n")
	if ts.IsZero() {
		t.Fatal("expected non-zero timestamp")
	}
	if line != "sonarr started\n" {
		t.Errorf("got line %q, want %q", line, "sonarr started\n")
	}

	// No timestamp prefix → zero time, line unchanged
	ts2, line2 := parseLogTimestamp("plain log line")
	if !ts2.IsZero() {
		t.Error("expected zero time for line without timestamp")
	}
	if line2 != "plain log line" {
		t.Errorf("got %q", line2)
	}
}

func TestSortedLogEntries(t *testing.T) {
	entries := []LogEntry{
		{Service: "a", Line: "old", Timestamp: time.Unix(100, 0)},
		{Service: "b", Line: "new", Timestamp: time.Unix(200, 0)},
		{Service: "c", Line: "older", Timestamp: time.Unix(50, 0)},
	}
	got := sortedLogEntries(entries, 2)
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	if got[0].Line != "new" || got[1].Line != "old" {
		t.Errorf("wrong order: got %v %v", got[0].Line, got[1].Line)
	}
}

func TestHandleLogsAggregateFansOut(t *testing.T) {
	origFetch := dockerCli.LogsFunc
	dockerCli.LogsFunc = func(name string, tail int, ts bool) ([]byte, error) {
		switch name {
		case "sonarr":
			return []byte("sonarr line 1\nsonarr line 2\n"), nil
		case "radarr":
			return []byte("radarr line 1\n"), nil
		}
		return []byte(""), nil
	}
	t.Cleanup(func() { dockerCli.LogsFunc = origFetch })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/logs/aggregate?tail=50&services=sonarr,radarr", nil)
	w := httptest.NewRecorder()
	handleLogsAggregate(w, req)

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
