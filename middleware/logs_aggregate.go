// logs_aggregate.go — fan-out over docker-proxy container logs, returning a
// unified entry list the dashboard Logs tab can colour by service.
package main

import (
	"bufio"
	"bytes"
	"net/http"
	"pelicula-api/httputil"
	"strconv"
	"strings"
	"sync"
)

// LogEntry is one line tagged with its source service.
type LogEntry struct {
	Service string `json:"service"`
	Line    string `json:"line"`
}

const (
	logAggDefaultTail = 100
	logAggMaxTail     = 500
)

// handleLogsAggregate fetches logs from each requested service in parallel
// and returns {entries: [...], services: [...]}.
// Query params: ?tail=N (default 100, max 500), ?services=a,b,c (default: all allowed).
func handleLogsAggregate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tail := logAggDefaultTail
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > logAggMaxTail {
				n = logAggMaxTail
			}
			tail = n
		}
	}

	var services []string
	if sv := r.URL.Query().Get("services"); sv != "" {
		for _, s := range strings.Split(sv, ",") {
			s = strings.TrimSpace(s)
			if isAllowedContainer(s) {
				services = append(services, s)
			}
		}
	} else {
		for name := range allowedContainers {
			services = append(services, name)
		}
	}

	type fetchResult struct {
		svc string
		raw []byte
		err error
	}
	resCh := make(chan fetchResult, len(services))
	var wg sync.WaitGroup
	for _, svc := range services {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			raw, err := dockerLogsFunc(name, tail)
			resCh <- fetchResult{svc: name, raw: raw, err: err}
		}(svc)
	}
	wg.Wait()
	close(resCh)

	var entries []LogEntry
	for res := range resCh {
		if res.err != nil {
			entries = append(entries, LogEntry{Service: res.svc, Line: "(logs unavailable: " + res.err.Error() + ")"})
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(res.raw))
		sc.Buffer(make([]byte, 256*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r\n")
			if line == "" {
				continue
			}
			entries = append(entries, LogEntry{Service: res.svc, Line: line})
		}
	}

	if entries == nil {
		entries = []LogEntry{}
	}

	httputil.WriteJSON(w, map[string]any{
		"entries":  entries,
		"services": services,
	})
}
