package procula

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestPipelineSettingsDefaults(t *testing.T) {
	// Clear all env-gated knobs so the test is deterministic regardless of caller env.
	t.Setenv("DUALSUB_ENABLED", "")
	t.Setenv("DUALSUB_PAIRS", "")
	t.Setenv("DUALSUB_TRANSLATOR", "")
	t.Setenv("TRANSCODING_ENABLED", "")

	s := defaultSettings()

	if !s.ValidationEnabled {
		t.Error("ValidationEnabled: want true, got false")
	}
	if s.DeleteOnFailure {
		t.Error("DeleteOnFailure: want false, got true")
	}
	if s.DualSubEnabled {
		t.Error("DualSubEnabled: want false, got true")
	}
	if want := []string{"en-es"}; !reflect.DeepEqual(s.DualSubPairs, want) {
		t.Errorf("DualSubPairs: want %v, got %v", want, s.DualSubPairs)
	}
	if s.DualSubTranslator != "none" {
		t.Errorf("DualSubTranslator: want %q, got %q", "none", s.DualSubTranslator)
	}
	if s.TranscodingEnabled {
		t.Error("TranscodingEnabled: want false, got true")
	}
	if !s.CatalogEnabled {
		t.Error("CatalogEnabled: want true, got false")
	}
	if s.NotifMode != "internal" {
		t.Errorf("NotifMode: want %q, got %q", "internal", s.NotifMode)
	}
	if s.StorageWarningPct != 85 {
		t.Errorf("StorageWarningPct: want 85, got %v", s.StorageWarningPct)
	}
	if s.StorageCriticalPct != 95 {
		t.Errorf("StorageCriticalPct: want 95, got %v", s.StorageCriticalPct)
	}
	if s.SubAcquireTimeout != 30 {
		t.Errorf("SubAcquireTimeout: want 30, got %d", s.SubAcquireTimeout)
	}
}

func TestHandleSaveSettings_ClampStorageWarningOver100(t *testing.T) {
	srv := newTestServer(t)

	// Send StorageWarningPct=150; after clamping it must be ≤ 100.
	// The warning < critical invariant may nudge it further down, so assert ≤ 100 not == 100.
	payload := map[string]any{
		"storage_warning_pct":  150,
		"storage_critical_pct": 100,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/procula/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleSaveSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var got PipelineSettings
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.StorageWarningPct > 100 {
		t.Errorf("StorageWarningPct = %v after POST 150, want ≤ 100", got.StorageWarningPct)
	}
}

func TestHandleSaveSettings_ClampStorageCriticalNegative(t *testing.T) {
	srv := newTestServer(t)

	// Send StorageCriticalPct=-10; it must be clamped to 0.
	payload := map[string]any{
		"storage_warning_pct":  0,
		"storage_critical_pct": -10,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/procula/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleSaveSettings(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var got PipelineSettings
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.StorageCriticalPct != 0 {
		t.Errorf("StorageCriticalPct = %v after POST -10, want 0", got.StorageCriticalPct)
	}
}
