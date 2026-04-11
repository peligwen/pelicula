package httputil

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteJSON(rr, map[string]string{"k": "v"})
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	if !strings.Contains(rr.Body.String(), `"k":"v"`) {
		t.Fatalf("body = %q, missing k:v", rr.Body.String())
	}
}

func TestWriteError(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteError(rr, "nope", http.StatusBadRequest)
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"error":"nope"`) {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name   string
		xreal  string
		remote string
		want   string
	}{
		{"x-real-ip wins", "10.0.0.5", "172.17.0.1:1234", "10.0.0.5"},
		{"fallback to remote host", "", "192.168.1.2:5678", "192.168.1.2"},
		// no colon: no port stripping, return as-is
		{"malformed remote fallback", "", "no-port", "no-port"},
		// IPv6 with port: strip last colon segment
		{"ipv6 with port", "", "[::1]:1234", "[::1]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remote
			if c.xreal != "" {
				r.Header.Set("X-Real-IP", c.xreal)
			}
			if got := ClientIP(r); got != c.want {
				t.Fatalf("ClientIP = %q, want %q", got, c.want)
			}
		})
	}
}

func TestClientIP_TrustedUpstreamHonorsXRealIP(t *testing.T) {
	old := TrustedUpstreamCIDR
	TrustedUpstreamCIDR = "172.17.0.0/16"
	defer func() { TrustedUpstreamCIDR = old }()

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.17.0.5:12345"
	r.Header.Set("X-Real-IP", "203.0.113.10")
	if got := ClientIP(r); got != "203.0.113.10" {
		t.Fatalf("ClientIP = %q, want 203.0.113.10", got)
	}
}

func TestClientIP_UntrustedUpstreamIgnoresXRealIP(t *testing.T) {
	old := TrustedUpstreamCIDR
	TrustedUpstreamCIDR = "172.17.0.0/16"
	defer func() { TrustedUpstreamCIDR = old }()

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.1.7:33000" // LAN client, not nginx
	r.Header.Set("X-Real-IP", "127.0.0.1")
	if got := ClientIP(r); got != "192.168.1.7" {
		t.Fatalf("ClientIP = %q, want 192.168.1.7 (header must be ignored)", got)
	}
}

func TestRemoteAddrTrusted(t *testing.T) {
	old := TrustedUpstreamCIDR
	TrustedUpstreamCIDR = "172.17.0.0/16"
	defer func() { TrustedUpstreamCIDR = old }()

	cases := []struct {
		addr string
		want bool
	}{
		{"172.17.0.1:12345", true},
		{"172.17.0.255:1", true},
		{"172.18.0.1:12345", false},
		{"192.168.1.1:80", false},
		{"127.0.0.1:8080", false},
		{"garbage", false},
	}
	for _, c := range cases {
		if got := RemoteAddrTrusted(c.addr); got != c.want {
			t.Errorf("RemoteAddrTrusted(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestClientIP_IPv6PeerFallback(t *testing.T) {
	// ::1 is not in 172.16.0.0/12, so X-Real-IP must be ignored and the
	// bracketed IPv6 form must be stripped correctly.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[::1]:12345"
	r.Header.Set("X-Real-IP", "203.0.113.10")
	if got := ClientIP(r); got != "[::1]" {
		t.Fatalf("ClientIP = %q, want [::1]", got)
	}
}

func TestIsLocalOrigin(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		// Empty origin must return false (prevents unauthenticated curl CSRF)
		{"", false},
		// Localhost variants
		{"http://localhost:7354", true},
		{"http://localhost", true},
		{"http://127.0.0.1:7354", true},
		{"http://127.0.0.1", true},
		{"http://[::1]:7354", true},
		{"http://[::1]", true},
		// RFC1918 ranges
		{"http://192.168.1.100:7354", true},
		{"http://10.0.0.1:7354", true},
		{"http://172.20.0.5:7354", true},
		{"http://10.0.0.3:8080", true},
		{"http://172.16.2.1", true},
		// External addresses
		{"https://evil.example.com", false},
		{"https://evil.example", false},
		{"http://8.8.8.8:7354", false},
		{"http://example.com", false},
		// Link-local: NOT treated as local (matches existing behavior)
		{"http://169.254.169.254", false},
		// Malformed
		{"not-a-url", false},
		{"not a url", false},
	}
	for _, c := range cases {
		t.Run(c.origin, func(t *testing.T) {
			if got := IsLocalOrigin(c.origin); got != c.want {
				t.Errorf("IsLocalOrigin(%q) = %v, want %v", c.origin, got, c.want)
			}
		})
	}
}

func TestIsStateMutating(t *testing.T) {
	mutating := []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	safe := []string{http.MethodGet, http.MethodHead, http.MethodOptions}
	for _, m := range mutating {
		if !IsStateMutating(m) {
			t.Errorf("IsStateMutating(%q) = false, want true", m)
		}
	}
	for _, m := range safe {
		if IsStateMutating(m) {
			t.Errorf("IsStateMutating(%q) = true, want false", m)
		}
	}
}

func TestRequireLocalOriginStrict_RejectsMissingOrigin(t *testing.T) {
	called := false
	h := RequireLocalOriginStrict(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/", nil))
	if called {
		t.Fatal("handler should not have run")
	}
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestRequireLocalOriginStrict_AllowsGET(t *testing.T) {
	called := false
	h := RequireLocalOriginStrict(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if !called {
		t.Fatal("safe method should pass through")
	}
}

func TestRequireLocalOriginStrict_AllowsLocalOrigin(t *testing.T) {
	for _, origin := range []string{"http://localhost:7354", "http://192.168.1.50:7354", "http://10.0.0.1"} {
		called := false
		h := RequireLocalOriginStrict(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			called = true
		}))
		req := httptest.NewRequest("POST", "/", nil)
		req.Header.Set("Origin", origin)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if !called {
			t.Errorf("POST with local origin %q: handler should have run", origin)
		}
		if rr.Code != http.StatusOK {
			t.Errorf("POST with local origin %q: want 200, got %d", origin, rr.Code)
		}
	}
}

func TestRequireLocalOriginStrict_RejectsForeignOrigin(t *testing.T) {
	called := false
	h := RequireLocalOriginStrict(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if called {
		t.Fatal("foreign origin should be rejected")
	}
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestRequireLocalOriginSoft_AllowsEmptyOrigin(t *testing.T) {
	called := false
	h := RequireLocalOriginSoft(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil))
	if !called {
		t.Fatal("empty origin should pass through in soft mode")
	}
}

func TestRequireLocalOriginSoft_RejectsForeignOrigin(t *testing.T) {
	called := false
	h := RequireLocalOriginSoft(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if called {
		t.Fatal("foreign origin should be rejected")
	}
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestRequireLocalOriginSoft_AllowsLocalOriginOnPost(t *testing.T) {
	called := false
	h := RequireLocalOriginSoft(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Origin", "http://192.168.1.50:7354")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !called {
		t.Fatal("local origin should pass through in soft mode")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}

func TestRequireLocalOriginSoft_RejectsForeignOriginOnDelete(t *testing.T) {
	called := false
	h := RequireLocalOriginSoft(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("DELETE", "/", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if called {
		t.Fatal("foreign origin DELETE should be rejected")
	}
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}
