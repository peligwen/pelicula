package cryptogen

import (
	"regexp"
	"testing"
)

func TestGenerateAPIKey_Length(t *testing.T) {
	key := GenerateAPIKey()
	if len(key) != 32 {
		t.Errorf("expected 32 hex chars, got %d: %q", len(key), key)
	}
}

func TestGenerateAPIKey_AllHex(t *testing.T) {
	key := GenerateAPIKey()
	re := regexp.MustCompile(`^[0-9a-f]+$`)
	if !re.MatchString(key) {
		t.Errorf("key contains non-hex characters: %q", key)
	}
}

func TestGenerateAPIKey_Unique(t *testing.T) {
	seen := make(map[string]bool, 50)
	for i := range 50 {
		key := GenerateAPIKey()
		if seen[key] {
			t.Errorf("duplicate key on iteration %d: %q", i, key)
		}
		seen[key] = true
	}
}
