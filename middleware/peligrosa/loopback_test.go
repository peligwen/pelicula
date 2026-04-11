package peligrosa

import (
	"net/http/httptest"
	"testing"

	"pelicula-api/httputil"
)

func TestLoopbackAutoSession(t *testing.T) {
	// Pin the trusted upstream CIDR so tests don't depend on the default.
	old := httputil.TrustedUpstreamCIDR
	httputil.TrustedUpstreamCIDR = "172.17.0.0/16"
	defer func() { httputil.TrustedUpstreamCIDR = old }()

	cases := []struct {
		name       string
		remoteAddr string // r.RemoteAddr — simulates docker-bridge upstream vs LAN
		xRealIP    string
		host       string
		want       bool
	}{
		{"host-machine curl through nginx", "172.17.0.1:55123", "127.0.0.1", "localhost", true},
		{"host-machine with port in host", "172.17.0.1:55123", "127.0.0.1", "localhost:7354", true},
		{"host-machine IPv6 loopback", "172.17.0.1:55123", "::1", "[::1]", true},
		{"host-machine 127.0.0.1 host", "172.17.0.1:55123", "127.0.0.1", "127.0.0.1", true},
		{"LAN client through nginx", "172.17.0.1:55123", "192.168.1.42", "localhost", false},
		{"loopback xreal but LAN host", "172.17.0.1:55123", "127.0.0.1", "192.168.1.5:7354", false},
		{"spoofed xreal from LAN upstream", "192.168.1.7:33000", "127.0.0.1", "localhost", false},
		{"empty host rejected", "172.17.0.1:55123", "127.0.0.1", "", false},
		{"empty xreal rejected", "172.17.0.1:55123", "", "localhost", false},
		{"malformed xreal rejected", "172.17.0.1:55123", "not-an-ip", "localhost", false},
		{"non-loopback xreal rejected", "172.17.0.1:55123", "8.8.8.8", "localhost", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/pelicula/check", nil)
			r.RemoteAddr = c.remoteAddr
			if c.xRealIP != "" {
				r.Header.Set("X-Real-IP", c.xRealIP)
			}
			r.Host = c.host
			got := loopbackAutoSession(r)
			if got != c.want {
				t.Errorf("loopbackAutoSession=%v, want %v", got, c.want)
			}
		})
	}
}
