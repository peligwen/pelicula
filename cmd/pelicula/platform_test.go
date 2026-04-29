package main

import (
	"net"
	"strings"
	"testing"
)

func TestDetectLANURL_FormatAndRange(t *testing.T) {
	t.Setenv("PELICULA_PORT", "")
	got := detectLANURL()
	if got == "" {
		// Acceptable on hosts with no RFC1918 address (CI sandboxes, etc.).
		// We don't fail the test — but we assert the empty-string contract
		// is honored rather than some partial/malformed string.
		return
	}

	// Must be http://<ip>:7354/jellyfin
	const prefix = "http://"
	const suffix = ":7354/jellyfin"
	if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, suffix) {
		t.Fatalf("detectLANURL() = %q, want http://<ip>%s", got, suffix)
	}

	ipStr := strings.TrimSuffix(strings.TrimPrefix(got, prefix), suffix)
	ip := net.ParseIP(ipStr)
	if ip == nil || ip.To4() == nil {
		t.Fatalf("detectLANURL() ip = %q, not a valid IPv4", ipStr)
	}

	if !isRFC1918(ip) {
		t.Fatalf("detectLANURL() ip %s not in an RFC1918 range", ipStr)
	}
	if ip.IsLoopback() {
		t.Fatalf("detectLANURL() returned loopback address %s", ipStr)
	}
}

// TestDetectLANURL_HonorsPort confirms a host-side PELICULA_PORT override
// shows up in the suggested URL — without this, hosts that swap the default
// port get a wrong URL baked into JELLYFIN_PUBLISHED_URL via the wizard.
func TestDetectLANURL_HonorsPort(t *testing.T) {
	t.Setenv("PELICULA_PORT", "9090")
	got := detectLANURL()
	if got == "" {
		return // host has no RFC1918 address — covered by sibling test
	}
	if !strings.HasSuffix(got, ":9090/jellyfin") {
		t.Errorf("detectLANURL() = %q, want suffix :9090/jellyfin", got)
	}
}

func TestIsRFC1918(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"172.15.0.1", false},
		{"172.32.0.1", false},
		{"192.168.1.1", true},
		{"192.168.255.255", true},
		{"8.8.8.8", false},
		{"127.0.0.1", false},
		{"169.254.1.1", false},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if got := isRFC1918(ip); got != c.want {
			t.Errorf("isRFC1918(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}
