package clients

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestIsValidUsername(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty", "", false},
		{"single char", "a", true},
		{"64 chars exact", strings.Repeat("a", 64), true},
		{"65 chars reject", strings.Repeat("a", 65), false},
		{"leading space", " alice", false},
		{"trailing space", "alice ", false},
		{"embedded space", "alice bob", true},
		{"slash", "/etc", false},
		{"backslash", `a\b`, false},
		{"nul byte", "a\x00b", false},
		{"tab", "a\tb", false},
		{"newline", "a\nb", false},
		{"unicode letters", "André", true},
		{"japanese", "山田太郎", true},
		// pins current behavior: zero-width space (U+200B) is not rejected
		// by unicode.IsControl or strings.TrimSpace
		{"zero-width space embedded", "a​b", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsValidUsername(tc.input)
			if got != tc.want {
				t.Errorf("IsValidUsername(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestJellyfinHTTPError_Format(t *testing.T) {
	err := &JellyfinHTTPError{StatusCode: 401}
	if got := err.Error(); got != "HTTP 401" {
		t.Errorf("JellyfinHTTPError.Error() = %q, want %q", got, "HTTP 401")
	}
}

func TestErrPasswordRequired_Identity(t *testing.T) {
	if !errors.Is(ErrPasswordRequired, ErrPasswordRequired) {
		t.Error("errors.Is(ErrPasswordRequired, ErrPasswordRequired) should be true")
	}

	wrapped := fmt.Errorf("create user: %w", ErrPasswordRequired)
	if !errors.Is(wrapped, ErrPasswordRequired) {
		t.Error("wrapped ErrPasswordRequired should unwrap via errors.Is")
	}

	other := errors.New("other")
	if errors.Is(other, ErrPasswordRequired) {
		t.Error("unrelated error should not match ErrPasswordRequired")
	}
}
