package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxBody = 8 << 10 // matches the nginx limit on the public auth routes

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// readJSON decodes a small JSON body into dst. An empty body leaves dst zero.
// It writes a 400 and returns false on malformed input.
func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// CSRF rejects cross-origin state-changing requests. For POST, PUT, PATCH and
// DELETE, when an Origin header (else a Referer header) is present, its host
// must equal the request host; X-Forwarded-Host takes precedence over
// r.Host when set. Requests carrying neither header (curl, server to server,
// *arr webhooks) pass, as do all safe methods.
func CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if !sameOrigin(r) {
				writeError(w, http.StatusForbidden, "cross-origin request blocked")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" {
		src = r.Header.Get("Referer")
	}
	if src == "" {
		return true
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" { // includes the literal Origin "null"
		return false
	}
	want := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		want, _, _ = strings.Cut(fh, ",")
		want = strings.TrimSpace(want)
	}
	return strings.EqualFold(u.Host, want)
}
