// Package httputil holds HTTP helpers shared across the middleware package and
// its subpackages. Keeping these in a separate package breaks import cycles
// between middleware and middleware/peligrosa.
package httputil

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// WriteJSON writes v as a JSON response with the standard content type.
func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes a JSON error response with the given HTTP status code.
func WriteError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// TrustedUpstreamCIDR is the CIDR range r.RemoteAddr must fall in for
// X-Real-IP to be honored. Nginx runs inside the docker network and
// proxies to middleware; any other socket peer is untrusted. Exposed and
// mutable so peligrosa's loopback auto-session can share the same trust
// check and so tests can override it.
//
// Default covers the RFC1918 /12 containing the default docker bridge
// (172.16.0.0/12). Override at runtime via PELICULA_UPSTREAM_CIDR.
var TrustedUpstreamCIDR = func() string {
	if v := os.Getenv("PELICULA_UPSTREAM_CIDR"); v != "" {
		return v
	}
	return "172.16.0.0/12"
}()

// RemoteAddrTrusted reports whether a remoteAddr (as seen on an http.Request)
// falls within TrustedUpstreamCIDR. Used by ClientIP and by peligrosa's
// loopback auto-session to decide whether X-Real-IP was set by a trusted
// reverse proxy.
func RemoteAddrTrusted(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	// Strip IPv6 brackets if present (net.SplitHostPort already did this,
	// but raw remoteAddrs without a port may still have them).
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	_, cidr, err := net.ParseCIDR(TrustedUpstreamCIDR)
	if err != nil {
		return false
	}
	return cidr.Contains(ip)
}

// ClientIP returns the best-effort client IP for the request. X-Real-IP is
// trusted only when the socket peer is within TrustedUpstreamCIDR (i.e.,
// a known reverse proxy — nginx in this stack). Otherwise the socket peer
// address is returned with port stripped. This fixes the MEDIUM-3 issue
// where untrusted clients could spoof their apparent IP via X-Real-IP to
// defeat per-IP rate limiting.
//
// IPv6 note: strings.LastIndex on ":" is sufficient for stripping the
// port from "[::1]:12345" style RemoteAddr forms — net.http uses that
// bracket-wrapped encoding for IPv6 peers.
func ClientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" && RemoteAddrTrusted(r.RemoteAddr) {
		return ip
	}
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i != -1 {
		return addr[:i]
	}
	return addr
}

// IsStateMutating reports whether the HTTP method changes server state.
// CSRF guards only apply to mutating methods; safe methods pass through.
func IsStateMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// IsLocalOrigin returns true if the request Origin is a localhost or
// private-network address. Parses the origin as a URL and checks the hostname
// to prevent substring-match bypasses. Returns false for empty Origin so that
// unauthenticated curl requests (no Origin header) cannot bypass strict checks.
//
// Peligrosa: Use the middleware wrappers (RequireLocalOriginStrict /
// RequireLocalOriginSoft) rather than calling this directly from handlers.
func IsLocalOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, cidr := range []string{
		"192.168.0.0/16",
		"10.0.0.0/8",
		"172.16.0.0/12",
	} {
		_, network, _ := net.ParseCIDR(cidr)
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// RequireLocalOriginStrict is a Peligrosa CSRF middleware. For state-mutating
// requests it rejects Origins that are missing or not a LAN/localhost address.
// Safe methods (GET/HEAD/OPTIONS) pass through.
// Use for admin-only endpoints where only a LAN browser should send POSTs.
func RequireLocalOriginStrict(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsStateMutating(r.Method) && !IsLocalOrigin(r.Header.Get("Origin")) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireLocalOriginSoft is a Peligrosa CSRF middleware. For state-mutating
// requests it allows empty Origin (API/curl callers) but rejects non-empty
// Origins that are not LAN/localhost (browser cross-origin).
// Safe methods pass through.
// Use for endpoints where programmatic callers without an Origin are valid.
func RequireLocalOriginSoft(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsStateMutating(r.Method) {
			if origin := r.Header.Get("Origin"); origin != "" && !IsLocalOrigin(origin) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
