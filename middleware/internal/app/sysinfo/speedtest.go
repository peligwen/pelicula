package sysinfo

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"pelicula-api/httputil"
	"sync"
	"time"
)

// Speed test downloads ~10 MB through gluetun's HTTP proxy and reports Mbps.
// Cloudflare's speed test endpoint is used as the download source because it
// is reliable, globally distributed, and responds quickly.
// var (not const) so tests can substitute an httptest URL.
var speedTestURL = "https://speed.cloudflare.com/__down?bytes=10000000"

// speedTestProxyOverride, when non-nil, is used as the HTTP proxy URL instead
// of the GLUETUN_PROXY_URL env var. Empty string means no proxy. Only set by tests.
var speedTestProxyOverride *string

// speedTestCooldown prevents rapid re-testing that would inflate VPN bandwidth use.
const speedTestCooldown = 60 * time.Second

var (
	speedTestMu         sync.Mutex
	speedTestLastRun    time.Time
	speedTestLastResult *speedTestResult
	speedTestInflight   bool
)

type speedTestResult struct {
	DownloadMbps  float64 `json:"download_mbps"`
	DurationMs    int64   `json:"duration_ms"`
	BytesReceived int64   `json:"bytes_received"`
	Timestamp     int64   `json:"timestamp"`
	Error         string  `json:"error,omitempty"`
}

// runSpeedTest executes the download against targetURL using client.
// It is called with no mutex held; ctx cancellation aborts the download.
func runSpeedTest(ctx context.Context, client *http.Client, targetURL string) (*speedTestResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return &speedTestResult{
			Error:     err.Error(),
			Timestamp: time.Now().Unix(),
		}, err
	}
	defer resp.Body.Close()

	n, err := io.Copy(io.Discard, resp.Body)
	elapsed := time.Since(start)
	if err != nil {
		return &speedTestResult{
			Error:     err.Error(),
			Timestamp: time.Now().Unix(),
		}, err
	}

	elapsedMs := elapsed.Milliseconds()
	mbps := 0.0
	if elapsedMs > 0 {
		mbps = float64(n) * 8 / float64(elapsedMs) / 1000 // bits / ms / 1000 = Mbps
	}

	return &speedTestResult{
		DownloadMbps:  mbps,
		DurationMs:    elapsedMs,
		BytesReceived: n,
		Timestamp:     time.Now().Unix(),
	}, nil
}

// buildSpeedTestClient constructs an *http.Client for the speed test.
// If a proxy URL is configured (via override or env), requests route through it.
// Empty proxy URL means direct (no proxy), used in tests.
func buildSpeedTestClient() (*http.Client, error) {
	proxyURL := ""
	if speedTestProxyOverride != nil {
		proxyURL = *speedTestProxyOverride
	} else {
		proxyURL = os.Getenv("GLUETUN_PROXY_URL")
		if proxyURL == "" {
			proxyURL = "http://gluetun:8888"
		}
	}

	transport := &http.Transport{IdleConnTimeout: 30 * time.Second}
	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(parsed)
	}

	return &http.Client{
		Transport: transport,
		Timeout:   35 * time.Second,
	}, nil
}

func handleSpeedTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	speedTestMu.Lock()
	if time.Since(speedTestLastRun) < speedTestCooldown && speedTestLastResult != nil {
		cached := speedTestLastResult
		speedTestMu.Unlock()
		httputil.WriteJSON(w, cached)
		return
	}
	if speedTestInflight {
		speedTestMu.Unlock()
		httputil.WriteError(w, "speedtest in progress", http.StatusTooManyRequests)
		return
	}
	speedTestInflight = true
	speedTestMu.Unlock()

	// Mutex released before I/O — the download can take up to 30 s.
	client, err := buildSpeedTestClient()
	if err != nil {
		speedTestMu.Lock()
		speedTestInflight = false
		speedTestMu.Unlock()
		httputil.WriteError(w, "invalid proxy URL: "+err.Error(), http.StatusInternalServerError)
		return
	}

	result, err := runSpeedTest(r.Context(), client, speedTestURL)

	speedTestMu.Lock()
	speedTestInflight = false
	if err == nil {
		speedTestLastResult = result
		speedTestLastRun = time.Now()
	}
	speedTestMu.Unlock()

	if err != nil {
		slog.Warn("speed test failed", "component", "speedtest", "error", err)
		httputil.WriteJSON(w, result)
		return
	}

	slog.Info("speed test complete", "component", "speedtest",
		"mbps", result.DownloadMbps, "bytes", result.BytesReceived, "elapsed_ms", result.DurationMs)
	httputil.WriteJSON(w, result)
}
