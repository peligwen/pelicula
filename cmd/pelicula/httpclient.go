// httpclient.go — shared HTTP client factory for the pelicula CLI.
package main

import (
	"net/http"
	"time"
)

// uaTransport injects a User-Agent header on every outbound request.
type uaTransport struct {
	base      http.RoundTripper
	userAgent string
}

func (t *uaTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("User-Agent", t.userAgent)
	return t.base.RoundTrip(r)
}

// newHTTPClient returns an *http.Client with the given timeout and a
// User-Agent header of the form:
//
//	PeliculaCLI/<version> (+https://github.com/peligwen/pelicula)
func newHTTPClient(timeout time.Duration) *http.Client {
	ua := "PeliculaCLI/" + version + " (+https://github.com/peligwen/pelicula)"
	return &http.Client{
		Timeout: timeout,
		Transport: &uaTransport{
			base:      http.DefaultTransport,
			userAgent: ua,
		},
	}
}
