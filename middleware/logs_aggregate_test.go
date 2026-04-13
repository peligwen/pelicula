package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleLogsAggregateFansOut(t *testing.T) {
	origFetch := dockerLogsFunc
	dockerLogsFunc = func(name string, tail int, ts bool) ([]byte, error) {
		switch name {
		case "sonarr":
			return []byte("sonarr line 1\nsonarr line 2\n"), nil
		case "radarr":
			return []byte("radarr line 1\n"), nil
		}
		return []byte(""), nil
	}
	t.Cleanup(func() { dockerLogsFunc = origFetch })

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
