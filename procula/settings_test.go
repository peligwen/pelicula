package procula

import (
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
