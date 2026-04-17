// Package httpx extends the httputil package with additional handler helpers,
// middleware, and error shapes used across the app/ subpackages.
// It re-exports all symbols from httputil so callers can import just one package.
package httpx

import (
	"context"
	"log/slog"
	"net/http"

	"pelicula-api/httputil"
)

// Re-export httputil symbols so callers of httpx don't need to also import httputil.

// WriteJSON writes v as a JSON response with the standard content type.
var WriteJSON = httputil.WriteJSON

// WriteError writes a JSON error response with the given HTTP status code.
var WriteError = httputil.WriteError

// ClientIP returns the best-effort client IP for the request.
var ClientIP = httputil.ClientIP

// IsLocalOrigin returns true if the request Origin is a localhost or private-network address.
var IsLocalOrigin = httputil.IsLocalOrigin

// RequireLocalOriginStrict is a Peligrosa CSRF middleware for admin-only endpoints.
var RequireLocalOriginStrict = httputil.RequireLocalOriginStrict

// RequireLocalOriginSoft is a Peligrosa CSRF middleware for API-accessible endpoints.
var RequireLocalOriginSoft = httputil.RequireLocalOriginSoft

// requestIDKey is the context key for request IDs.
type requestIDKey struct{}

// RequestIDFromContext retrieves the request ID from the context.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// LoggingMiddleware logs each incoming request with its method, path, and remote address.
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("request",
			"component", "httpx",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", httputil.ClientIP(r),
		)
		next.ServeHTTP(w, r)
	})
}

// RecoverMiddleware catches panics in handlers and returns a 500 error.
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("handler panic", "component", "httpx", "recover", rec)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
