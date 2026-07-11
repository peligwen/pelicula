package main

import (
	"os"
	"path/filepath"
	"strings"
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

// TestWriteEnvFile_SingleTimestampedBackup is the regression test for the
// resetConfigAll double-backup fix: writeEnvFile must produce exactly one
// backup of a pre-existing .env — its own timestamped ".env.bak.<unix>" —
// and must NOT also trigger WriteEnv's plain ".env.bak" (writeEnvFile calls
// writeEnvNoBackup internally for exactly this reason).
func TestWriteEnvFile_SingleTimestampedBackup(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")

	const oldContent = "CONFIG_DIR=\"/old/config\"\n"
	if err := os.WriteFile(envPath, []byte(oldContent), 0600); err != nil {
		t.Fatal(err)
	}

	if err := writeEnvFile(envPath, "/new/config", "/new/lib", "/new/work",
		"1000", "1000", "UTC", "wgkey", "Netherlands", "7354",
		"admin", "proculakey", "jfpass", nil); err != nil {
		t.Fatalf("writeEnvFile: %v", err)
	}

	// The plain ".bak" (WriteEnv's own backup) must NOT exist — only the
	// timestamped one should have been written.
	if _, err := os.Stat(envPath + ".bak"); !os.IsNotExist(err) {
		t.Errorf("plain %s.bak should not exist (redundant backup) — writeEnvFile should be the only backup writer", envPath)
	}

	// Exactly one timestamped backup should exist, containing the old content.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var timestamped []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".env.bak.") {
			timestamped = append(timestamped, e.Name())
		}
	}
	if len(timestamped) != 1 {
		t.Fatalf("expected exactly 1 timestamped backup, found %d: %v", len(timestamped), timestamped)
	}
	backupContent, err := os.ReadFile(filepath.Join(dir, timestamped[0]))
	if err != nil {
		t.Fatal(err)
	}
	if string(backupContent) != oldContent {
		t.Errorf("backup content = %q, want %q", backupContent, oldContent)
	}

	// The .env file itself should now hold the new content.
	m, err := ParseEnv(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if m["CONFIG_DIR"] != "/new/config" {
		t.Errorf("CONFIG_DIR = %q, want /new/config", m["CONFIG_DIR"])
	}
}
