// Package httpx provides handler-side middleware that complements the httputil package.
// The single middleware exported here is RecoverMiddleware, used to wrap the top-level
// mux in cmd/pelicula-api.
package httpx

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// RecoverMiddleware catches panics in handlers and returns a JSON 500 error.
// If the handler already started writing the response (headers sent), stdlib's
// WriteHeader call becomes a no-op; the client sees a truncated response rather
// than a clean JSON body, but we still log the panic and stack trace.
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				stack := debug.Stack()
				slog.Error("handler panic",
					"component", "httpx",
					"panic", fmt.Sprintf("%v", rec),
					slog.String("stack", string(stack)),
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":"internal server error"}`)) //nolint:errcheck
			}
		}()
		next.ServeHTTP(w, r)
	})
}
