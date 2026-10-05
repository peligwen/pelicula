package main

import (
	"net"
	"testing"
)

func TestPlatformDefaults(t *testing.T) {
	cases := []struct {
		name                  string
		synology              bool
		config, library, work string
	}{
		{"linux", false, "/repo/config", "/home/u/media", "/home/u/media"},
		{"synology", true, "/volume1/docker/pelicula/config", "/volume1/media", "/volume1/media"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, l, w := platformDefaults(tc.synology, "/repo", "/home/u")
			if c != tc.config || l != tc.library || w != tc.work {
				t.Errorf("platformDefaults = (%q, %q, %q), want (%q, %q, %q)", c, l, w, tc.config, tc.library, tc.work)
			}
		})
	}
}

func TestPlatformLabel(t *testing.T) {
	t.Setenv("WSL_DISTRO_NAME", "")
	cases := []struct {
		p    Platform
		want string
	}{
		{Platform{OS: "linux", IsSynology: true}, "Synology NAS"},
		{Platform{OS: "darwin"}, "macOS"},
		{Platform{OS: "windows"}, "Windows"},
		{Platform{OS: "linux", IsWSL: true}, "WSL"},
		{Platform{OS: "linux"}, "Linux"},
	}
	for _, tc := range cases {
		if got := tc.p.PlatformLabel(); got != tc.want {
			t.Errorf("%+v label = %q, want %q", tc.p, got, tc.want)
		}
	}

	t.Setenv("WSL_DISTRO_NAME", "Ubuntu")
	if got := (Platform{OS: "linux", IsWSL: true}).PlatformLabel(); got != "WSL (Ubuntu)" {
		t.Errorf("WSL label = %q, want %q", got, "WSL (Ubuntu)")
	}
}

func TestDockerBinary(t *testing.T) {
	has := func(string) bool { return true }
	none := func(string) bool { return false }

	if got := dockerBinary(true, has); got != synologyDockerPath {
		t.Errorf("synology with docker installed: %q, want %q", got, synologyDockerPath)
	}
	if got := dockerBinary(true, none); got != "docker" {
		t.Errorf("synology without %s: %q, want docker", synologyDockerPath, got)
	}
	if got := dockerBinary(false, has); got != "docker" {
		t.Errorf("non-synology: %q, want docker", got)
	}
}

func TestIsRFC1918(t *testing.T) {
	cases := map[string]bool{
		"10.0.0.5": true, "172.16.0.1": true, "172.31.255.255": true, "192.168.1.20": true,
		"172.15.0.1": false, "172.32.0.1": false, "8.8.8.8": false, "127.0.0.1": false,
		"192.169.0.1": false, "::1": false,
	}
	for s, want := range cases {
		if got := isRFC1918(net.ParseIP(s)); got != want {
			t.Errorf("isRFC1918(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestLanIP(t *testing.T) {
	orig := detectLANIPs
	defer func() { detectLANIPs = orig }()

	detectLANIPs = func() []net.IP { return nil }
	if got := lanIP(); got != "localhost" {
		t.Errorf("lanIP with no interfaces = %q, want localhost", got)
	}
	detectLANIPs = func() []net.IP { return []net.IP{net.ParseIP("192.168.1.9"), net.ParseIP("10.0.0.2")} }
	if got := lanIP(); got != "192.168.1.9" {
		t.Errorf("lanIP = %q, want the first address", got)
	}
}
