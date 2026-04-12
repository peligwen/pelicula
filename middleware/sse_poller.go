package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

const pollInterval = 5 * time.Second

// SSEPoller polls backend data sources every pollInterval and broadcasts SSE
// events on change (detected via SHA-256 hash comparison).
type SSEPoller struct {
	hub       *SSEHub
	svc       *ServiceClients
	dismissed *DismissedStore
	hashes    map[string][32]byte
	mu        sync.Mutex
}

// NewSSEPoller creates an SSEPoller that will broadcast to hub using svc.
func NewSSEPoller(hub *SSEHub, svc *ServiceClients, dismissed *DismissedStore) *SSEPoller {
	return &SSEPoller{
		hub:       hub,
		svc:       svc,
		dismissed: dismissed,
		hashes:    make(map[string][32]byte),
	}
}

// Run starts the polling loop and blocks until ctx is cancelled.
func (p *SSEPoller) Run(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollOnce(ctx)
		}
	}
}

// pollOnce fetches all 5 event types in parallel, hashes each response, and
// broadcasts any that have changed since the last poll.
func (p *SSEPoller) pollOnce(ctx context.Context) {
	type fetchResult struct {
		event         string
		hashData      []byte // stable bytes used for change detection
		broadcastData []byte // bytes sent to clients (may include timestamps)
		err           error
	}

	type namedFetch struct {
		event string
		fn    func(context.Context) (hashData []byte, broadcastData []byte, err error)
	}

	fetches := []namedFetch{
		{"pipeline", p.fetchPipeline},
		{"services", wrapFetch(p.fetchServices)},
		{"downloads", wrapFetch(p.fetchDownloads)},
		{"storage", wrapFetch(p.fetchStorage)},
		{"notifications", wrapFetch(p.fetchNotifications)},
	}

	results := make(chan fetchResult, len(fetches))
	var wg sync.WaitGroup

	for _, nf := range fetches {
		wg.Add(1)
		go func(nf namedFetch) {
			defer wg.Done()
			hashData, broadcastData, err := nf.fn(ctx)
			results <- fetchResult{event: nf.event, hashData: hashData, broadcastData: broadcastData, err: err}
		}(nf)
	}

	wg.Wait()
	close(results)

	for res := range results {
		if res.err != nil {
			slog.Debug("sse poller fetch failed", "component", "sse_poller", "event", res.event, "error", res.err)
			continue
		}
		hash := sha256.Sum256(res.hashData)
		p.mu.Lock()
		prev, seen := p.hashes[res.event]
		changed := !seen || prev != hash
		if changed {
			p.hashes[res.event] = hash
		}
		p.mu.Unlock()

		if changed {
			p.hub.Broadcast(SSEMessage{
				Event: res.event,
				Data:  res.broadcastData,
			})
		}
	}
}

// wrapFetch adapts a simple (ctx) ([]byte, error) fetch function into the
// two-return-value form expected by pollOnce (hashData == broadcastData).
func wrapFetch(fn func(context.Context) ([]byte, error)) func(context.Context) ([]byte, []byte, error) {
	return func(ctx context.Context) ([]byte, []byte, error) {
		data, err := fn(ctx)
		return data, data, err
	}
}

// TriggerImmediate fetches eventType and broadcasts unconditionally (no hash
// check). Intended for hooks that know data has changed and want instant
// feedback to connected clients.
func (p *SSEPoller) TriggerImmediate(ctx context.Context, eventType string) {
	var fn func(context.Context) ([]byte, []byte, error)
	switch eventType {
	case "pipeline":
		fn = p.fetchPipeline
	case "services":
		fn = wrapFetch(p.fetchServices)
	case "downloads":
		fn = wrapFetch(p.fetchDownloads)
	case "storage":
		fn = wrapFetch(p.fetchStorage)
	case "notifications":
		fn = wrapFetch(p.fetchNotifications)
	default:
		slog.Warn("sse poller: unknown event type for TriggerImmediate", "component", "sse_poller", "event", eventType)
		return
	}

	hashData, broadcastData, err := fn(ctx)
	if err != nil {
		slog.Warn("sse poller TriggerImmediate fetch failed", "component", "sse_poller", "event", eventType, "error", err)
		return
	}

	// Update stored hash so the next regular poll doesn't re-broadcast.
	hash := sha256.Sum256(hashData)
	p.mu.Lock()
	p.hashes[eventType] = hash
	p.mu.Unlock()

	p.hub.Broadcast(SSEMessage{
		Event: eventType,
		Data:  broadcastData,
	})
}

