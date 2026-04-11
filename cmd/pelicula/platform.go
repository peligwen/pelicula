package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Platform holds detected host environment info.
type Platform struct {
	OS        string // "darwin", "linux", "windows"
	IsSynology bool
	IsWSL      bool
	NeedsSudo  bool
	TZ         string
	UID        int
	GID        int

	DefaultConfigDir  string
	DefaultLibraryDir string
	DefaultWorkDir    string
}

// Detect runs all platform detection and returns a filled Platform.
func Detect(scriptDir string) Platform {
	p := Platform{}
	p.OS = runtime.GOOS
	p.UID = os.Getuid()
	p.GID = os.Getgid()
	// os.Getuid/Getgid return -1 on Windows — default to 1000
	if p.UID < 0 {
		p.UID = 1000
	}
	if p.GID < 0 {
		p.GID = 1000
	}

	// Synology detection
	if _, err := os.Stat("/proc/syno_platform"); err == nil {
		p.IsSynology = true
	} else if _, err := os.Stat("/volume1"); err == nil {
		p.IsSynology = true
	}

	// WSL detection (Linux only)
	if p.OS == "linux" {
		if data, err := os.ReadFile("/proc/version"); err == nil {
			lower := strings.ToLower(string(data))
			if strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl") {
				p.IsWSL = true
			}
		}
	}

	// Timezone detection
	p.TZ = detectTZ()

	// Docker sudo detection
	p.NeedsSudo = detectSudo()

	// Default paths
	if p.IsSynology {
		p.DefaultConfigDir = "/volume1/docker/pelicula/config"
		p.DefaultLibraryDir = "/volume1/media"
		p.DefaultWorkDir = "/volume1/media"
	} else {
		home, _ := os.UserHomeDir()
		p.DefaultConfigDir = scriptDir + "/config"
		p.DefaultLibraryDir = home + "/media"
		p.DefaultWorkDir = home + "/media"
	}

	return p
}

// PlatformLabel returns a human-readable platform label.
func (p Platform) PlatformLabel() string {
	if p.IsSynology {
		return "Synology NAS"
	}
	switch p.OS {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	}
	if p.IsWSL {
		if name := os.Getenv("WSL_DISTRO_NAME"); name != "" {
			return "WSL (" + name + ")"
		}
		return "WSL"
	}
	return "Linux"
}

// HostPlatformID returns the platform string used in the setup container env vars.
func (p Platform) HostPlatformID() string {
	if p.IsSynology {
		return "synology"
	}
	if p.OS == "darwin" {
		return "macos"
	}
	if p.IsWSL {
		return "wsl"
	}
	return "linux"
}

func detectTZ() string {
	// Try reading /etc/localtime symlink
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		if idx := strings.Index(link, "zoneinfo/"); idx >= 0 {
			return link[idx+len("zoneinfo/"):]
		}
	}
	// Try /etc/timezone file
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		tz := strings.TrimSpace(string(data))
		if tz != "" {
			return tz
		}
	}
	return "UTC"
}

// detectLANURL walks host interfaces and returns an http URL of the first
// non-loopback IPv4 address in an RFC1918 range, formatted for the nginx
// dashboard port. Returns empty string if no suitable interface is found
// (no network, all loopback, or all public/APIPA addresses).
//
// Used to populate HOST_LAN_URL so the setup wizard can prefill a Jellyfin
// PublishedServerUrl — what clients on the LAN should see when they discover
// the server over UDP 7359 broadcast.
func detectLANURL() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP
		if ip == nil || ip.IsLoopback() {
			continue
		}
		ip4 := ip.To4()
		if ip4 == nil {
			continue
		}
		if isRFC1918(ip4) {
			return fmt.Sprintf("http://%s:7354/jellyfin", ip4.String())
		}
	}
	return ""
}

// isRFC1918 reports whether ip is in 10.0.0.0/8, 172.16.0.0/12, or
// 192.168.0.0/16.
func isRFC1918(ip net.IP) bool {
	if ip == nil {
		return false
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	switch {
	case ip4[0] == 10:
		return true
	case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
		return true
	case ip4[0] == 192 && ip4[1] == 168:
		return true
	}
	return false
}

func detectSudo() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	// Try docker info without sudo
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err == nil {
		return false
	}
	// Try with sudo (-n = non-interactive: fail immediately if a password prompt would appear)
	cmd2 := exec.Command("sudo", "-n", "docker", "info")
	cmd2.Stdout = nil
	cmd2.Stderr = nil
	if err := cmd2.Run(); err == nil {
		return true
	}
	return false
}
