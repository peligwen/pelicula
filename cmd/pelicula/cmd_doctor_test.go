package main

import "testing"

// TestFilterErrorLines verifies that filterErrorLines correctly matches lines
// containing error-like keywords. Matching is by substring (case-insensitive),
// so "errored" also matches — this is expected behavior given the regex used.
func TestFilterErrorLines(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantLen  int
		wantNone bool
	}{
		{
			name:     "clean log — no matches",
			input:    "INFO starting service\nINFO ready\nDEBUG poll tick",
			wantLen:  0,
			wantNone: true,
		},
		{
			name:    "lowercase error",
			input:   "2026-04-22 some error occurred",
			wantLen: 1,
		},
		{
			name:    "uppercase ERROR",
			input:   "2026-04-22 [ERROR] connection refused",
			wantLen: 1,
		},
		{
			name:    "Warning (mixed case)",
			input:   "Warning: disk usage high",
			wantLen: 1,
		},
		{
			name:    "Fatal",
			input:   "Fatal: could not bind port",
			wantLen: 1,
		},
		{
			name:    "PANIC",
			input:   "PANIC: nil pointer dereference",
			wantLen: 1,
		},
		{
			name: "substring errored also matches",
			// "errored" contains "error" — substring matching is the documented behavior.
			input:   "request errored after timeout",
			wantLen: 1,
		},
		{
			name:     "empty input — empty slice",
			input:    "",
			wantLen:  0,
			wantNone: true,
		},
		{
			name:    "multiple matching lines",
			input:   "INFO ok\nERROR boom\nWARNING: disk low\nINFO done",
			wantLen: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterErrorLines([]byte(tc.input))
			if tc.wantNone && got != nil && len(got) != 0 {
				t.Errorf("expected empty/nil slice, got %v", got)
			}
			if len(got) != tc.wantLen {
				t.Errorf("expected %d lines, got %d: %v", tc.wantLen, len(got), got)
			}
		})
	}
}
