package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestGluetunGet_ErrorOnConnectionRefused verifies gluetunGet returns a
// descriptive error when the control API is unreachable.
func TestGluetunGet_ErrorOnConnectionRefused(t *testing.T) {
	origURL := gluetunControlURL
	gluetunControlURL = "http://127.0.0.1:0" // nothing listening
	t.Cleanup(func() { gluetunControlURL = origURL })

	client := &http.Client{Timeout: time.Second}
	_, err := gluetunGet(client, "/v1/portforward")
	if err == nil {
		t.Fatal("expected error when control API unreachable, got nil")
	}
}

// TestGluetunGet_ErrorIncludesStatusCode verifies gluetunGet returns an error
// that includes the HTTP status code on non-200 responses.
func TestGluetunGet_ErrorIncludesStatusCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	origURL := gluetunControlURL
	gluetunControlURL = srv.URL
	t.Cleanup(func() { gluetunControlURL = origURL })

	client := &http.Client{Timeout: time.Second}
	_, err := gluetunGet(client, "/v1/portforward")
	if err == nil {
		t.Fatal("expected error on 401 response, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error %q should include HTTP status code 401", err.Error())
	}
}

// TestGluetunGet_SuccessReturnsBody verifies gluetunGet returns body and nil
// error on a valid 200 response.
func TestGluetunGet_SuccessReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"port":51413}`))
	}))
	defer srv.Close()

	origURL := gluetunControlURL
	gluetunControlURL = srv.URL
	t.Cleanup(func() { gluetunControlURL = origURL })

	client := &http.Client{Timeout: time.Second}
	body, err := gluetunGet(client, "/v1/portforward")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(body) != `{"port":51413}` {
		t.Fatalf("body = %q, want {\"port\":51413}", string(body))
	}
}
