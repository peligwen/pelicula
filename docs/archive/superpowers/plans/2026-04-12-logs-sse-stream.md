# Logs SSE Stream Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the manual-fetch logs view with a live SSE-driven stream that interleaves log lines from all services in chronological order, newest first, updating every 5 seconds.

**Architecture:** A new `fetchLogs` method on `SSEPoller` fetches all container logs with Docker timestamps enabled (`timestamps=1`), parses the RFC3339 prefix from each line, sorts all entries across services newest-first, keeps the top 200, and broadcasts them as a `logs` SSE event. The frontend subscribes via the existing SSE hub; filter chips work client-side against a cached copy of the last push rather than triggering a re-fetch.

**Tech Stack:** Go stdlib (bufio, sort, time), Docker socket proxy API, vanilla JS EventSource

---

### File Map

| File | Change |
|------|--------|
| `middleware/docker.go` | Add `timestamps bool` param to `dockerLogs`; update URL |
| `middleware/logs_aggregate.go` | Add `Timestamp time.Time` to `LogEntry`; add `parseLogTimestamp`, `sortedLogEntries`; update `dockerLogsFunc` caller to pass `false` |
| `middleware/logs_aggregate_test.go` | Update mock function signature to accept `ts bool` |
| `middleware/sse_poller.go` | Add `fetchLogs` method; add `"logs"` to `fetches` list in `pollOnce` |
| `nginx/sse.js` | Add `logs` event listener calling `window.renderLogsFromSSE` |
| `nginx/logs.js` | Add `lastEntries` to state; export `renderLogsFromSSE`; update filter chips to re-render from cache; skip initial fetch when SSE is active |

---

### Task 1: Add `timestamps` parameter to `dockerLogs`

**Files:**
- Modify: `middleware/docker.go:68-72`
- Modify: `middleware/logs_aggregate.go:69` (call site)
- Modify: `middleware/logs_aggregate_test.go:12-14` (mock)

The existing `TestHandleLogsAggregateFansOut` mock will fail to compile when the `dockerLogsFunc` type changes — that compile error is the red step.

- [ ] **Step 1: Update mock in test to new 3-arg signature (makes test fail to compile)**

In `middleware/logs_aggregate_test.go`, change the mock assignment:

```go
// Before (line 12-14):
dockerLogsFunc = func(name string, tail int) ([]byte, error) {

// After:
dockerLogsFunc = func(name string, tail int, ts bool) ([]byte, error) {
```

- [ ] **Step 2: Run to confirm compile failure**

```
cd middleware && go build ./...
```
Expected: `cannot use func literal (type func(string, int, bool) ([]byte, error)) as type func(string, int) ([]byte, error)`

- [ ] **Step 3: Change `dockerLogs` signature in docker.go**

Replace lines 65-73 of `middleware/docker.go`:

```go
// dockerLogs fetches the last tail lines of stdout+stderr for a container.
// Docker multiplexes streams using an 8-byte framing header when the container
// has no TTY (our case); we strip those headers and return raw log bytes.
// When timestamps is true, each output line is prefixed with an RFC3339Nano timestamp.
func dockerLogs(name string, tail int, timestamps bool) ([]byte, error) {
	if tail <= 0 || tail > 500 {
		tail = 200
	}
	tsParam := "0"
	if timestamps {
		tsParam = "1"
	}
	url := fmt.Sprintf("%s/containers/%s/logs?stdout=1&stderr=1&tail=%d&timestamps=%s",
		dockerHost(), name, tail, tsParam)
```

- [ ] **Step 4: Update the call site in `logs_aggregate.go`**

In `middleware/logs_aggregate.go` line 69, change:

```go
// Before:
raw, err := dockerLogsFunc(name, tail)

// After:
raw, err := dockerLogsFunc(name, tail, false)
```

- [ ] **Step 5: Build and confirm it compiles**

```
cd middleware && go build ./...
```
Expected: success (no output)

- [ ] **Step 6: Run existing test to confirm it still passes**

```
cd middleware && go test ./... -run TestHandleLogsAggregateFansOut -v
```
Expected: PASS

