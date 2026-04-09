//go:build windows

package main

import "os"

// isTerminal always returns false on Windows (color output disabled).
func isTerminal(_ *os.File) bool {
	return false
}
