package services

import "testing"

func TestVersion_DefaultIsDev(t *testing.T) {
	if Version != "dev" {
		t.Errorf("expected Version == %q, got %q", "dev", Version)
	}
}

func TestVersion_NotEmpty(t *testing.T) {
	if Version == "" {
		t.Error("Version must not be empty")
	}
}
