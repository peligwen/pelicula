package main

import (
	"fmt"
	"os"
)

const (
	colorRed    = "\033[0;31m"
	colorGreen  = "\033[0;32m"
	colorYellow = "\033[0;33m"
	colorCyan   = "\033[0;36m"
	colorBold   = "\033[1m"
	colorReset  = "\033[0m"
)

func pass(msg string) {
	fmt.Printf("  %s✓%s %s\n", colorGreen, colorReset, msg)
}

func fail(msg string) {
	fmt.Printf("  %s✗%s %s\n", colorRed, colorReset, msg)
}

func info(msg string) {
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
