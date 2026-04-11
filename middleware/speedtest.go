package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

// Speed test downloads ~10 MB through gluetun's HTTP proxy and reports Mbps.
// Cloudflare's speed test endpoint is used as the download source because it
// is reliable, globally distributed, and responds quickly.
const speedTestURL = "https://speed.cloudflare.com/__down?bytes=10000000"

// speedTestCooldown prevents rapid re-testing that would inflate VPN bandwidth use.
const speedTestCooldown = 60 * time.Second

var (
	speedTestMu         sync.Mutex
	speedTestLastRun    time.Time
	speedTestLastResult *speedTestResult
)

type speedTestResult struct {
	DownloadMbps  float64 `json:"download_mbps"`
	DurationMs    int64   `json:"duration_ms"`
	BytesReceived int64   `json:"bytes_received"`
	Timestamp     int64   `json:"timestamp"`
	Error         string  `json:"error,omitempty"`
}

func handleSpeedTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	speedTestMu.Lock()
	defer speedTestMu.Unlock()

	if time.Since(speedTestLastRun) < speedTestCooldown && speedTestLastResult != nil {
		writeJSON(w, speedTestLastResult)
		return
	}

	proxyURL := os.Getenv("GLUETUN_PROXY_URL")
	if proxyURL == "" {
		proxyURL = "http://gluetun:8888"
	}

	parsed, err := url.Parse(proxyURL)
	if err != nil {
		writeError(w, "invalid proxy URL: "+err.Error(), http.StatusInternalServerError)
		return
	}

	transport := &http.Transport{
		Proxy: http.ProxyURL(parsed),
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	start := time.Now()
	resp, err := client.Get(speedTestURL)
	if err != nil {
		slog.Warn("speed test failed", "component", "speedtest", "error", err)
		result := &speedTestResult{
			Error:     err.Error(),
			Timestamp: time.Now().Unix(),
		}
		speedTestLastResult = result
		speedTestLastRun = time.Now()
		writeJSON(w, result)
		return
	}
	defer resp.Body.Close()

	n, err := io.Copy(io.Discard, resp.Body)
	elapsed := time.Since(start)
	if err != nil {
		slog.Warn("speed test read failed", "component", "speedtest", "error", err)
		result := &speedTestResult{
			Error:     err.Error(),
			Timestamp: time.Now().Unix(),
		}
		speedTestLastResult = result
		speedTestLastRun = time.Now()
		writeJSON(w, result)
		return
	}

	elapsedMs := elapsed.Milliseconds()
	mbps := 0.0
	if elapsedMs > 0 {
		mbps = float64(n) * 8 / float64(elapsedMs) / 1000 // bits / ms / 1000 = Mbps
	}

	result := &speedTestResult{
		DownloadMbps:  mbps,
		DurationMs:    elapsedMs,
		BytesReceived: n,
		Timestamp:     time.Now().Unix(),
	}
	speedTestLastResult = result
	speedTestLastRun = time.Now()

	slog.Info("speed test complete", "component", "speedtest",
		"mbps", mbps, "bytes", n, "elapsed_ms", elapsedMs)
	writeJSON(w, result)
}
