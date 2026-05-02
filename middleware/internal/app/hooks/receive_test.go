package hooks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"pelicula-api/internal/app/hooks"
	proculaclient "pelicula-api/internal/clients/procula"
	qbtclient "pelicula-api/internal/clients/qbt"
)

// newRadarrImportBody builds a minimal Radarr Download webhook body with the given downloadId.
func newRadarrImportBody(downloadID string) []byte {
	raw := map[string]any{
		"eventType":  "Download",
		"downloadId": downloadID,
		"movie": map[string]any{
			"title": "Dune",
			"year":  float64(2021),
			"id":    float64(1),
		},
		"movieFile": map[string]any{
			"path": "/media/movies/Dune (2021)/dune.mkv",
			"size": float64(5_000_000_000),
		},
	}
	b, _ := json.Marshal(raw)
	return b
}

// TestSeedingRemoveOnComplete_NoLongerReadsEnv confirms that
// SeedingRemoveOnComplete is controlled by the Handler field, not os.Getenv.
// When env is "true" but field is false, no qBT removal call is made.
// Note: cannot be parallel — uses t.Setenv which mutates process environment.
func TestSeedingRemoveOnComplete_NoLongerReadsEnv(t *testing.T) {
	t.Setenv("SEEDING_REMOVE_ON_COMPLETE", "true")

	var removeCalled atomic.Bool
	fakeQBT := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		removeCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer fakeQBT.Close()

	fakeProcula := newFakeProcula(t, "/api/procula/jobs", `{"id":"1","status":"queued"}`)
	defer fakeProcula.Close()

	h := &hooks.Handler{
		Procula:                 proculaclient.New(fakeProcula.URL, ""),
		HTTPClient:              &http.Client{},
		ProculaURL:              fakeProcula.URL,
		SeedingRemoveOnComplete: false, // field is false; env is "true"
		Qbt:                     qbtclient.New(fakeQBT.URL),
		GetKeys:                 func() (string, string, string) { return "", "", "" },
		ArrGet:                  func(_ context.Context, _, _, _ string) ([]byte, error) { return nil, nil },
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/hooks/import", bytes.NewReader(newRadarrImportBody("hash-abc")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleImportHook(w, req)

	if removeCalled.Load() {
		t.Error("qBT remove was called but SeedingRemoveOnComplete field was false — env var must still be read")
	}
}

// TestSeedingRemoveOnComplete_HandlerFieldTrue confirms that when the
// SeedingRemoveOnComplete field is true (env unset), the qBT removal is triggered.
func TestSeedingRemoveOnComplete_HandlerFieldTrue(t *testing.T) {
	t.Parallel()

	var removePath string
	fakeQBT := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		removePath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer fakeQBT.Close()

	fakeProcula := newFakeProcula(t, "/api/procula/jobs", `{"id":"1","status":"queued"}`)
	defer fakeProcula.Close()

	h := &hooks.Handler{
		Procula:                 proculaclient.New(fakeProcula.URL, ""),
		HTTPClient:              &http.Client{},
		ProculaURL:              fakeProcula.URL,
		SeedingRemoveOnComplete: true, // field true; env not set
		Qbt:                     qbtclient.New(fakeQBT.URL),
		GetKeys:                 func() (string, string, string) { return "", "", "" },
		ArrGet:                  func(_ context.Context, _, _, _ string) ([]byte, error) { return nil, nil },
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/hooks/import", bytes.NewReader(newRadarrImportBody("hash-xyz")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleImportHook(w, req)

	if removePath == "" {
		t.Error("qBT remove was not called but SeedingRemoveOnComplete field was true")
	}
}

// TestHandleImportHook_SecretHeader exercises the X-Webhook-Secret auth check.
// (a) correct header → 200; (b) missing header → 401; (c) wrong header → 401.
func TestHandleImportHook_SecretHeader(t *testing.T) {
	const secret = "test-webhook-secret"

	// Fake Procula that accepts job creation.
	fake := newFakeProcula(t, "/api/procula/jobs", `{"id":"1","status":"queued"}`)
	defer fake.Close()

	h := &hooks.Handler{
		Procula:       proculaclient.New(fake.URL, ""),
		HTTPClient:    &http.Client{},
		ProculaURL:    fake.URL,
		WebhookSecret: secret,
		GetKeys:       func() (string, string, string) { return "", "", "" },
		ArrGet:        func(_ context.Context, _, _, _ string) ([]byte, error) { return nil, nil },
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
