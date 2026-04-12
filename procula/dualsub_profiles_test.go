package main

import (
	"testing"
)

func TestBuiltinDualSubProfiles(t *testing.T) {
	profiles := builtinDualSubProfiles()
	if len(profiles) != 3 {
		t.Fatalf("expected 3 built-in profiles, got %d", len(profiles))
	}
	names := map[string]bool{}
	for _, p := range profiles {
		names[p.Name] = true
		if !p.Builtin {
			t.Errorf("profile %q: Builtin should be true", p.Name)
		}
		if p.FontSize <= 0 {
			t.Errorf("profile %q: FontSize must be positive", p.Name)
		}
		if p.FontName == "" {
			t.Errorf("profile %q: FontName must not be empty", p.Name)
		}
		switch p.Layout {
		case "stacked_bottom", "stacked_top", "split":
		default:
			t.Errorf("profile %q: unknown layout %q", p.Name, p.Layout)
		}
	}
	if !names["Default"] {
		t.Error("missing built-in profile 'Default'")
	}
	// Default must be stacked_bottom
	for _, p := range profiles {
		if p.Name == "Default" && p.Layout != "stacked_bottom" {
			t.Errorf("Default profile layout = %q, want stacked_bottom", p.Layout)
		}
	}
}