- [ ] **Step 7: Commit**

```bash
cd middleware
git add docker.go logs_aggregate.go logs_aggregate_test.go
git commit -m "refactor(logs): add timestamps bool param to dockerLogsFunc"
```

---

### Task 2: Add timestamp parsing and sorting to `logs_aggregate.go`

**Files:**
- Modify: `middleware/logs_aggregate.go`

Add `Timestamp time.Time` to `LogEntry`, and two helpers: `parseLogTimestamp` (peels the RFC3339Nano prefix Docker adds with `timestamps=1`) and `sortedLogEntries` (sorts entries newest-first and trims to max).

- [ ] **Step 1: Write failing tests for the two helpers**

Add to `middleware/logs_aggregate_test.go`:

```go
func TestParseLogTimestamp(t *testing.T) {
	ts, line := parseLogTimestamp("2024-01-15T12:34:05.123456789Z sonarr started\n")
	if ts.IsZero() {
		t.Fatal("expected non-zero timestamp")
	}
	if line != "sonarr started\n" {
		t.Errorf("got line %q, want %q", line, "sonarr started\n")
	}

	// No timestamp prefix → zero time, line unchanged
	ts2, line2 := parseLogTimestamp("plain log line")
	if !ts2.IsZero() {
		t.Error("expected zero time for line without timestamp")
	}
	if line2 != "plain log line" {
		t.Errorf("got %q", line2)
	}
}

func TestSortedLogEntries(t *testing.T) {
	entries := []LogEntry{
		{Service: "a", Line: "old", Timestamp: time.Unix(100, 0)},
		{Service: "b", Line: "new", Timestamp: time.Unix(200, 0)},
		{Service: "c", Line: "older", Timestamp: time.Unix(50, 0)},
	}
	got := sortedLogEntries(entries, 2)
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	if got[0].Line != "new" || got[1].Line != "old" {
		t.Errorf("wrong order: got %v %v", got[0].Line, got[1].Line)
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```
cd middleware && go test ./... -run "TestParseLogTimestamp|TestSortedLogEntries" -v
```
Expected: FAIL — `parseLogTimestamp` and `sortedLogEntries` undefined

- [ ] **Step 3: Add `Timestamp` field to `LogEntry` and the two helpers**

In `middleware/logs_aggregate.go`, change the `LogEntry` struct and add the new imports and helpers:

Change the imports block to add `"sort"` and `"time"`:
```go
import (
	"bufio"
	"bytes"
	"net/http"
	"pelicula-api/httputil"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)
