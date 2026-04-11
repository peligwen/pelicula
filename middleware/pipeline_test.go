package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPipelineGetRequestsPipelineActionType(t *testing.T) {
	called := ""
	procula := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/procula/jobs" {
			called = r.URL.RawQuery
			w.Write([]byte(`[]`))
		}
	}))
	defer procula.Close()

	origP := proculaURL
	proculaURL = procula.URL
	services = &ServiceClients{}
	services.client = &http.Client{}
	t.Cleanup(func() { proculaURL = origP })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/pipeline", nil)
	w := httptest.NewRecorder()
	handlePipelineGet(w, req)

	if !strings.Contains(called, "action_type=pipeline") {
		t.Errorf("procula called with %q, want action_type=pipeline", called)
	}
}
