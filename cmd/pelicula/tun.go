package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const tunOverrideContent = `services:
  gluetun:
    devices:
      - /dev/net/tun:/dev/net/tun
`

// SetupTUN checks for /dev/net/tun on Linux and writes compose/docker-compose.override.yml.
// On macOS/Windows, Docker Desktop handles TUN devices natively — nothing to do.
func SetupTUN(scriptDir string) error {
	if runtime.GOOS != "linux" {
		return nil
	}

	tunPath := "/dev/net/tun"
	if _, err := os.Stat(tunPath); err != nil {
		// TUN device missing — warn the user
		warn("/dev/net/tun not found (required for VPN)")
		fmt.Print("  Create it now with sudo? [Y/n]: ")
		var answer string
		fmt.Scanln(&answer)
		if answer != "n" && answer != "N" {
			if err := createTUN(); err != nil {
				return fmt.Errorf("create /dev/net/tun: %w", err)
			}
			pass("Created /dev/net/tun")

			// Synology-specific note
			if _, statErr := os.Stat("/proc/syno_platform"); statErr == nil {
				warn("Add a boot-up task in DSM to recreate this on reboot:")
				fmt.Println("    Control Panel → Task Scheduler → Triggered Task (Boot-up, root)")
				fmt.Println("    Script: mkdir -p /dev/net && mknod /dev/net/tun c 10 200 && chmod 600 /dev/net/tun")
			} else {
				warn("This device node may not persist across reboots on some systems")
			}
		}
	} else {
		pass("/dev/net/tun exists")
	}

	// Write compose/docker-compose.override.yml with TUN device mapping
	overrideFile := filepath.Join(scriptDir, "compose", "docker-compose.override.yml")
	if _, err := os.Stat(overrideFile); err == nil {
		// Back up existing override
		bak := fmt.Sprintf("%s.bak.%d", overrideFile, time.Now().Unix())
		_ = copyFile(overrideFile, bak)
	}
	if err := os.WriteFile(overrideFile, []byte(tunOverrideContent), 0644); err != nil {
		return fmt.Errorf("write compose/docker-compose.override.yml: %w", err)
	}
	pass("Wrote compose/docker-compose.override.yml (TUN device mapping)")
	return nil
}

// CheckTUN returns an error if /dev/net/tun is missing on Linux.
// This is called by cmd_up to guard against starting the stack without VPN support.
func CheckTUN() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		return fmt.Errorf("/dev/net/tun not found — run: pelicula setup")
	}
	return nil
}

// createTUN creates /dev/net/tun using mknod via sudo.
func createTUN() error {
	// mkdir -p /dev/net
	mkdirCmd := sudoRun("mkdir", "-p", "/dev/net")
	if err := mkdirCmd.Run(); err != nil {
		return fmt.Errorf("mkdir /dev/net: %w", err)
	}
	// mknod /dev/net/tun c 10 200
	mknodCmd := sudoRun("mknod", "/dev/net/tun", "c", "10", "200")
	if err := mknodCmd.Run(); err != nil {
		return fmt.Errorf("mknod: %w", err)
	}
	// chmod 600 /dev/net/tun
	chmodCmd := sudoRun("chmod", "600", "/dev/net/tun")
	if err := chmodCmd.Run(); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	return nil
}
