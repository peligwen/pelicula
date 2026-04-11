package peligrosa

import (
	"net"
	"net/http"
	"strings"

	"pelicula-api/httputil"
)

// loopbackAutoSession reports whether the request should be granted a
// transient admin session without a cookie. It is the host-machine
// convenience path that replaces PELICULA_AUTH=off: requests that
// originate on the Docker host get admin access automatically, LAN and
// remote clients do not.
//
// Returns true only when ALL three gates pass:
//  1. r.RemoteAddr falls within httputil.TrustedUpstreamCIDR — the
//     request must have come through nginx. LAN clients hit the
//     middleware directly at a LAN IP and fail here.
//  2. X-Real-IP is a loopback address — nginx sets X-Real-IP from
//     $remote_addr on every request, overwriting any client-supplied
//     header. Only a client that connected to nginx on 127.0.0.1 / ::1
//     has a loopback X-Real-IP.
//  3. Host header is localhost / 127.0.0.1 / ::1 — defense in depth.
//     LAN clients hitting http://<lan-ip>:7354/ would have <lan-ip>
//     in Host, not loopback.
//
// Task 7 only introduces this helper and its tests. Wiring it into
// (*Auth).SessionFor / HandleCheck happens in Task 8 when off-mode is
// deleted.
func loopbackAutoSession(r *http.Request) bool {
	if !httputil.RemoteAddrTrusted(r.RemoteAddr) {
		return false
	}
	realIP := r.Header.Get("X-Real-IP")
	ip := net.ParseIP(realIP)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}
