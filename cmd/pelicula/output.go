package main

import (
	"fmt"
	"os"
)

// verboseMode gates debug-level output. Set from main.go when -v is passed.
var verboseMode bool

// isTTY is true when stdout is a terminal. Detected once at startup via isTerminal (tty.go).
var isTTY = isTerminal(os.Stdout)

// ANSI color codes — empty strings when stdout is not a terminal.
var (
	colorRed    = ansiCode("\033[0;31m")
	colorGreen  = ansiCode("\033[0;32m")
	colorYellow = ansiCode("\033[0;33m")
	colorCyan   = ansiCode("\033[0;36m")
	colorBold   = ansiCode("\033[1m")
	colorReset  = ansiCode("\033[0m")
)

// ansiCode returns the escape sequence only when stdout is a terminal.
func ansiCode(seq string) string {
	if isTTY {
		return seq
	}
	return ""
}

func pass(msg string) {
	if !verboseMode {
		return
	}
	fmt.Printf("  %s✓%s %s\n", colorGreen, colorReset, msg)
}

func fail(msg string) {
	fmt.Printf("  %s✗%s %s\n", colorRed, colorReset, msg)
}

func info(msg string) {
	if !verboseMode {
		return
	}
	fmt.Printf("%s→%s %s\n", colorCyan, colorReset, msg)
}

func warn(msg string) {
	fmt.Printf("%s!%s %s\n", colorYellow, colorReset, msg)
}

func fatal(msg string) {
	fmt.Fprintf(os.Stderr, "%s✗%s %s\n", colorRed, colorReset, msg)
	os.Exit(1)
}

func bold(s string) string {
	return colorBold + s + colorReset
}
