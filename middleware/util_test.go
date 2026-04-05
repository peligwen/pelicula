package main

import (
	"testing"
)

func TestShortHash(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"abcdef1234567890", "abcdef12"},
		{"abcdefgh", "abcdefgh"},        // exactly 8
		{"abc", "abc"},                   // shorter than 8
		{"", ""},
		{"12345678extra", "12345678"},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got := shortHash(c.input)
			if got != c.want {
				t.Errorf("shortHash(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

func TestStrVal(t *testing.T) {
	m := map[string]any{
		"present": "hello",
		"number":  float64(42),
		"null":    nil,
	}
	if got := strVal(m, "present"); got != "hello" {
		t.Errorf("strVal present = %q, want %q", got, "hello")
	}
	if got := strVal(m, "number"); got != "" {
		t.Errorf("strVal number (wrong type) = %q, want empty", got)
	}
	if got := strVal(m, "missing"); got != "" {
		t.Errorf("strVal missing = %q, want empty", got)
	}
	if got := strVal(m, "null"); got != "" {
		t.Errorf("strVal null = %q, want empty", got)
	}
}

func TestFloatVal(t *testing.T) {
	m := map[string]any{
		"num":    float64(3.14),
		"string": "not a number",
	}
	if got := floatVal(m, "num"); got != 3.14 {
		t.Errorf("floatVal num = %v, want 3.14", got)
	}
	if got := floatVal(m, "string"); got != 0 {
		t.Errorf("floatVal string (wrong type) = %v, want 0", got)
	}
	if got := floatVal(m, "missing"); got != 0 {
		t.Errorf("floatVal missing = %v, want 0", got)
	}
}

func TestIntVal(t *testing.T) {
	m := map[string]any{
		"num":    float64(100),
		"string": "not a number",
	}
	if got := intVal(m, "num"); got != 100 {
		t.Errorf("intVal num = %v, want 100", got)
	}
	if got := intVal(m, "string"); got != 0 {
		t.Errorf("intVal string (wrong type) = %v, want 0", got)
	}
	if got := intVal(m, "missing"); got != 0 {
		t.Errorf("intVal missing = %v, want 0", got)
	}
}
