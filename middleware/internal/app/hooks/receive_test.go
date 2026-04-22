package hooks_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"pelicula-api/internal/app/hooks"
	proculaclient "pelicula-api/internal/clients/procula"
)

// TestHandleImportHook_SecretHeader exercises the X-Webhook-Secret auth check.
// (a) correct header → 200; (b) missing header → 401; (c) wrong header → 401.
func TestHandleImportHook_SecretHeader(t *testing.T) {
	// t.Setenv requires sequential execution — no t.Parallel.
	const secret = "test-webhook-secret"
	t.Setenv("WEBHOOK_SECRET", secret)

	// Fake Procula that accepts job creation.
	fake := newFakeProcula(t, "/api/procula/jobs", `{"id":"1","status":"queued"}`)
	defer fake.Close()

	h := &hooks.Handler{
		Procula:    proculaclient.New(fake.URL, ""),
		HTTPClient: &http.Client{},
		ProculaURL: fake.URL,
		GetKeys:    func() (string, string, string) { return "", "", "" },
		ArrGet:     func(_, _, _ string) ([]byte, error) { return nil, nil },
	}

	validBody := []byte(`{"eventType":"Download","series":{"id":1,"title":"Test Show","tvdbId":123},"episodes":[{"id":1,"episodeNumber":1,"seasonNumber":1,"title":"Pilot"}],"episodeFile":{"path":"/media/tv/show/s01e01.mkv","quality":{"quality":{"name":"HDTV-1080p"}}},"downloadClient":"qbittorrent","downloadId":"abc123"}`)

	tests := []struct {
		name       string
		headerVal  string // empty means don't set the header
		wantStatus int
	}{
		{"correct header", secret, http.StatusOK},
		{"missing header", "", http.StatusUnauthorized},
		{"wrong header", "wrong-secret", http.StatusUnauthorized},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/pelicula/hooks/import", bytes.NewReader(validBody))
			req.Header.Set("Content-Type", "application/json")
			if tc.headerVal != "" {
				req.Header.Set("X-Webhook-Secret", tc.headerVal)
			}
			w := httptest.NewRecorder()
			h.HandleImportHook(w, req)
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}
}
