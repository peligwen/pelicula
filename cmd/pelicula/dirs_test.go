package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFirstExistingAncestor verifies that firstExistingAncestor returns the
// deepest existing ancestor for paths that do and do not exist.
func TestFirstExistingAncestor(t *testing.T) {
	root := t.TempDir()

	// Create a real two-level subtree: root/a/b exists; root/a/b/c does not.
	existing := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(existing, 0755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "path already exists — returns path itself",
			input: existing,
			want:  existing,
		},
		{
			name:  "one level below existing — returns parent",
			input: filepath.Join(existing, "c"),
			want:  existing,
		},
		{
			name:  "two levels below existing — returns deepest existing ancestor",
			input: filepath.Join(existing, "c", "d"),
			want:  existing,
		},
		{
			name:  "root dir always exists — deep non-existent path eventually hits root",
			input: filepath.Join(root, "x", "y", "z"),
			want:  root,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := firstExistingAncestor(tc.input)
			if got != tc.want {
				t.Errorf("firstExistingAncestor(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestFirstExistingAncestor_NonExistentInputUnderRoot verifies that a completely
// non-existent subtree under a known-existent root returns the root.
func TestFirstExistingAncestor_RootSentinel(t *testing.T) {
	root := t.TempDir()
	// None of these intermediate dirs are created.
	deep := filepath.Join(root, "no", "such", "path")

	got := firstExistingAncestor(deep)
	if got != root {
		t.Errorf("firstExistingAncestor(%q) = %q, want %q (the temp root)", deep, got, root)
	}
}
