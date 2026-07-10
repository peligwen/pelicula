package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written to it. usage() prints via fmt.Print, which always
// targets the real os.Stdout, so this is the only way to observe its output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = orig
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(out)
}

// TestUsage_ListsAllDispatchedCommands is the CIT-9 regression test: every
// first-class subcommand handled by main()'s dispatch switch must be
// discoverable in `pelicula --help`. This previously omitted "test" and
// "restart-acquire" even though both were live dispatch cases.
func TestUsage_ListsAllDispatchedCommands(t *testing.T) {
	// Mirrors the case labels in main()'s switch (args[0]) block.
	dispatched := []string{
		"up", "down", "restart", "restart-acquire", "rebuild", "redeploy",
		"reset-config", "status", "logs", "check-vpn", "update",
		"export", "import-backup", "import", "test", "verify", "doctor",
	}

	out := captureStdout(t, usage)
	lines := strings.Split(out, "\n")

	for _, cmd := range dispatched {
		found := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			// Command lines look like "  restart [service]   ..." or
			// "  restart-acquire     ...": the command is the first
			// whitespace-delimited token. Match on that token rather than a
			// bare substring so e.g. "up" doesn't false-positive against
			// "setup" in a description column.
			if trimmed == cmd || strings.HasPrefix(trimmed, cmd+" ") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("usage() output missing a command line for %q", cmd)
		}
	}
}
