package settings

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// stubApplier returns counters that let tests assert what was called.
func stubApplier() (a *Applier, seedCalls *atomic.Int32, restartCalls *atomic.Int32, lastURL *string) {
	var seed atomic.Int32
	var restart atomic.Int32
	captured := ""
	return &Applier{
		SeedJellyfinNetworkXML: func(url string) error {
			seed.Add(1)
			captured = url
			return nil
		},
		RestartJellyfin: func() error {
			restart.Add(1)
			return nil
		},
	}, &seed, &restart, &captured
}

func postSettings(t *testing.T, h *Handler, body settingsResponse) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/settings", bytes.NewReader(raw))
	req.Header.Set("Origin", "http://localhost:7354")
	rec := httptest.NewRecorder()
	h.handleSettingsUpdate(rec, req)
	return rec
}

type applyResponse struct {
	Status             string   `json:"status"`
	Applied            []string `json:"applied"`
	Pending            []string `json:"pending"`
	RequiresPeliculaUp bool     `json:"requires_pelicula_up"`
}

func decodeApply(t *testing.T, rec *httptest.ResponseRecorder) applyResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got applyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return got
}

func TestApply_JellyfinPublishedURL_AppliedInPlace(t *testing.T) {
	_, h := newSettingsEnv(t)
	applier, seed, restart, last := stubApplier()
	h.Apply = applier

	rec := postSettings(t, h, settingsResponse{LanUrl: "http://192.168.1.42:7354/jellyfin"})
	resp := decodeApply(t, rec)

	if seed.Load() != 1 {
		t.Errorf("seed called %d times, want 1", seed.Load())
	}
	if restart.Load() != 1 {
		t.Errorf("restart called %d times, want 1", restart.Load())
	}
	if *last != "http://192.168.1.42:7354/jellyfin" {
		t.Errorf("last seeded URL = %q", *last)
	}
	if !contains(resp.Applied, "Jellyfin published URL") {
		t.Errorf("Applied = %v, want includes %q", resp.Applied, "Jellyfin published URL")
	}
	if resp.RequiresPeliculaUp {
		t.Error("requires_pelicula_up should be false when only JF URL changed")
	}
	if len(resp.Pending) != 0 {
		t.Errorf("Pending = %v, want empty", resp.Pending)
	}
}

func TestApply_NoChange_NothingApplied(t *testing.T) {
	_, h := newSettingsEnv(t)
	applier, seed, restart, _ := stubApplier()
	h.Apply = applier

	// Send a payload with no relevant fields changed
	rec := postSettings(t, h, settingsResponse{TZ: "UTC"})
	resp := decodeApply(t, rec)

	if seed.Load() != 0 || restart.Load() != 0 {
		t.Errorf("no apply should fire (seed=%d restart=%d)", seed.Load(), restart.Load())
	}
	if resp.RequiresPeliculaUp {
		t.Error("requires_pelicula_up should be false when nothing remote changed")
	}
}

func TestApply_NilApplier_FallsBackToPending(t *testing.T) {
	_, h := newSettingsEnv(t)
	// h.Apply is nil — middleware not granted Docker access (degenerate setup).

	rec := postSettings(t, h, settingsResponse{LanUrl: "http://10.0.0.5:7354/jellyfin"})
	resp := decodeApply(t, rec)

	if len(resp.Applied) != 0 {
		t.Errorf("Applied = %v, want empty when applier is nil", resp.Applied)
	}
	if !contains(resp.Pending, "Jellyfin published URL") {
		t.Errorf("Pending = %v, want includes JF URL when applier is nil", resp.Pending)
	}
	if !resp.RequiresPeliculaUp {
		t.Error("requires_pelicula_up should be true when even JF URL falls into pending")
	}
}

func TestApply_RestartFails_AppendsRestartReminder(t *testing.T) {
	_, h := newSettingsEnv(t)
	h.Apply = &Applier{
		SeedJellyfinNetworkXML: func(url string) error { return nil },
		RestartJellyfin: func() error {
			return &stubError{msg: "container not found"}
		},
	}

	rec := postSettings(t, h, settingsResponse{LanUrl: "http://10.0.0.6:7354/jellyfin"})
	resp := decodeApply(t, rec)

	if !resp.RequiresPeliculaUp {
		t.Error("requires_pelicula_up should be true when restart fails")
	}
	if !containsContains(resp.Pending, "Jellyfin restart") {
		t.Errorf("Pending = %v, want includes Jellyfin restart hint", resp.Pending)
	}
}

func TestApply_SeedFails_PendingExplainsError(t *testing.T) {
	_, h := newSettingsEnv(t)
	h.Apply = &Applier{
		SeedJellyfinNetworkXML: func(url string) error {
			return &stubError{msg: "permission denied writing network.xml"}
		},
		RestartJellyfin: func() error { return nil },
	}

	rec := postSettings(t, h, settingsResponse{LanUrl: "http://10.0.0.7:7354/jellyfin"})
	resp := decodeApply(t, rec)

	if !resp.RequiresPeliculaUp {
		t.Error("requires_pelicula_up should be true when seed fails")
	}
	if !containsContains(resp.Pending, "permission denied") {
		t.Errorf("Pending = %v, want includes seed error message", resp.Pending)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

type stubError struct{ msg string }

func (e *stubError) Error() string { return e.msg }

func contains(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}

func containsContains(slice []string, sub string) bool {
	for _, s := range slice {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
