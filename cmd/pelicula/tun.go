package main

import (
	"fmt"
	"os"
	"runtime"
)

// CheckTUN returns an error if /dev/net/tun is missing on Linux.
// This is called by cmd_up to guard against starting the stack without VPN support.
func CheckTUN() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		return fmt.Errorf("/dev/net/tun not found — create it with:\n  sudo mkdir -p /dev/net && sudo mknod /dev/net/tun c 10 200 && sudo chmod 600 /dev/net/tun")
	}
	return nil
}