```

Change `LogEntry`:
```go
// LogEntry is one line tagged with its source service.
type LogEntry struct {
	Service   string    `json:"service"`
	Line      string    `json:"line"`
	Timestamp time.Time `json:"ts,omitempty"`
}
```

Add after the `LogEntry` definition:
```go
// parseLogTimestamp peels the RFC3339Nano prefix Docker adds when timestamps=1.
// Returns the parsed time and the remainder of the line. On parse failure, returns
// a zero time and the original line unchanged.
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
```

- [ ] **Step 4: Run tests to confirm they pass**

```
cd middleware && go test ./... -run "TestParseLogTimestamp|TestSortedLogEntries" -v
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd middleware
git add logs_aggregate.go logs_aggregate_test.go
git commit -m "feat(logs): add Timestamp to LogEntry and sorting helpers"
```

---

### Task 3: Add `fetchLogs` to SSEPoller

**Files:**
- Modify: `middleware/sse_poller.go`

`fetchLogs` fans out over `allowedContainers` with `timestamps=true`, parses each line, sorts newest-first, and returns JSON in the same `{entries, services}` shape as the HTTP endpoint.

- [ ] **Step 1: Write a failing test for `fetchLogs`**

Add to `middleware/sse_poller_test.go` (look at existing tests in that file first for the pattern — use `newTestPoller()` or however the test fixture creates a poller):

```go
func TestFetchLogsTimestampedAndSorted(t *testing.T) {
	orig := dockerLogsFunc
	dockerLogsFunc = func(name string, tail int, ts bool) ([]byte, error) {
		if !ts {
			t.Error("fetchLogs should pass timestamps=true")
		}
		switch name {
		case "sonarr":
			return []byte("2024-01-15T12:34:06.000000000Z sonarr newer\n2024-01-15T12:34:04.000000000Z sonarr older\n"), nil
		case "radarr":
			return []byte("2024-01-15T12:34:05.000000000Z radarr middle\n"), nil
		}
		return []byte{}, nil
	}
	t.Cleanup(func() { dockerLogsFunc = orig })

	// Temporarily restrict allowedContainers so we don't need all services
	orig2 := allowedContainers
	allowedContainers = map[string]bool{"sonarr": true, "radarr": true}
	t.Cleanup(func() { allowedContainers = orig2 })

	p := &SSEPoller{hub: newSSEHub(), hashes: make(map[string][32]byte)}
	data, err := p.fetchLogs(context.Background())
	if err != nil {
		t.Fatalf("fetchLogs error: %v", err)
	}
	var resp struct {
		Entries []LogEntry `json:"entries"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(resp.Entries))
	}
	// Newest first: sonarr newer (T+6) > radarr middle (T+5) > sonarr older (T+4)
	if resp.Entries[0].Line != "sonarr newer" {
		t.Errorf("first entry: got %q, want %q", resp.Entries[0].Line, "sonarr newer")
	}
	if resp.Entries[1].Line != "radarr middle" {
		t.Errorf("second entry: got %q, want %q", resp.Entries[1].Line, "radarr middle")
	}
}
```

- [ ] **Step 2: Run to confirm it fails**

```
cd middleware && go test ./... -run TestFetchLogsTimestampedAndSorted -v
```
Expected: FAIL — `p.fetchLogs` undefined (and possibly `allowedContainers` type mismatch — see Step 3 note)

- [ ] **Step 3: Check `allowedContainers` type in logs_aggregate.go**

```
grep -n "allowedContainers" middleware/logs_aggregate.go | head -5
```

If it's `map[string]struct{}` rather than `map[string]bool`, update the test fixture accordingly (use `map[string]struct{}{"sonarr":{}, "radarr":{}}` and the range check with `_, ok := allowedContainers[name]`). The test mock assignment will match whichever type the production code uses.

- [ ] **Step 4: Add `fetchLogs` to `sse_poller.go`**

Add `"bufio"`, `"bytes"`, and `"strings"` to the imports in `middleware/sse_poller.go`.

Add this method after `fetchNotifications`:

```go
// fetchLogs fans out over all allowed containers with Docker timestamps enabled,
// parses the RFC3339 prefix from each line, sorts entries newest-first, and
// returns the top 200 as JSON in the same {entries} shape as handleLogsAggregate.
func (p *SSEPoller) fetchLogs(ctx context.Context) ([]byte, error) {
	const perSvcTail = 50 // 10 services × 50 = 500 candidates; trimmed to 200

	type result struct {
		svc string
		raw []byte
		err error
	}
	ch := make(chan result, len(allowedContainers))
	var wg sync.WaitGroup
	for name := range allowedContainers {
		wg.Add(1)
		go func(svc string) {
			defer wg.Done()
			raw, err := dockerLogsFunc(svc, perSvcTail, true)
			ch <- result{svc: svc, raw: raw, err: err}
		}(name)
	}
	wg.Wait()
	close(ch)

	var entries []LogEntry
	for r := range ch {
		if r.err != nil {
			continue // skip unavailable containers silently
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
```

- [ ] **Step 5: Register `fetchLogs` in `pollOnce`**

In `sse_poller.go`, add `"logs"` to the `fetches` slice inside `pollOnce`:

```go
fetches := []namedFetch{
    {"pipeline", p.fetchPipeline},
    {"services", wrapFetch(p.fetchServices)},
    {"downloads", wrapFetch(p.fetchDownloads)},
    {"storage", wrapFetch(p.fetchStorage)},
    {"notifications", wrapFetch(p.fetchNotifications)},
    {"logs", wrapFetch(p.fetchLogs)},
}
```

- [ ] **Step 6: Run tests to confirm they pass**

```
cd middleware && go test ./... -run TestFetchLogsTimestampedAndSorted -v
```
Expected: PASS

- [ ] **Step 7: Run full test suite**

```
cd middleware && go test ./...
```
Expected: all PASS

- [ ] **Step 8: Commit**

```bash
cd middleware
git add sse_poller.go sse_poller_test.go
git commit -m "feat(logs): add fetchLogs to SSEPoller; broadcast timestamped log stream every 5s"
```

---

### Task 4: Frontend — add `logs` event to `sse.js`

**Files:**
- Modify: `nginx/sse.js`

Add a `logs` event listener that calls `window.renderLogsFromSSE` (exported by `logs.js` in Task 5).

- [ ] **Step 1: Add the listener**

In `nginx/sse.js`, add after the `storage` event listener (around line 68), before the closing `}` of the `connect()` function:

```js
        // logs event: {entries: [{service, line, ts},...]} — interleaved across services,
        // newest first. renderLogsFromSSE is exported by logs.js.
        source.addEventListener('logs', function(e) {
            try {
                var data = JSON.parse(e.data);
                if (window.renderLogsFromSSE) window.renderLogsFromSSE(data);
            } catch(err) { console.warn('[sse] logs parse error', err); }
        });
```

- [ ] **Step 2: Commit**

```bash
git add nginx/sse.js
git commit -m "feat(logs): add logs SSE event listener to sse.js"
```

---

### Task 5: Frontend — switch `logs.js` to SSE

**Files:**
- Modify: `nginx/logs.js`

Changes:
1. Add `lastEntries: []` to `logsState` — caches the last pushed entries for client-side filter re-renders.
2. Update `renderLogs` to filter by `logsState.enabled` before rendering (so toggling a chip re-renders without re-fetching).
3. Export `window.renderLogsFromSSE(data)` — called by `sse.js` on each `logs` event.
4. Filter chip click: if SSE is active, re-render from `logsState.lastEntries` instead of re-fetching.
5. Tab activation: skip the initial `loadLogs()` fetch if SSE is already connected; otherwise fetch once as a fallback.
6. Remove `window.logsRefresh` — no longer needed.

- [ ] **Step 1: Replace `nginx/logs.js` with the updated version**

```js
// logs.js — Logs tab: aggregated container log stream, coloured by service.
(function () {
'use strict';

const ALL_SERVICES = [
    'pelicula-api', 'procula', 'nginx',
    'sonarr', 'radarr', 'prowlarr',
    'qbittorrent', 'jellyfin', 'bazarr', 'gluetun',
];

const logsState = {
    loaded: false,
    loading: false,
    enabled: new Set(ALL_SERVICES),
    lastEntries: [],
};

function lfetch(url) { return fetch(url, { credentials: 'same-origin' }); }

async function loadLogs() {
    if (logsState.loading) return;
    logsState.loading = true;
    const out = document.getElementById('logs-stream');
    if (!out) { logsState.loading = false; return; }
    out.textContent = 'Loading\u2026';
    const enabled = Array.from(logsState.enabled).join(',');
    try {
        const res = await lfetch('/api/pelicula/logs/aggregate?tail=200&services=' + encodeURIComponent(enabled));
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const data = await res.json();
        logsState.lastEntries = data.entries || [];
        renderLogs(out, logsState.lastEntries);
        logsState.loaded = true;
    } catch (e) {
        out.textContent = 'Failed to load logs: ' + e.message;
    } finally {
        logsState.loading = false;
    }
}

function renderLogs(out, entries) {
    const frag = document.createDocumentFragment();
    for (const e of entries) {
        if (!logsState.enabled.has(e.service)) continue; // client-side service filter
        const row = document.createElement('span');
        row.className = 'logs-line logs-svc-' + e.service;
        const svc = document.createElement('span');
        svc.className = 'logs-line-svc';
        svc.textContent = e.service;
        row.appendChild(svc);
        row.appendChild(document.createTextNode(e.line + '\n'));
        frag.appendChild(row);
    }
    out.replaceChildren(frag);
    out.scrollTop = out.scrollHeight;
}

function renderFilters() {
    const wrap = document.getElementById('logs-filters');
    if (!wrap) return;
    const frag = document.createDocumentFragment();
    for (const svc of ALL_SERVICES) {
        const chip = document.createElement('span');
        chip.className = 'logs-filter-chip' + (logsState.enabled.has(svc) ? ' active' : '');
        chip.textContent = svc;
        chip.addEventListener('click', () => {
            if (logsState.enabled.has(svc)) logsState.enabled.delete(svc);
            else logsState.enabled.add(svc);
            renderFilters();
            const out = document.getElementById('logs-stream');
            if (window.sseIsActive && window.sseIsActive() && out) {
                renderLogs(out, logsState.lastEntries); // re-render from cache
            } else {
                loadLogs(); // fallback: re-fetch (SSE not connected)
            }
        });
        frag.appendChild(chip);
    }
    wrap.replaceChildren(frag);
}

// renderLogsFromSSE is called by sse.js on each 'logs' SSE event.
window.renderLogsFromSSE = function(data) {
    const out = document.getElementById('logs-stream');
    if (!out) return;
    logsState.lastEntries = data.entries || [];
    renderLogs(out, logsState.lastEntries);
    logsState.loaded = true;
};

PeliculaFW.onTab('logs', function () {
    renderFilters();
    // If SSE is already connected, the next push will populate the view.
    // Only do an initial fetch if SSE is not available.
    if (!window.sseIsActive || !window.sseIsActive()) {
        loadLogs();
    }
});

})();
```

- [ ] **Step 2: Verify the dashboard builds (no JS syntax errors)**

Open the dashboard in a browser (or run `node --check nginx/logs.js` if Node is available). There should be no syntax errors.

- [ ] **Step 3: Start the stack and open the Logs tab**

```
pelicula up
```

Navigate to the dashboard Logs tab. Confirm:
- The log stream populates automatically within 5 seconds (SSE push)
- New log lines appear as services emit them
- Clicking a service chip hides/shows its lines without triggering a page reload
- The stream is sorted newest-first

- [ ] **Step 4: Commit**

```bash
git add nginx/logs.js
git commit -m "feat(logs): switch Logs tab to SSE; client-side service filter; remove manual poll"
```

---

## Self-Review

**Spec coverage:**

| Requirement | Covered |
|-------------|---------|
| Log lines interleaved, newest at top | ✓ `sortedLogEntries` + `renderLogsFromSSE` |
| Each line timestamped for sorting | ✓ `parseLogTimestamp` + `LogEntry.Timestamp` |
| Live — updates without user interaction | ✓ SSE event every 5s via `fetchLogs` in `pollOnce` |
| Rolling window of ~200 lines total | ✓ `sortedLogEntries(entries, 200)` |
| Must use existing SSE hub | ✓ added to `pollOnce` fetches list |
| Docker timestamps via `timestamps=1` | ✓ `dockerLogs(name, tail, true)` |
| Service filter chips stay | ✓ chips re-render from `lastEntries` client-side |
| Color coding by service stays | ✓ `logs-svc-{service}` class unchanged |

**Open questions resolved:**
- Polling interval: 5s (existing `pollInterval` constant — no new constant needed)
- Timestamp display: hidden from rendered lines; used only for sorting (timestamps are stripped by `parseLogTimestamp` before the line is stored in `Line`)

**Type consistency check:**
- `LogEntry.Timestamp time.Time` defined in Task 2, consumed in Task 3 (`parseLogTimestamp`) — consistent ✓
- `sortedLogEntries([]LogEntry, int) []LogEntry` defined in Task 2, called in Task 3 — consistent ✓
- `dockerLogsFunc(name string, tail int, ts bool)` updated in Task 1, consumed in Task 3 — consistent ✓
- `renderLogsFromSSE` exported in Task 5, called by `sse.js` in Task 4 — consistent ✓
