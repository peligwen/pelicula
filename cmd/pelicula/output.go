package main

import (
	"fmt"
	"os"
)

// verboseMode gates info/pass output. Set from main when -v is passed.
var verboseMode bool

// debugMode enables diagnostic output. Set from main when --debug is passed.
var debugMode bool

// isTTY is true when stdout is a terminal (see tty_*.go).
var isTTY = isTerminal(os.Stdout)

// ANSI color codes; empty strings when stdout is not a terminal.
var (
	colorRed    = ansiCode("\033[0;31m")
	colorGreen  = ansiCode("\033[0;32m")
	colorYellow = ansiCode("\033[0;33m")
	colorCyan   = ansiCode("\033[0;36m")
	colorBold   = ansiCode("\033[1m")
	colorReset  = ansiCode("\033[0m")
)

func ansiCode(seq string) string {
	if isTTY {
		return seq
	}
	return ""
}

// ok prints a success line.
func ok(msg string) {
	fmt.Printf("  %s✓%s %s\n", colorGreen, colorReset, msg)
}

func fail(msg string) {
	fmt.Printf("  %s✗%s %s\n", colorRed, colorReset, msg)
}

// info prints a hint line (verbose only).
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

// progress always prints a short milestone line so a failed step is locatable.
func progress(msg string) {
	fmt.Printf("%s▸%s %s\n", colorCyan, colorReset, msg)
}

// debug prints a diagnostic line only when --debug is active.
func debug(msg string) {
	if !debugMode {
		return
	}
	fmt.Printf("%s[debug]%s %s\n", colorYellow, colorReset, msg)
}
