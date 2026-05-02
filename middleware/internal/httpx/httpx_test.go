package httpx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoverMiddleware_RecoversPanic(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	RecoverMiddleware(handler).ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want status 500, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("want Content-Type application/json, got %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not valid JSON: %v — body: %s", err, w.Body.String())
	}
	if _, ok := body["error"]; !ok {
		t.Fatalf("expected JSON key %q in body, got %v", "error", body)
	}
}

func TestRecoverMiddleware_PassesThroughOnNoPanic(t *testing.T) {
	const wantBody = `{"status":"ok"}`
	const wantStatus = http.StatusOK

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(wantStatus)
		w.Write([]byte(wantBody)) //nolint:errcheck
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	RecoverMiddleware(handler).ServeHTTP(w, r)

	if w.Code != wantStatus {
		t.Fatalf("want status %d, got %d", wantStatus, w.Code)
	}
	if got := w.Body.String(); got != wantBody {
		t.Fatalf("want body %q, got %q", wantBody, got)
	}
}

func TestRecoverMiddleware_LogsStackTrace(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	RecoverMiddleware(handler).ServeHTTP(w, r)

	logged := buf.String()
	if !strings.Contains(logged, "boom") {
		t.Fatalf("expected panic value %q in log output, got: %s", "boom", logged)
	}
	if !strings.Contains(logged, ".go:") {
		t.Fatalf("expected stack frame (.go:) in log output, got: %s", logged)
	}
}
