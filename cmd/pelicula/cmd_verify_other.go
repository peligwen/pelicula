//go:build windows

package main

import (
	"fmt"
	"os"
)

func cmdVerify(_ *Context, _ []string) {
	fmt.Fprintln(os.Stderr, "pelicula verify is not supported on Windows — run tests/verify.sh under WSL")
	os.Exit(1)
}
