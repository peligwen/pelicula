package sse

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const pollInterval = 5 * time.Second

// ServiceQuerier is the subset of ServiceClients that SSEPoller needs.
type ServiceQuerier interface {
	CheckHealth() map[string]string
	IsWired() bool
	QbtGet(path string) ([]byte, error)
}

// DockerLogsFunc is a function that fetches container logs.
// Matches the signature of dockerLogsFunc in the main package.
type DockerLogsFunc func(name string, tail int, timestamps bool) ([]byte, error)

// LogEntry is one log line tagged with its source service.
type LogEntry struct {
	Service   string    `json:"service"`
	Line      string    `json:"line"`
	Timestamp time.Time `json:"ts,omitempty"`
}

// AllowedContainers is the set of Compose service names the log fetcher may query.
var AllowedContainers = map[string]bool{
	"nginx":        true,
	"pelicula-api": true,
	"procula":      true,
	"sonarr":       true,
	"radarr":       true,
	"prowlarr":     true,
	"qbittorrent":  true,
	"jellyfin":     true,
	"bazarr":       true,
	"gluetun":      true,
}

// Poller polls backend data sources every pollInterval and broadcasts SSE
// events on change (detected via SHA-256 hash comparison).
type Poller struct {
	hub        *Hub
	svc        ServiceQuerier
	proculaURL string
	dockerLogs DockerLogsFunc
	hashes     map[string][32]byte
	mu         sync.Mutex
}

// NewPoller creates a Poller that will broadcast to hub using svc.
// proculaURL is the base URL for the Procula service (e.g. "http://procula:8282").
// dockerLogs is the function to fetch container log lines.
func NewPoller(hub *Hub, svc ServiceQuerier, proculaURL string, dockerLogs DockerLogsFunc) *Poller {
	return &Poller{
		hub:        hub,
		svc:        svc,
		proculaURL: proculaURL,
		dockerLogs: dockerLogs,
		hashes:     make(map[string][32]byte),
	}
}

// Run starts the polling loop and blocks until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
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
func (p *Poller) pollOnce(ctx context.Context) {
	type fetchResult struct {
		event         string
		hashData      []byte
		broadcastData []byte
		err           error
	}

	type namedFetch struct {
		event string
		fn    func(context.Context) (hashData []byte, broadcastData []byte, err error)
	}

	fetches := []namedFetch{
		{"services", wrapFetch(p.fetchServices)},
		{"downloads", wrapFetch(p.fetchDownloads)},
		{"storage", wrapFetch(p.fetchStorage)},
		{"notifications", wrapFetch(p.fetchNotifications)},
		{"logs", wrapFetch(p.fetchLogs)},
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
			p.hub.Broadcast(Message{
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
func (p *Poller) TriggerImmediate(ctx context.Context, eventType string) {
	var fn func(context.Context) ([]byte, []byte, error)
	switch eventType {
	case "services":
		fn = wrapFetch(p.fetchServices)
	case "downloads":
		fn = wrapFetch(p.fetchDownloads)
	case "storage":
		fn = wrapFetch(p.fetchStorage)
	case "notifications":
		fn = wrapFetch(p.fetchNotifications)
	case "logs":
		fn = wrapFetch(p.fetchLogs)
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

	p.hub.Broadcast(Message{
		Event: eventType,
		Data:  broadcastData,
	})
}

// fetchServices builds a lightweight service-status map and marshals it to JSON.
func (p *Poller) fetchServices(ctx context.Context) ([]byte, error) {
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
func (p *Poller) fetchDownloads(ctx context.Context) ([]byte, error) {
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
func (p *Poller) fetchStorage(ctx context.Context) ([]byte, error) {
	return proculaGet(p.proculaURL + "/api/procula/storage")
}

// fetchNotifications proxies Procula's notification feed.
func (p *Poller) fetchNotifications(ctx context.Context) ([]byte, error) {
	return proculaGet(p.proculaURL + "/api/procula/notifications")
}

// fetchLogs fans out over all allowed containers, parses timestamps, sorts
// newest-first, and returns the top 200 as JSON.
func (p *Poller) fetchLogs(ctx context.Context) ([]byte, error) {
	const perSvcTail = 50

	type result struct {
		svc string
		raw []byte
		err error
	}
	ch := make(chan result, len(AllowedContainers))
	var wg sync.WaitGroup
	for name := range AllowedContainers {
		wg.Add(1)
		go func(svc string) {
			defer wg.Done()
			raw, err := p.dockerLogs(svc, perSvcTail, true)
			ch <- result{svc: svc, raw: raw, err: err}
		}(name)
	}
	wg.Wait()
	close(ch)

	var entries []LogEntry
	for r := range ch {
		if r.err != nil {
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(r.raw))
		sc.Buffer(make([]byte, 256*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r\n")
			if line == "" {
				continue
			}
			ts, content := parseLogTimestamp(line)
			entries = append(entries, LogEntry{Service: r.svc, Line: content, Timestamp: ts})
		}
	}

	sorted := sortedLogEntries(entries, 200)
	if sorted == nil {
		sorted = []LogEntry{}
	}
	return json.Marshal(map[string]any{"entries": sorted})
}

// parseLogTimestamp peels the RFC3339Nano prefix Docker adds when timestamps=1.
func parseLogTimestamp(line string) (time.Time, string) {
	idx := strings.IndexByte(line, ' ')
	if idx <= 0 {
		return time.Time{}, line
	}
	t, err := time.Parse(time.RFC3339Nano, line[:idx])
	if err != nil {
		return time.Time{}, line
	}
	return t, line[idx+1:]
}

// sortedLogEntries returns a copy of entries sorted newest-first, capped at max.
func sortedLogEntries(entries []LogEntry, max int) []LogEntry {
	out := make([]LogEntry, len(entries))
	copy(out, entries)
	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamp.After(out[j].Timestamp)
	})
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// proculaGet makes a GET request and returns the body.
func proculaGet(url string) ([]byte, error) {
	resp, err := http.Get(url) //nolint:noctx
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
