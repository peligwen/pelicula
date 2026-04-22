// httpclient.go — shared HTTP client factory for the procula module.
package procula

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
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

// newProculaClient returns an *http.Client with the given timeout and a
// User-Agent header of the form:
//
//	Pelicula/<version> (+https://github.com/peligwen/pelicula)
func newProculaClient(timeout time.Duration) *http.Client {
	ua := "Pelicula/" + Version + " (+https://github.com/peligwen/pelicula)"
	return &http.Client{
		Timeout: timeout,
		Transport: &uaTransport{
			base:      http.DefaultTransport,
			userAgent: ua,
		},
	}
}

// retryHTTPPost POSTs body (as JSON) to url using client, retrying up to
// maxAttempts times on network errors or 4xx/5xx responses. Each retry waits
// attempt*2 seconds, or returns early if ctx is cancelled.
//
// body is re-read from the supplied []byte on every attempt so the caller
// does not need to manage io.Reader state.
func retryHTTPPost(ctx context.Context, client *http.Client, url string, body []byte, maxAttempts int) error {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("retryHTTPPost: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
		} else {
			resp.Body.Close()
			if resp.StatusCode < 400 {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		}

		if attempt == maxAttempts {
			break
		}
		delay := time.Duration(attempt) * 2 * time.Second
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			slog.Debug("retryHTTPPost: context cancelled during retry backoff", "url", url)
			return ctx.Err()
		}
	}
	return fmt.Errorf("retryHTTPPost: failed after %d attempts: %w", maxAttempts, lastErr)
}
