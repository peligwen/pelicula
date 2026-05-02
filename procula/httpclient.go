// httpclient.go — shared HTTP client factory for the procula module.
package procula

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
// maxAttempts times on network errors or 408/429/5xx responses. 4xx responses
// other than 408 and 429 are permanent failures and return immediately without
// retrying — retrying a 401 or 403 caused by a misconfigured key wastes the
// full retry ladder and delays surfacing the real problem.
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
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			if resp.StatusCode < 400 {
				return nil
			}
			// 4xx other than 408 (Request Timeout) and 429 (Too Many Requests)
			// are permanent — retrying will not change the outcome.
			if resp.StatusCode >= 400 && resp.StatusCode < 500 &&
				resp.StatusCode != http.StatusRequestTimeout &&
				resp.StatusCode != http.StatusTooManyRequests {
				return fmt.Errorf("retryHTTPPost: permanent failure: HTTP %d", resp.StatusCode)
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
