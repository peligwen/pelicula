# Activity Feed Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Redesign the Activity section on the search tab to use a tiered list with inline drawers, auto-expire old events, and expose retry/dismiss actions per item.

**Architecture:** Three layers: active items (24h), a collapsible "older" separator, and dimmed older items. Each row expands inline to show an error detail drawer with context-appropriate actions. Backend gets two new optional fields on notifications (`detail`, `job_id`) and a 7-day prune pass. Frontend becomes a proper PeliculaFW component in its own file.

**Tech Stack:** Go (procula, middleware), vanilla JS (PeliculaFW component), CSS custom properties

---

## File Map

| File | Change |
|------|--------|
| `procula/catalog.go` | Add `JobID`/`Detail` to `NotificationEvent`; update `buildEvent`; add 7-day prune to `appendToFeed` |
| `procula/storage_test.go` | Add tests for `buildEvent` fields and 7-day prune |
| `middleware/hooks.go` | Add `Detail`/`JobID` to `dashNotif`; pass through from procula; add `Detail` for arr events |
| `middleware/hooks_test.go` | Add test for `handleNotificationsProxy` field passthrough |
| `middleware/library.go` | Add `handleJobRetry` proxy |
| `middleware/library_test.go` | Add test for `handleJobRetry` |
| `middleware/main.go` | Register retry route |
| `nginx/styles.css` | Add `.act-*` CSS rules |
| `nginx/activity.js` | New PeliculaFW component — all activity rendering and actions |
| `nginx/dashboard.js` | Remove `renderActivity()`; mount `activity` component |
| `nginx/index.html` | Add script tag for `activity.js` |

---

## Task 1: Extend procula NotificationEvent

**Files:**
- Modify: `procula/catalog.go`
- Test: `procula/storage_test.go`

- [ ] **Step 1: Write the failing test**

Add to `procula/storage_test.go`:

```go
func TestBuildEvent_SetsJobIDAndDetail(t *testing.T) {
	job := &Job{
		ID:    "abc12345",
		Error: "FFmpeg error: codec not supported",
		Source: Source{
			Title: "Dune Part Two",
			Year:  2024,
			Type:  "movie",
		},
	}

	// Failure event: detail and job_id should be set
	ev := buildEvent(job, "validation_failed", "Validation failed: Dune Part Two")
	if ev.JobID != "abc12345" {
		t.Errorf("JobID = %q, want %q", ev.JobID, "abc12345")
	}
	if ev.Detail != "FFmpeg error: codec not supported" {
		t.Errorf("Detail = %q, want %q", ev.Detail, "FFmpeg error: codec not supported")
	}

	// content_ready: detail should be empty (don't leak error text for successful imports)
	job.Error = "should not appear"
	ev2 := buildEvent(job, "content_ready", "Movie ready: Dune Part Two (2024)")
	if ev2.Detail != "" {
		t.Errorf("content_ready Detail = %q, want empty", ev2.Detail)
	}
	if ev2.JobID != "abc12345" {
		t.Errorf("content_ready JobID = %q, want %q", ev2.JobID, "abc12345")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
cd procula && go test -run TestBuildEvent_SetsJobIDAndDetail -v
```

