package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gluetunclient "pelicula-api/internal/clients/gluetun"
)

// TestGluetunClient_ErrorOnConnectionRefused verifies the gluetun client returns
// a descriptive error when the control API is unreachable.
func TestGluetunClient_ErrorOnConnectionRefused(t *testing.T) {
	c := gluetunclient.New("http://127.0.0.1:0", "", "")
	_, err := c.GetPortForward(context.Background())
	if err == nil {
		t.Fatal("expected error when control API unreachable, got nil")
	}
}

// TestGluetunClient_ErrorIncludesStatusCode verifies the gluetun client returns
// an error that includes the HTTP status code on non-200 responses.
func TestGluetunClient_ErrorIncludesStatusCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := gluetunclient.New(srv.URL, "", "")
	_, err := c.GetPortForward(context.Background())
	if err == nil {
		t.Fatal("expected error on 401 response, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error %q should include HTTP status code 401", err.Error())
	}
}

// TestGluetunClient_SuccessReturnsPort verifies the gluetun client correctly
// parses the port from a valid GetPortForward response.
func TestGluetunClient_SuccessReturnsPort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"port":51413}`))
	}))
	defer srv.Close()

	old := gluetunClient
	gluetunClient = gluetunclient.New(srv.URL, "", "")
	t.Cleanup(func() { gluetunClient = old })

	port, err := gluetunClient.GetPortForward(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 51413 {
		t.Fatalf("port = %d, want 51413", port)
	}
}
