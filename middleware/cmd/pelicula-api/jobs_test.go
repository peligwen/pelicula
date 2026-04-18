package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	appservices "pelicula-api/internal/app/services"
	"pelicula-api/internal/config"
)

func TestHandleJobsListGroupsByState(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/procula/jobs" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		w.Write([]byte(`[
			{"id":"a","state":"queued","stage":"validate","progress":0,"source":{"title":"A","path":"/movies/A/A.mkv"},"action_type":"pipeline"},
			{"id":"b","state":"processing","stage":"process","progress":0.5,"source":{"title":"B","path":"/movies/B/B.mkv"},"action_type":"pipeline"},
			{"id":"c","state":"failed","stage":"validate","progress":0,"error":"boom","source":{"title":"C","path":"/movies/C/C.mkv"},"action_type":"pipeline"},
			{"id":"d","state":"completed","stage":"done","progress":1,"source":{"title":"D","path":"/movies/D/D.mkv"},"action_type":"subtitle_request"}
		]`))
	}))
	defer upstream.Close()

	orig := proculaURL
	origSvc := services
	proculaURL = upstream.URL
	services = appservices.New(&config.Config{}, "")
	t.Cleanup(func() { proculaURL = orig; services = origSvc })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/jobs", nil)
	w := httptest.NewRecorder()
	handleJobsList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Groups map[string][]map[string]any `json:"groups"`
		Total  int                         `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 4 {
		t.Errorf("total = %d, want 4", resp.Total)
	}
	if len(resp.Groups["queued"]) != 1 || len(resp.Groups["processing"]) != 1 ||
		len(resp.Groups["failed"]) != 1 || len(resp.Groups["completed"]) != 1 {
		t.Errorf("groups = %+v", resp.Groups)
	}
}
