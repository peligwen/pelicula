package main

import (
	"net/http"
	"net/http/httptest"
	"pelicula-api/internal/app/adminops"
	"strings"
	"testing"
)

func TestHandleVPNRestart_RejectsGET(t *testing.T) {
	h := adminops.New(dockerCli, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/admin/vpn/restart", nil)
	w := httptest.NewRecorder()
	h.HandleVPNRestart(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestHandleVPNRestart_Post_ReturnsOK(t *testing.T) {
	// With no docker proxy available, dockerCli.Restart errors but handler still
	// returns 200 with ok:true and errors listed — never a 5xx.
	h := adminops.New(dockerCli, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/admin/vpn/restart", nil)
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	h.HandleVPNRestart(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("body does not contain ok:true: %s", w.Body.String())
	}
}