Expected: FAIL — `ev.JobID` and `ev.Detail` are empty (fields don't exist yet).

- [ ] **Step 3: Add fields to NotificationEvent and update buildEvent**

In `procula/catalog.go`, update the `NotificationEvent` struct (lines 19-27):

```go
type NotificationEvent struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"` // "content_ready", "validation_failed", "transcode_failed"
	Title     string    `json:"title"`
	Year      int       `json:"year,omitempty"`
	MediaType string    `json:"media_type"` // "movie" or "episode"
	Message   string    `json:"message"`
	Detail    string    `json:"detail,omitempty"`  // error text for drawer; empty for content_ready
	JobID     string    `json:"job_id,omitempty"`  // procula job ID; enables Retry action
}
```

Replace `buildEvent` (lines 105-119):

```go
func buildEvent(job *Job, eventType, message string) NotificationEvent {
	suffix := job.ID
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	detail := ""
	if eventType != "content_ready" {
		detail = job.Error
	}
	return NotificationEvent{
		ID:        fmt.Sprintf("notif_%d_%s", time.Now().UnixNano(), suffix),
		Timestamp: time.Now().UTC(),
		Type:      eventType,
		Title:     job.Source.Title,
		Year:      job.Source.Year,
		MediaType: job.Source.Type,
		Message:   message,
		Detail:    detail,
		JobID:     job.ID,
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```
cd procula && go test -run TestBuildEvent_SetsJobIDAndDetail -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add procula/catalog.go procula/storage_test.go
git commit -m "feat(procula): add job_id and detail fields to NotificationEvent"
```

---

## Task 2: Add 7-day prune to appendToFeed

**Files:**
- Modify: `procula/catalog.go`
- Test: `procula/storage_test.go`

- [ ] **Step 1: Write the failing test**

Add to `procula/storage_test.go`:

```go
func TestAppendToFeed_PrunesEventsOlderThan7Days(t *testing.T) {
	dir := t.TempDir()

	old := NotificationEvent{
		ID:        "old-event",
		Timestamp: time.Now().UTC().Add(-8 * 24 * time.Hour), // 8 days ago
		Type:      "content_ready",
		Message:   "old movie",
	}
	recent := NotificationEvent{
		ID:        "recent-event",
		Timestamp: time.Now().UTC().Add(-1 * time.Hour),
		Type:      "content_ready",
		Message:   "new movie",
	}

	appendToFeed(dir, old)
	appendToFeed(dir, recent)

	feedPath := filepath.Join(dir, "procula", "notifications_feed.json")
	data, err := os.ReadFile(feedPath)
	if err != nil {
		t.Fatalf("feed file not created: %v", err)
	}
	var events []NotificationEvent
	if err := json.Unmarshal(data, &events); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, ev := range events {
		if ev.ID == "old-event" {
			t.Error("old event (8 days ago) should have been pruned")
		}
	}
	found := false
	for _, ev := range events {
		if ev.ID == "recent-event" {
			found = true
		}
	}
	if !found {
		t.Error("recent event should still be present")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
cd procula && go test -run TestAppendToFeed_PrunesEventsOlderThan7Days -v
```

Expected: FAIL — old event is not pruned.

- [ ] **Step 3: Add 7-day prune to appendToFeed**

In `procula/catalog.go`, replace the prepend+cap block inside `appendToFeed` (lines 137-141):

```go
	// Prepend new event, prune events older than 7 days, cap at maxFeedEvents.
	events = append([]NotificationEvent{event}, events...)
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
	pruned := events[:0]
	for _, e := range events {
		if e.Timestamp.After(cutoff) {
			pruned = append(pruned, e)
		}
	}
	if len(pruned) > maxFeedEvents {
		pruned = pruned[:maxFeedEvents]
	}
	events = pruned
```

- [ ] **Step 4: Run tests to verify they pass**

```
cd procula && go test -run "TestAppendToFeed" -v
```

Expected: both existing and new tests PASS.

- [ ] **Step 5: Commit**

```bash
git add procula/catalog.go procula/storage_test.go
git commit -m "feat(procula): prune notifications feed entries older than 7 days"
```

---

## Task 3: Extend dashNotif and update proxy passthrough

**Files:**
- Modify: `middleware/hooks.go`
- Test: `middleware/hooks_test.go`

- [ ] **Step 1: Write the failing test**

Add to `middleware/hooks_test.go`:

```go
func TestHandleNotificationsProxy_PassesThroughDetailAndJobID(t *testing.T) {
	proculaBody := `[{"id":"notif_1","timestamp":"2026-04-14T10:00:00Z","type":"validation_failed","message":"Validation failed: Dune","detail":"FFmpeg error: codec not supported","job_id":"abc12345"}]`
	fake := newFakeProcula(t, "/api/procula/notifications", proculaBody)
	defer fake.Close()

	old := proculaURL
	origSvc := services
	proculaURL = fake.URL
	services = NewServiceClients("/config")
	t.Cleanup(func() { proculaURL = old; services = origSvc })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/notifications", nil)
	w := httptest.NewRecorder()
	handleNotificationsProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var events []struct {
		ID     string `json:"id"`
		Detail string `json:"detail"`
		JobID  string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}
	if events[0].Detail != "FFmpeg error: codec not supported" {
		t.Errorf("Detail = %q, want %q", events[0].Detail, "FFmpeg error: codec not supported")
	}
	if events[0].JobID != "abc12345" {
		t.Errorf("JobID = %q, want %q", events[0].JobID, "abc12345")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
cd middleware && go test -run TestHandleNotificationsProxy_PassesThroughDetailAndJobID -v
```

Expected: FAIL — `Detail` and `JobID` are empty (fields not on `dashNotif` yet).

- [ ] **Step 3: Extend dashNotif struct**

In `middleware/hooks.go`, update `dashNotif` (lines 312-317):

```go
// dashNotif is the shape the dashboard notification panel expects.
type dashNotif struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"` // "content_ready", "download_failed", "validation_failed", "transcode_failed"
	Message   string    `json:"message"`
	Detail    string    `json:"detail,omitempty"`  // error text / release info for drawer
	JobID     string    `json:"job_id,omitempty"`  // procula job ID; enables Retry action
}
```

- [ ] **Step 4: Update the procula feed decode in handleNotificationsProxy**

In `middleware/hooks.go`, replace the anonymous struct used to decode procula events (inside the procula goroutine, lines 345-357):

```go
		var events []struct {
			ID        string    `json:"id"`
			Timestamp time.Time `json:"timestamp"`
			Type      string    `json:"type"`
			Message   string    `json:"message"`
			Detail    string    `json:"detail"`
			JobID     string    `json:"job_id"`
		}
		if json.NewDecoder(resp.Body).Decode(&events) == nil {
			mu.Lock()
			for _, e := range events {
				all = append(all, dashNotif{
					ID:        e.ID,
					Timestamp: e.Timestamp,
					Type:      e.Type,
					Message:   e.Message,
					Detail:    e.Detail,
					JobID:     e.JobID,
				})
			}
			mu.Unlock()
		}
```

- [ ] **Step 5: Add Detail to arr history events in fetchArrHistory**

In `middleware/hooks.go`, replace the two switch cases and the append at the end of `fetchArrHistory` (lines 424-437):

```go
		switch eventType {
		case "downloadFolderImported":
			nType = "content_ready"
			msg = arrImportMessage(rec, arrType)
		case "downloadFailed":
			nType = "download_failed"
			msg = arrFailedMessage(rec, arrType)
		default:
			continue
		}
		detail := strVal(rec, "sourceTitle")
		if nType == "download_failed" {
			if data, ok := rec["data"].(map[string]any); ok {
				if reason := strVal(data, "reason"); reason != "" {
					detail += " · " + reason
				}
			}
		}
		id := fmt.Sprintf("%s:%v", arrType, rec["id"])
		ts := parseArrDate(strVal(rec, "date"))
		notifs = append(notifs, dashNotif{ID: id, Timestamp: ts, Type: nType, Message: msg, Detail: detail})
```

- [ ] **Step 6: Run tests to verify they pass**

```
cd middleware && go test -run "TestHandleNotificationsProxy" -v
```

Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add middleware/hooks.go middleware/hooks_test.go
git commit -m "feat(middleware): add detail and job_id to dashNotif; pass through from procula and arr history"
```

---

## Task 4: Add retry proxy endpoint

**Files:**
- Modify: `middleware/library.go`
- Modify: `middleware/main.go`
- Test: `middleware/library_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `middleware/library_test.go`:

```go
func TestHandleJobRetry_ProxiesToProcula(t *testing.T) {
	var gotPath string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer fake.Close()

	old := proculaURL
	origSvc := services
	proculaURL = fake.URL
	services = NewServiceClients("/config")
	t.Cleanup(func() { proculaURL = old; services = origSvc })

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/procula/jobs/abc123/retry", nil)
	req.SetPathValue("id", "abc123")
	w := httptest.NewRecorder()
	handleJobRetry(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if gotPath != "/api/procula/jobs/abc123/retry" {
		t.Errorf("proxied to %q, want /api/procula/jobs/abc123/retry", gotPath)
	}
}

func TestHandleJobRetry_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/procula/jobs/abc123/retry", nil)
	req.SetPathValue("id", "abc123")
	w := httptest.NewRecorder()
	handleJobRetry(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
cd middleware && go test -run "TestHandleJobRetry" -v
```

Expected: FAIL — `handleJobRetry` undefined.

- [ ] **Step 3: Add handleJobRetry to library.go**

Add directly after `handleJobResub` in `middleware/library.go`:

```go
// handleJobRetry proxies POST /api/procula/jobs/{id}/retry — re-queues
// a failed procula job for processing.
func handleJobRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteError(w, "job id required", http.StatusBadRequest)
		return
	}
	upstream, err := http.NewRequest(http.MethodPost, proculaURL+"/api/procula/jobs/"+url.PathEscape(id)+"/retry", nil)
	if err != nil {
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
		upstream.Header.Set("X-API-Key", key)
	}
	resp, err := services.client.Do(upstream)
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}
```

- [ ] **Step 4: Register the route in main.go**

In `middleware/main.go`, add after the existing `/resub` route (near line 188):

```go
mux.Handle("/api/pelicula/procula/jobs/{id}/retry", auth.GuardAdmin(http.HandlerFunc(handleJobRetry)))
```

- [ ] **Step 5: Run tests to verify they pass**

```
cd middleware && go test -run "TestHandleJobRetry" -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add middleware/library.go middleware/library_test.go middleware/main.go
git commit -m "feat(middleware): add retry proxy for procula jobs"
```

---

## Task 5: Add activity CSS

**Files:**
- Modify: `nginx/styles.css`

- [ ] **Step 1: Replace the existing activity CSS block**

In `nginx/styles.css`, find and replace the `/* ---- Activity feed ---- */` block (lines 981-993):

```css
/* ---- Activity feed ----------------------------------------------------------------- */
.activity-item { display: none; } /* legacy -- replaced by .act-* */

.act-item {
    border-bottom: 1px solid var(--border);
    cursor: pointer;
    user-select: none;
}
.act-item:last-child { border-bottom: none; }

.act-row {
    display: flex; align-items: center; gap: 0.6rem;
    padding: 0.5rem 1rem; font-size: 0.8rem;
}
.act-icon { flex-shrink: 0; font-size: 0.75rem; }
.act-msg  { flex: 1; color: var(--ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.act-time { flex-shrink: 0; font-size: 0.68rem; color: var(--faint); }

.act-x {
    flex-shrink: 0; background: none; border: none; color: var(--faint);
    cursor: pointer; font-size: 0.75rem; padding: 0.1rem 0.3rem;
    border-radius: 3px; line-height: 1;
}
.act-x:hover { color: var(--pink); background: var(--pink-l); }

.act-item.notif-ready   .act-icon { color: var(--mint); }
.act-item.notif-failed  .act-icon { color: var(--pink); }
.act-item.notif-storage .act-icon { color: var(--warn); }

.act-drawer {
    display: none;
    padding: 0.5rem 1rem 0.7rem 2.4rem;
    background: rgba(255,255,255,0.02);
    border-top: 1px solid var(--border);
    font-size: 0.76rem; color: var(--muted);
}
.act-drawer.open { display: block; }
.act-detail {
    margin-bottom: 0.55rem;
    font-family: var(--font-mono, monospace);
    font-size: 0.71rem; color: var(--warn);
    line-height: 1.5;
}
.act-actions { display: flex; gap: 0.45rem; flex-wrap: wrap; }
.act-btn {
    background: none; border: 1px solid var(--border2);
    color: var(--muted); border-radius: 4px;
    padding: 0.2rem 0.65rem; font-size: 0.7rem;
    cursor: pointer; letter-spacing: 0.04em;
    font-family: var(--font-display, 'Nunito', sans-serif); font-weight: 700;
    transition: border-color 0.15s, color 0.15s;
}
.act-btn:hover { border-color: rgba(240,96,168,0.4); color: var(--pink); }
.act-btn.act-btn-primary { border-color: rgba(112,128,232,0.5); color: #7080e8; }
.act-btn.act-btn-primary:hover { background: rgba(112,128,232,0.1); }

.act-sep {
    display: flex; align-items: center; gap: 0.6rem;
    padding: 0.3rem 1rem; font-size: 0.68rem; color: var(--faint);
    cursor: pointer;
    border-top: 1px solid var(--border);
    border-bottom: 1px solid var(--border);
    user-select: none;
}
.act-sep:hover { color: var(--muted); background: rgba(255,255,255,0.02); }
.act-sep-line { flex: 1; height: 1px; background: var(--border); }
.act-sep-chevron { font-size: 0.6rem; transition: transform 0.2s; display: inline-block; }
.act-sep-chevron.open { transform: rotate(180deg); }

.act-older { display: none; }
.act-older.visible { display: block; }
.act-older .act-item { opacity: 0.55; }

.act-empty { padding: 1.2rem 1rem; font-size: 0.8rem; color: var(--faint); text-align: center; }
```

- [ ] **Step 2: Commit**

```bash
git add nginx/styles.css
git commit -m "style(activity): replace legacy activity CSS with tiered drawer styles"
```

---

## Task 6: Create activity.js PeliculaFW component

**Files:**
- Create: `nginx/activity.js`

**Security note:** All event data is passed through PeliculaFW's `html` tagged template, which auto-escapes all string interpolations. The only `raw()` calls are for static icon HTML entity strings (e.g. `'&#10003;'`). This is the same pattern used in `search.js`, `notifications.js`, and `downloads.js`.

- [ ] **Step 1: Create the component file**

Create `nginx/activity.js`:

```javascript
// nginx/activity.js
// Activity feed component -- registered with PeliculaFW; mounted by dashboard.js.
// Depends on: framework.js (PeliculaFW, router, toast, html, raw), dashboard.js (tfetch, checkNotifications).
//
// Security: all event data is passed through PeliculaFW's html tagged template, which
// auto-escapes string interpolations. raw() is only used for static icon HTML entity strings.

'use strict';

(function () {
    const { component, html, raw, toast, router } = PeliculaFW;

    // 24 hours -- boundary between "active" and "older" tiers
    const ACTIVE_MS = 24 * 60 * 60 * 1000;

    function notifIcon(type) {
        if (type === 'content_ready') return '&#10003;';
        if (type === 'storage_warning' || type === 'storage_critical') return '&#9632;';
        return '&#9888;';
    }

    function notifClass(type) {
        if (type === 'content_ready') return 'notif-ready';
        if (type === 'storage_warning' || type === 'storage_critical') return 'notif-storage';
        return 'notif-failed';
    }

    function formatTime(ts) {
        try {
            const diff = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
            if (diff < 60) return 'just now';
            if (diff < 3600) return Math.floor(diff / 60) + 'm ago';
            if (diff < 86400) return Math.floor(diff / 3600) + 'h ago';
            return Math.floor(diff / 86400) + 'd ago';
        } catch { return ''; }
    }

    function buildDrawer(e) {
        const actions = [];
        if (e.type === 'validation_failed' || e.type === 'transcode_failed') {
            if (e.job_id) {
                actions.push(html`<button class="act-btn act-btn-primary" onclick="actRetry('${e.job_id}')">Retry</button>`);
                actions.push(html`<button class="act-btn" onclick="actJumpToJob('${e.job_id}')">Jump to job</button>`);
            }
        } else if (e.type === 'storage_warning' || e.type === 'storage_critical') {
            actions.push(html`<button class="act-btn act-btn-primary" onclick="actGoToStorage()">Go to storage</button>`);
        }
        actions.push(html`<button class="act-btn" onclick="actDismiss('${e.id}')">Dismiss</button>`);

        const detail = e.detail ? html`<div class="act-detail">${e.detail}</div>` : raw('');
        return html`<div class="act-drawer">${detail}<div class="act-actions">${actions}</div></div>`;
    }

    function buildRow(e) {
        return html`<div class="act-item ${notifClass(e.type)}" onclick="actToggleDrawer(this)">
            <div class="act-row">
                <span class="act-icon">${raw(notifIcon(e.type))}</span>
                <span class="act-msg">${e.message}</span>
                <span class="act-time">${formatTime(e.timestamp)}</span>
                <button class="act-x" title="Dismiss"
                    onclick="event.stopPropagation();actDismiss('${e.id}')">&#10005;</button>
            </div>
            ${buildDrawer(e)}
        </div>`;
    }

    function renderActivity(events) {
        const list = document.getElementById('activity-list');
        if (!list) return;

        if (!Array.isArray(events) || !events.length) {
            list.innerHTML = html`<div class="act-empty">No recent activity yet.</div>`.str;
            return;
        }

        const now = Date.now();
        const active = events.filter(e => now - new Date(e.timestamp).getTime() <= ACTIVE_MS);
        const older  = events.filter(e => now - new Date(e.timestamp).getTime() >  ACTIVE_MS);

        let out = active.map(e => buildRow(e).str).join('');

        if (older.length > 0) {
            const label = older.length + ' older event' + (older.length !== 1 ? 's' : '');
            out += html`<div class="act-sep" id="act-sep" onclick="actToggleOlder()">
                <span class="act-sep-line"></span>
                <span>${label}</span>
                <span class="act-sep-chevron" id="act-chevron">&#9660;</span>
                <span class="act-sep-line"></span>
            </div>`.str;
            out += html`<div class="act-older" id="act-older">${older.map(e => buildRow(e))}</div>`.str;
        }

        list.innerHTML = out;
    }

    // -- Action handlers (called via onclick attributes) ----------------------

    function actToggleDrawer(item) {
        const drawer = item.querySelector('.act-drawer');
        if (drawer) drawer.classList.toggle('open');
    }

    function actToggleOlder() {
        const older   = document.getElementById('act-older');
        const chevron = document.getElementById('act-chevron');
        if (older)   older.classList.toggle('visible');
        if (chevron) chevron.classList.toggle('open');
    }

    async function actDismiss(id) {
        try {
            const res = await tfetch('/api/pelicula/notifications/' + id, { method: 'DELETE' });
            if (!res.ok) { toast('Could not dismiss', { error: true }); return; }
        } catch { toast('Could not dismiss', { error: true }); return; }
        // Re-fetch so both the activity feed and the notification bell update.
        try {
            const res = await tfetch('/api/pelicula/notifications');
            if (!res.ok) return;
            const events = await res.json();
            renderNotifications(events); // exported by notifications.js
            renderActivity(events);      // this component's own export
        } catch (e) { console.warn('[activity] dismiss refresh error:', e); }
    }

    async function actRetry(jobId) {
        try {
            const res = await tfetch('/api/pelicula/procula/jobs/' + jobId + '/retry', { method: 'POST' });
            if (!res.ok) { toast('Retry failed', { error: true }); return; }
            toast('Job queued for retry');
        } catch { toast('Retry failed', { error: true }); }
    }

    function actJumpToJob(jobId) {
        router.navigate('jobs', { id: jobId });
    }

    function actGoToStorage() {
        router.navigate('storage');
    }

    // -- Component registration -----------------------------------------------

    component('activity', function () {
        return { render: function () {} }; // renderActivity is called directly by checkNotifications
    });

    // -- Window exports (for onclick handlers and dashboard.js) ---------------
    window.renderActivity  = renderActivity;
    window.actToggleDrawer = actToggleDrawer;
    window.actToggleOlder  = actToggleOlder;
    window.actDismiss      = actDismiss;
    window.actRetry        = actRetry;
    window.actJumpToJob    = actJumpToJob;
    window.actGoToStorage  = actGoToStorage;
}());
```

- [ ] **Step 2: Commit**

```bash
git add nginx/activity.js
git commit -m "feat(activity): add tiered activity feed component with inline drawers"
```

---

## Task 7: Wire activity component in dashboard.js and index.html

**Files:**
- Modify: `nginx/dashboard.js`
- Modify: `nginx/index.html`

- [ ] **Step 1: Remove renderActivity from dashboard.js**

In `nginx/dashboard.js`, delete the entire block from the comment through the closing brace (lines 220-239):

```
// DELETE this entire block (the comment line plus the full renderActivity function):
// ---- Activity feed -------------------------
function renderActivity(events) { ... }
```

- [ ] **Step 2: Mount the activity component**

In `nginx/dashboard.js`, find the component mount calls (near line 889):

```javascript
    PeliculaFW.mount('search', document.getElementById('search-section'));
    PeliculaFW.mount('downloads', document.getElementById('pipeline-section'));
```

Add the activity mount on the next line:

```javascript
    PeliculaFW.mount('activity', document.getElementById('activity-section'));
```

- [ ] **Step 3: Add script tag in index.html**

In `nginx/index.html`, find the block of deferred script tags. Add `activity.js` alongside the other component scripts (after `notifications.js`):

```html
<script defer src="/activity.js"></script>
```

- [ ] **Step 4: Verify in the browser**

Open the dashboard at `http://localhost:7354` and check the browser console:
- No JS errors on page load
- Activity section renders (list of events, or "No recent activity yet.")
- Clicking a row expands the inline drawer
- The X button dismisses and the feed refreshes
- Older events appear below the separator when clicked

- [ ] **Step 5: Commit**

```bash
git add nginx/dashboard.js nginx/index.html
git commit -m "feat(dashboard): mount activity component; remove legacy renderActivity"
```
