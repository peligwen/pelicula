package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Platform holds detected host environment info.
type Platform struct {
	OS         string // "darwin", "linux", "windows"
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

// synologyDockerPath is where DSM installs the docker CLI. It is not on
// sudo's secure_path, so `sudo docker` fails unless the absolute path is used.
const synologyDockerPath = "/usr/local/bin/docker"

// Detect runs all platform detection and returns a filled Platform.
func Detect(scriptDir string) Platform {
	p := Platform{OS: runtime.GOOS, UID: os.Getuid(), GID: os.Getgid()}
	// os.Getuid/Getgid return -1 on Windows.
	if p.UID < 0 {
		p.UID = 1000
	}
	if p.GID < 0 {
		p.GID = 1000
	}

	// /proc/syno_platform is a Synology-only kernel marker. A bare /volume1
	// directory can exist on any Linux host, so it is never used.
	if _, err := os.Stat("/proc/syno_platform"); err == nil {
		p.IsSynology = true
	}

	if p.OS == "linux" {
		if data, err := os.ReadFile("/proc/version"); err == nil {
			lower := strings.ToLower(string(data))
			p.IsWSL = strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl")
		}
	}

	p.TZ = detectTZ()
	p.NeedsSudo = detectSudo(dockerBinary(p.IsSynology, fileExists))

	home, _ := os.UserHomeDir()
	p.DefaultConfigDir, p.DefaultLibraryDir, p.DefaultWorkDir = platformDefaults(p.IsSynology, scriptDir, home)
	return p
}

// platformDefaults returns the default CONFIG_DIR, LIBRARY_DIR and WORK_DIR
// offered by the setup wizard. Config lives next to the repo (fast local
// disk); the library and downloads share one tree so imports can hardlink.
func platformDefaults(isSynology bool, scriptDir, home string) (config, library, work string) {
	if isSynology {
		return "/volume1/docker/pelicula/config", "/volume1/media", "/volume1/media"
	}
	return filepath.Join(scriptDir, "config"), filepath.Join(home, "media"), filepath.Join(home, "media")
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

// dockerBinary returns the docker executable to run. On Synology the absolute
// DSM path is preferred when it exists (sudo does not search /usr/local/bin).
func dockerBinary(isSynology bool, exists func(string) bool) string {
	if isSynology && exists(synologyDockerPath) {
		return synologyDockerPath
	}
	return "docker"
}

func detectTZ() string {
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		if idx := strings.Index(link, "zoneinfo/"); idx >= 0 {
			return link[idx+len("zoneinfo/"):]
		}
	}
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		if tz := strings.TrimSpace(string(data)); tz != "" {
			return tz
		}
	}
	return "UTC"
}

// detectSudo reports whether docker needs sudo: false when `docker info`
// works as the current user, true when only `sudo -n docker info` does.
func detectSudo(docker string) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	const timeout = 5 * time.Second
	run := func(name string, args ...string) bool {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return exec.CommandContext(ctx, name, args...).Run() == nil
	}
	if run(docker, "info") {
		return false
	}
	// -n: fail immediately rather than prompt for a password.
	return run("sudo", "-n", docker, "info")
}

// detectLANIPs returns every non-loopback RFC1918 IPv4 address on the host's
// interfaces. Overridable for tests.
var detectLANIPs = func() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var ips []net.IP
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP == nil || ipnet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil && isRFC1918(ip4) {
			ips = append(ips, ip4)
		}
	}
	return ips
}

// lanIP returns the first RFC1918 address of the host, or "localhost".
func lanIP() string {
	if ips := detectLANIPs(); len(ips) > 0 {
		return ips[0].String()
	}
	return "localhost"
}

// isRFC1918 reports whether ip is in 10/8, 172.16/12 or 192.168/16.
func isRFC1918(ip net.IP) bool {
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