// fetchPipeline builds the pipeline response and returns two byte slices:
// hashBytes has a zeroed GeneratedAt so the hash is stable across polls,
// broadcastBytes carries a real timestamp for clients.
func (p *SSEPoller) fetchPipeline(ctx context.Context) (hashBytes []byte, broadcastBytes []byte, err error) {
	resp, err := BuildPipelineResponse(p.svc, p.dismissed)
	if err != nil {
		return nil, nil, err
	}
	// For hash: zero out the timestamp to avoid spurious change detection.
	hashResp := resp
	hashResp.GeneratedAt = time.Time{}
	hashBytes, err = json.Marshal(hashResp)
	if err != nil {
		return nil, nil, err
	}
	// For broadcast: real timestamp.
	resp.GeneratedAt = time.Now().UTC()
	broadcastBytes, err = json.Marshal(resp)
	return hashBytes, broadcastBytes, err
}

// fetchServices builds a lightweight service-status map and marshals it to JSON.
// Matches the shape of handleStatus minus the Prowlarr indexer count (too
// expensive to fetch every 5 seconds).
func (p *SSEPoller) fetchServices(ctx context.Context) ([]byte, error) {
	svcs := p.svc.CheckHealth()
	status := map[string]any{
		"status":         "ok",
		"services":       svcs,
		"wired":          p.svc.IsWired(),
		"vpn_configured": os.Getenv("WIREGUARD_PRIVATE_KEY") != "",
	}
	return json.Marshal(status)
}

// fetchDownloads fetches raw torrent list and transfer stats from qBittorrent.
// The SSE "downloads" event drives the Downloads tab in the dashboard (downloads.js),
// distinct from the pipeline board which normalizes this data into PipelineItem cards.
func (p *SSEPoller) fetchDownloads(ctx context.Context) ([]byte, error) {
	torrentData, err := p.svc.QbtGet("/api/v2/torrents/info")
	if err != nil {
		return nil, err
	}

	type combined struct {
		Torrents json.RawMessage `json:"torrents"`
		Stats    json.RawMessage `json:"stats,omitempty"`
	}
	out := combined{Torrents: torrentData}

	if statsData, err := p.svc.QbtGet("/api/v2/transfer/info"); err == nil {
		out.Stats = statsData
	}

	return json.Marshal(out)
}

// fetchStorage proxies Procula's storage report.
func (p *SSEPoller) fetchStorage(ctx context.Context) ([]byte, error) {
	return proculaGet(p.svc, proculaURL+"/api/procula/storage")
}

// fetchNotifications proxies Procula's notification feed.
func (p *SSEPoller) fetchNotifications(ctx context.Context) ([]byte, error) {
	return proculaGet(p.svc, proculaURL+"/api/procula/notifications")
}

// proculaGet makes a GET request using svc's HTTP client and returns the body.
func proculaGet(svc *ServiceClients, url string) ([]byte, error) {
	resp, err := svc.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, &httpStatusError{Code: resp.StatusCode, URL: url}
	}
	return io.ReadAll(resp.Body)
}

// httpStatusError is returned by proculaGet when the server returns a 4xx/5xx.
type httpStatusError struct {
	Code int
	URL  string
}

func (e *httpStatusError) Error() string {
	return "HTTP " + http.StatusText(e.Code) + " from " + e.URL
}
