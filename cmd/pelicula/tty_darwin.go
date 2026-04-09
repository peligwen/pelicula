//go:build darwin

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// isTerminal reports whether f is connected to a terminal.
func isTerminal(f *os.File) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGETA, uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}
