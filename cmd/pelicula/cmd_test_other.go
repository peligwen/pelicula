//go:build windows

package main

import (
	"fmt"
	"os"
)

func cmdTest(_ []string) {
	fmt.Fprintln(os.Stderr, "pelicula test is not supported on Windows — run tests/e2e.sh under WSL")
	os.Exit(1)
}
