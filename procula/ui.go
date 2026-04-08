package main

import (
	"net/http"
)

func handleUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Write([]byte(procualdashHTML))
}

func handleUICSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(proculaCSS))
}

const proculaCSS = `
* { margin: 0; padding: 0; box-sizing: border-box; }
.hidden { display: none !important; }
body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    background: #0a0a0a; color: #e0e0e0; min-height: 100vh;
}
.masthead {
    background: linear-gradient(180deg, #111 0%, #0a0a0a 100%);
    border-bottom: 1px solid #1a1a1a;
    padding: 1.5rem 2rem 1.25rem;
}
.masthead-inner { max-width: 900px; margin: 0 auto; display: flex; align-items: flex-end; justify-content: space-between; }
.logo { text-decoration: none; font-family: "Helvetica Neue", Helvetica, Arial, sans-serif; font-weight: 700; font-size: 2rem; letter-spacing: 0.35em; text-transform: uppercase; color: #fff; }
.logo .accent { color: #c8a2ff; }
.logo-sub { font-size: 0.55rem; letter-spacing: 0.5em; text-transform: uppercase; color: #555; margin-top: 0.3rem; }
.back-link { font-size: 0.75rem; color: #555; text-decoration: none; letter-spacing: 0.05em; }
.back-link:hover { color: #888; }
.main { max-width: 900px; margin: 0 auto; padding: 1.5rem 2rem 2rem; }
.section { margin-bottom: 1.5rem; }
.section-header { font-size: 0.65rem; color: #555; text-transform: uppercase; letter-spacing: 0.12em; margin-bottom: 0.75rem; display: flex; justify-content: space-between; align-items: center; }
.status-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(100px, 1fr)); gap: 0.5rem; }
.stat-box { background: #131313; border: 1px solid #1e1e1e; border-radius: 8px; padding: 0.75rem 1rem; }
.stat-label { font-size: 0.65rem; color: #555; text-transform: uppercase; letter-spacing: 0.08em; }
.stat-value { font-size: 1.4rem; font-weight: 600; font-family: "SF Mono", "Menlo", monospace; color: #ccc; margin-top: 0.15rem; }
.stat-value.ok { color: #7dda93; }
.card { background: #131313; border: 1px solid #1e1e1e; border-radius: 8px; padding: 1rem 1.25rem; margin-bottom: 0.5rem; }
.toggle-row { display: flex; align-items: center; justify-content: space-between; padding: 0.65rem 0; border-bottom: 1px solid #1a1a1a; }
.toggle-row:last-child { border-bottom: none; }
.toggle-label { font-size: 0.9rem; font-weight: 500; }
.toggle-desc { font-size: 0.75rem; color: #555; margin-top: 0.15rem; }
.toggle { position: relative; width: 38px; height: 22px; flex-shrink: 0; }
.toggle input { opacity: 0; width: 0; height: 0; }
.toggle-track { position: absolute; inset: 0; background: #222; border: 1px solid #333; border-radius: 22px; cursor: pointer; transition: background 0.2s, border-color 0.2s; }
.toggle input:checked + .toggle-track { background: #1a3020; border-color: #2a5a38; }
.toggle-thumb { position: absolute; top: 3px; left: 3px; width: 14px; height: 14px; background: #444; border-radius: 50%; transition: transform 0.2s, background 0.2s; pointer-events: none; }
.toggle input:checked ~ .toggle-thumb { transform: translateX(16px); background: #7dda93; }
.field-group { margin-top: 0.75rem; }
.field-label { font-size: 0.75rem; color: #999; margin-bottom: 0.35rem; display: block; }
.field-input { width: 100%; padding: 0.6rem 0.75rem; background: #0a0a0a; border: 1px solid #222; border-radius: 6px; color: #e0e0e0; font-size: 0.85rem; outline: none; font-family: "SF Mono", "Menlo", monospace; }
.field-input:focus { border-color: #444; }
textarea.field-input { min-height: 80px; resize: vertical; }
.radio-group { display: flex; gap: 0.5rem; flex-wrap: wrap; margin-top: 0.5rem; }
.radio-opt { display: flex; align-items: center; gap: 0.4rem; cursor: pointer; }
.radio-opt input { accent-color: #c8a2ff; }
.radio-opt span { font-size: 0.82rem; color: #aaa; }
.notif-extra { margin-top: 0.75rem; }
.save-bar { display: flex; gap: 0.75rem; align-items: center; margin-top: 1.25rem; }
.btn-save { padding: 0.6rem 1.5rem; background: #1e1e1e; border: 1px solid #2a2a2a; border-radius: 6px; color: #ccc; font-size: 0.9rem; cursor: pointer; transition: background 0.15s; }
.btn-save:hover { background: #282828; }
.btn-save:disabled { opacity: 0.5; cursor: not-allowed; }
.save-msg { font-size: 0.8rem; color: #7dda93; opacity: 0; transition: opacity 0.3s; }
.save-msg.visible { opacity: 1; }
.save-msg.err { color: #f87171; }
.footer { margin-top: 2rem; text-align: center; font-size: 0.65rem; color: #333; letter-spacing: 0.05em; }

/* Tabs */
.tabs { display: flex; gap: 0; border-bottom: 1px solid #1e1e1e; margin-bottom: 1.5rem; }
.tab-btn { padding: 0.6rem 1.25rem; background: none; border: none; border-bottom: 2px solid transparent; color: #555; font-size: 0.8rem; letter-spacing: 0.06em; text-transform: uppercase; cursor: pointer; transition: color 0.15s, border-color 0.15s; }
.tab-btn:hover { color: #aaa; }
.tab-btn.active { color: #c8a2ff; border-bottom-color: #c8a2ff; }
.tab-pane { display: none; }
.tab-pane.active { display: block; }

/* Jobs table */
.jobs-table { width: 100%; border-collapse: collapse; font-size: 0.82rem; }
.jobs-table th { font-size: 0.6rem; color: #555; text-transform: uppercase; letter-spacing: 0.08em; padding: 0.4rem 0.75rem; text-align: left; border-bottom: 1px solid #1a1a1a; }
.jobs-table td { padding: 0.55rem 0.75rem; border-bottom: 1px solid #111; vertical-align: top; }
.jobs-table tr:last-child td { border-bottom: none; }
.jobs-table tr { cursor: pointer; transition: background 0.1s; }
.jobs-table tr:hover td { background: #131313; }
.job-state-badge { display: inline-block; font-size: 0.65rem; padding: 0.1rem 0.45rem; border-radius: 4px; font-weight: 500; }
.state-queued { background: #111; color: #666; border: 1px solid #222; }
.state-processing { background: #1a1030; color: #c8a2ff; border: 1px solid #3a2a60; }
.state-completed { background: #0e2016; color: #7dda93; border: 1px solid #1a4030; }
.state-failed { background: #1a0808; color: #f87171; border: 1px solid #4a1a1a; }
.state-cancelled { background: #111; color: #555; border: 1px solid #222; }
.job-progress-bar { height: 3px; background: #1a1a1a; border-radius: 2px; margin-top: 4px; }
.job-progress-fill { height: 100%; border-radius: 2px; background: #c8a2ff; transition: width 0.3s; }
.job-progress-fill.done { background: #7dda93; }
.job-progress-fill.failed { background: #f87171; }
.job-title { font-weight: 500; }
.job-year { color: #555; font-size: 0.75rem; margin-left: 0.3rem; }
.empty-state { padding: 2rem; text-align: center; color: #444; font-size: 0.82rem; }

/* Job detail drawer */
.drawer-backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.6); z-index: 100; display: none; }
.drawer-backdrop.open { display: block; }
.drawer { position: fixed; right: 0; top: 0; bottom: 0; width: min(560px, 100vw); background: #0f0f0f; border-left: 1px solid #1e1e1e; overflow-y: auto; z-index: 101; transform: translateX(100%); transition: transform 0.22s ease; }
.drawer.open { transform: translateX(0); }
.drawer-header { padding: 1.25rem 1.5rem; border-bottom: 1px solid #1a1a1a; display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
.drawer-title { font-size: 1rem; font-weight: 600; }
.drawer-close { background: none; border: none; color: #555; font-size: 1.2rem; cursor: pointer; padding: 0.1rem 0.3rem; line-height: 1; }
.drawer-close:hover { color: #aaa; }
.drawer-body { padding: 1.25rem 1.5rem; }
.drawer-section { margin-bottom: 1.25rem; }
.drawer-section-label { font-size: 0.6rem; color: #555; text-transform: uppercase; letter-spacing: 0.1em; margin-bottom: 0.5rem; }
.check-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(120px, 1fr)); gap: 0.4rem; }
.check-item { background: #131313; border: 1px solid #1e1e1e; border-radius: 6px; padding: 0.5rem 0.75rem; }
.check-name { font-size: 0.65rem; color: #555; text-transform: uppercase; letter-spacing: 0.05em; }
.check-val { font-size: 0.85rem; font-weight: 500; margin-top: 0.2rem; }
.check-pass { color: #7dda93; }
.check-fail { color: #f87171; }
.check-warn { color: #f0c060; }
.check-skip { color: #555; }
.check-pending { color: #666; }
.codec-row { display: flex; gap: 1rem; flex-wrap: wrap; margin-top: 0.4rem; }
.codec-item { background: #131313; border: 1px solid #1e1e1e; border-radius: 5px; padding: 0.35rem 0.6rem; font-size: 0.78rem; font-family: "SF Mono","Menlo",monospace; color: #c8a2ff; }
.output-list { margin-top: 0.4rem; }
.output-item { font-size: 0.78rem; font-family: "SF Mono","Menlo",monospace; color: #888; word-break: break-all; padding: 0.2rem 0; }
.error-box { background: #1a0808; border: 1px solid #4a1a1a; border-radius: 6px; padding: 0.75rem; font-size: 0.8rem; color: #f87171; font-family: "SF Mono","Menlo",monospace; word-break: break-word; margin-top: 0.4rem; }
.drawer-actions { display: flex; gap: 0.5rem; margin-top: 1rem; }
.btn-action { padding: 0.5rem 1rem; border-radius: 6px; border: 1px solid #2a2a2a; background: #1a1a1a; color: #ccc; font-size: 0.82rem; cursor: pointer; transition: background 0.15s; }
.btn-action:hover { background: #242424; }
.btn-action.danger { border-color: #4a1a1a; color: #f87171; }
.btn-action.danger:hover { background: #1a0808; }

/* Event log */
.event-filters { display: flex; gap: 0.5rem; flex-wrap: wrap; margin-bottom: 1rem; }
.filter-chip { padding: 0.25rem 0.75rem; border-radius: 20px; border: 1px solid #222; background: #111; color: #666; font-size: 0.72rem; cursor: pointer; transition: all 0.15s; }
.filter-chip:hover { border-color: #333; color: #999; }
.filter-chip.active { background: #1a1030; border-color: #3a2a60; color: #c8a2ff; }
.event-row { display: flex; gap: 0.75rem; align-items: flex-start; padding: 0.6rem 0; border-bottom: 1px solid #111; }
.event-row:last-child { border-bottom: none; }
.event-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; margin-top: 5px; }
.event-body { flex: 1; min-width: 0; }
.event-msg { font-size: 0.82rem; word-break: break-word; }
.event-meta { font-size: 0.68rem; color: #555; margin-top: 0.2rem; display: flex; gap: 0.6rem; flex-wrap: wrap; }
.event-detail { font-size: 0.72rem; color: #555; margin-top: 0.25rem; font-family: "SF Mono","Menlo",monospace; word-break: break-all; cursor: pointer; }
.event-detail-expanded { display: none; background: #0d0d0d; border-radius: 4px; padding: 0.4rem 0.6rem; margin-top: 0.2rem; white-space: pre-wrap; }
.event-detail.expanded .event-detail-expanded { display: block; }
.dot-passed { background: #7dda93; }
.dot-failed { background: #f87171; }
.dot-started { background: #c8a2ff; }
.dot-done { background: #7dda93; }
.dot-canceled { background: #555; }
.dot-blocked { background: #f0c060; }
.dot-catalog { background: #6db3f2; }
.dot-default { background: #444; }
.pager { display: flex; gap: 0.5rem; align-items: center; margin-top: 1rem; font-size: 0.78rem; color: #555; }
.pager-btn { padding: 0.3rem 0.6rem; border: 1px solid #222; background: #111; color: #888; border-radius: 4px; cursor: pointer; font-size: 0.75rem; }
.pager-btn:disabled { opacity: 0.3; cursor: not-allowed; }
.pager-btn:not(:disabled):hover { background: #1a1a1a; }
`

const procualdashHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta http-equiv="Cache-Control" content="no-store">
<title>Procula</title>
<link rel="stylesheet" href="/procula/static/procula.css">
</head>
<body>

<div class="masthead">
  <div class="masthead-inner">
    <div>
      <div class="logo">P<span class="accent">R</span>OCULA</div>
      <div class="logo-sub">Processing Pipeline</div>
    </div>
    <a class="back-link" href="/">&larr; Dashboard</a>
  </div>
</div>

<div class="main">

  <!-- Status -->
  <div class="section">
    <div class="section-header"><span>Status</span></div>
    <div class="status-grid" id="status-grid">
      <div class="stat-box"><div class="stat-label">Health</div><div class="stat-value" id="s-health">—</div></div>
      <div class="stat-box"><div class="stat-label">Queued</div><div class="stat-value" id="s-queued">—</div></div>
      <div class="stat-box"><div class="stat-label">Processing</div><div class="stat-value" id="s-processing">—</div></div>
      <div class="stat-box"><div class="stat-label">Completed</div><div class="stat-value" id="s-completed">—</div></div>
      <div class="stat-box"><div class="stat-label">Failed</div><div class="stat-value" id="s-failed">—</div></div>
    </div>
  </div>

  <!-- Tabs -->
  <div class="tabs">
    <button class="tab-btn active" onclick="switchTab('settings')">Settings</button>
    <button class="tab-btn" onclick="switchTab('jobs')">Jobs</button>
    <button class="tab-btn" onclick="switchTab('events')">Event Log</button>
  </div>

  <!-- Settings tab -->
  <div class="tab-pane active" id="tab-settings">

    <!-- Pipeline settings -->
    <div class="section">
      <div class="section-header"><span>Pipeline</span></div>
      <div class="card">
        <div class="toggle-row">
          <div>
            <div class="toggle-label">Validation</div>
            <div class="toggle-desc">FFprobe integrity check, duration sanity, sample detection</div>
          </div>
          <label class="toggle">
            <input type="checkbox" id="opt-validation">
            <div class="toggle-track"></div>
            <div class="toggle-thumb"></div>
          </label>
        </div>
        <div class="toggle-row">
          <div>
            <div class="toggle-label">Transcoding</div>
            <div class="toggle-desc">Re-encode using profiles in /config/procula/profiles/</div>
          </div>
          <label class="toggle">
            <input type="checkbox" id="opt-transcoding">
            <div class="toggle-track"></div>
            <div class="toggle-thumb"></div>
          </label>
        </div>
        <div class="toggle-row">
          <div>
            <div class="toggle-label">Cataloging</div>
            <div class="toggle-desc">Trigger Jellyfin library refresh and write notification on completion</div>
          </div>
          <label class="toggle">
            <input type="checkbox" id="opt-catalog">
            <div class="toggle-track"></div>
            <div class="toggle-thumb"></div>
          </label>
        </div>
      </div>
    </div>

    <!-- Dual Subtitles -->
    <div class="section">
      <div class="section-header"><span>Dual Subtitles</span></div>
      <div class="card">
        <div class="toggle-row">
          <div>
            <div class="toggle-label">Generate stacked subtitle tracks</div>
            <div class="toggle-desc">Produce .en-es.ass sidecars with two languages stacked top + bottom</div>
          </div>
          <label class="toggle">
            <input type="checkbox" id="opt-dualsub" onchange="updateDualsubExtras()">
            <div class="toggle-track"></div>
            <div class="toggle-thumb"></div>
          </label>
        </div>
        <div id="dualsub-extras" class="hidden">
          <div class="field-group">
            <label class="field-label" for="dualsub-pairs">Language pairs (one per line, e.g. en-es)</label>
            <textarea class="field-input" id="dualsub-pairs" placeholder="en-es&#10;en-de"></textarea>
          </div>
          <div class="field-group" style="margin-top:0.75rem">
            <div class="field-label">Translator (for missing language tracks)</div>
            <div class="radio-group">
              <label class="radio-opt"><input type="radio" name="dualsub-translator" value="none"><span>None — require existing subs</span></label>
              <label class="radio-opt"><input type="radio" name="dualsub-translator" value="argos"><span>Argos Translate (local, offline)</span></label>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Notifications -->
    <div class="section">
      <div class="section-header"><span>Notifications</span></div>
      <div class="card">
        <div class="field-label" style="margin-bottom:0.5rem">Mode</div>
        <div class="radio-group">
          <label class="radio-opt"><input type="radio" name="notif-mode" value="internal"><span>Internal only</span></label>
          <label class="radio-opt"><input type="radio" name="notif-mode" value="apprise"><span>Apprise</span></label>
          <label class="radio-opt"><input type="radio" name="notif-mode" value="direct"><span>Direct webhook</span></label>
        </div>
        <div class="notif-extra hidden" id="notif-apprise">
          <div class="field-group">
            <label class="field-label" for="apprise-urls">Apprise URLs (one per line)</label>
            <textarea class="field-input" id="apprise-urls" placeholder="ntfy://my-topic&#10;gotify://host/token"></textarea>
          </div>
        </div>
        <div class="notif-extra hidden" id="notif-direct">
          <div class="field-group">
            <label class="field-label" for="direct-url">Webhook URL</label>
            <input class="field-input" id="direct-url" type="url" placeholder="https://ntfy.sh/my-topic">
          </div>
        </div>
      </div>
    </div>

    <div class="save-bar">
      <button class="btn-save" id="btn-save" onclick="saveSettings()">Save settings</button>
      <span class="save-msg" id="save-msg"></span>
    </div>
  </div><!-- /tab-settings -->

  <!-- Jobs tab -->
  <div class="tab-pane" id="tab-jobs">
    <div class="section">
      <div class="section-header">
        <span>All Jobs</span>
        <span id="jobs-stats" style="font-size:0.7rem;color:#555"></span>
      </div>
      <div id="jobs-list"><div class="empty-state">Loading…</div></div>
    </div>
  </div>

  <!-- Event Log tab -->
  <div class="tab-pane" id="tab-events">
    <div class="section">
      <div class="section-header">
        <span>Event Log</span>
        <span id="events-total" style="font-size:0.7rem;color:#555"></span>
      </div>
      <div class="event-filters">
        <button class="filter-chip active" data-type="" onclick="setEventFilter(this,'')">All</button>
        <button class="filter-chip" data-type="validation_passed" onclick="setEventFilter(this,'validation_passed')">Validation ✓</button>
        <button class="filter-chip" data-type="validation_failed" onclick="setEventFilter(this,'validation_failed')">Validation ✗</button>
        <button class="filter-chip" data-type="transcode_started,transcode_done,transcode_failed" onclick="setEventFilter(this,'transcode_started,transcode_done,transcode_failed')">Transcode</button>
        <button class="filter-chip" data-type="catalog_refreshed" onclick="setEventFilter(this,'catalog_refreshed')">Catalog</button>
        <button class="filter-chip" data-type="job_cancelled,job_retried,release_blocklisted" onclick="setEventFilter(this,'job_cancelled,job_retried,release_blocklisted')">Actions</button>
      </div>
      <div id="events-list"><div class="empty-state">Loading…</div></div>
      <div class="pager">
        <button class="pager-btn" id="pager-prev" onclick="eventsPage(-1)" disabled>&larr; Prev</button>
        <span id="pager-info">—</span>
        <button class="pager-btn" id="pager-next" onclick="eventsPage(1)">Next &rarr;</button>
      </div>
    </div>
  </div>

  <div class="footer" id="footer"></div>
</div>

<!-- Job detail drawer -->
<div class="drawer-backdrop" id="drawer-backdrop" onclick="closeDrawer()"></div>
<div class="drawer" id="job-drawer">
  <div class="drawer-header">
    <div>
      <div class="drawer-title" id="drawer-title">—</div>
      <div id="drawer-subtitle" style="font-size:0.75rem;color:#555;margin-top:0.2rem"></div>
    </div>
    <button class="drawer-close" onclick="closeDrawer()">&times;</button>
  </div>
  <div class="drawer-body" id="drawer-body"></div>
</div>

<script>
// ── Helpers ──────────────────────────────────────────
function esc(s) {
    return String(s ?? '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}
function fmtTime(ts) {
    if (!ts) return '—';
    try { return new Date(ts).toLocaleString(undefined,{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit',second:'2-digit'}); }
    catch { return ts; }
}
function fmtDur(s) {
    if (!s) return '';
    s = Math.round(s);
    if (s < 60) return s + 's';
    return Math.floor(s/60) + 'm ' + (s%60) + 's';
}

// ── Tabs ─────────────────────────────────────────────
function switchTab(id) {
    document.querySelectorAll('.tab-pane').forEach(p => p.classList.remove('active'));
    document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
    document.getElementById('tab-' + id).classList.add('active');
    document.querySelectorAll('.tab-btn').forEach(b => { if (b.textContent.toLowerCase().startsWith(id.slice(0,4))) b.classList.add('active'); });
    if (id === 'jobs') loadJobs();
    if (id === 'events') loadEvents();
}

// Check for #job= fragment on load to open detail drawer
function checkFragment() {
    const hash = location.hash;
    if (hash.startsWith('#job=')) {
        const id = hash.slice(5);
        switchTab('jobs');
        loadJobs().then(() => openJobById(id));
    }
}

// ── Status ───────────────────────────────────────────
async function loadStatus() {
    try {
        const res = await fetch('/api/procula/status');
        if (!res.ok) { document.getElementById('s-health').textContent = 'down'; return; }
        const data = await res.json();
        const q = data.queue || {};
        document.getElementById('s-health').textContent = 'up';
        document.getElementById('s-health').classList.add('ok');
        document.getElementById('s-queued').textContent = q.queued ?? q.pending ?? '0';
        document.getElementById('s-processing').textContent = q.processing ?? '0';
        document.getElementById('s-completed').textContent = q.completed ?? '0';
        document.getElementById('s-failed').textContent = q.failed ?? '0';
    } catch {
        document.getElementById('s-health').textContent = 'err';
    }
}

// ── Settings ─────────────────────────────────────────
async function loadSettings() {
    try {
        const res = await fetch('/api/procula/settings');
        if (!res.ok) return;
        const s = await res.json();
        document.getElementById('opt-validation').checked = s.validation_enabled !== false;
        document.getElementById('opt-transcoding').checked = !!s.transcoding_enabled;
        document.getElementById('opt-catalog').checked = s.catalog_enabled !== false;
        document.getElementById('opt-dualsub').checked = !!s.dualsub_enabled;
        document.getElementById('dualsub-pairs').value = (s.dualsub_pairs || ['en-es']).join('\n');
        const translator = s.dualsub_translator || 'none';
        const tEl = document.querySelector('input[name="dualsub-translator"][value="' + translator + '"]');
        if (tEl) tEl.checked = true;
        updateDualsubExtras();
        const mode = s.notification_mode || 'internal';
        document.querySelector('input[name="notif-mode"][value="' + mode + '"]').checked = true;
        document.getElementById('apprise-urls').value = (s.apprise_urls || []).join('\n');
        document.getElementById('direct-url').value = s.direct_url || '';
        updateNotifExtras(mode);
    } catch {}
}

function updateDualsubExtras() {
    const on = document.getElementById('opt-dualsub').checked;
    document.getElementById('dualsub-extras').classList.toggle('hidden', !on);
}

function updateNotifExtras(mode) {
    document.getElementById('notif-apprise').classList.toggle('hidden', mode !== 'apprise');
    document.getElementById('notif-direct').classList.toggle('hidden', mode !== 'direct');
}

document.querySelectorAll('input[name="notif-mode"]').forEach(r => {
    r.addEventListener('change', () => updateNotifExtras(r.value));
});

async function saveSettings() {
    const btn = document.getElementById('btn-save');
    const msg = document.getElementById('save-msg');
    btn.disabled = true;
    msg.className = 'save-msg';
    const mode = document.querySelector('input[name="notif-mode"]:checked')?.value || 'internal';
    const appriseRaw = document.getElementById('apprise-urls').value.trim();
    const pairsRaw = document.getElementById('dualsub-pairs').value.trim();
    const translator = document.querySelector('input[name="dualsub-translator"]:checked')?.value || 'none';
    const s = {
        validation_enabled: document.getElementById('opt-validation').checked,
        transcoding_enabled: document.getElementById('opt-transcoding').checked,
        catalog_enabled: document.getElementById('opt-catalog').checked,
        dualsub_enabled: document.getElementById('opt-dualsub').checked,
        dualsub_pairs: pairsRaw ? pairsRaw.split('\n').map(p => p.trim()).filter(Boolean) : ['en-es'],
        dualsub_translator: translator,
        notification_mode: mode,
        apprise_urls: appriseRaw ? appriseRaw.split('\n').map(u => u.trim()).filter(Boolean) : [],
        direct_url: document.getElementById('direct-url').value.trim(),
    };
    try {
        const res = await fetch('/api/procula/settings', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(s)
        });
        if (res.ok) {
            msg.textContent = 'Saved'; msg.className = 'save-msg visible';
        } else {
            msg.textContent = 'Error saving'; msg.className = 'save-msg err visible';
        }
    } catch {
        msg.textContent = 'Network error'; msg.className = 'save-msg err visible';
    }
    btn.disabled = false;
    setTimeout(() => { msg.className = 'save-msg'; }, 3000);
}

// ── Jobs tab ─────────────────────────────────────────
let allJobs = [];

async function loadJobs() {
    const res = await fetch('/api/procula/jobs');
    if (!res.ok) { document.getElementById('jobs-list').innerHTML = '<div class="empty-state">Failed to load jobs</div>'; return; }
    allJobs = await res.json();
    // Sort newest first
    allJobs.sort((a,b) => new Date(b.created_at) - new Date(a.created_at));
    renderJobs();
}

function renderJobs() {
    const el = document.getElementById('jobs-list');
    const stats = document.getElementById('jobs-stats');
    if (!allJobs.length) { el.innerHTML = '<div class="empty-state">No jobs yet</div>'; stats.textContent = ''; return; }
    const counts = {};
    allJobs.forEach(j => { counts[j.state] = (counts[j.state]||0)+1; });
    stats.textContent = Object.entries(counts).map(([k,v])=>v+' '+k).join(' · ');
    el.innerHTML = '<table class="jobs-table"><thead><tr><th>Title</th><th>State</th><th>Stage</th><th>Created</th></tr></thead><tbody>' +
        allJobs.map(j => {
            const pct = Math.round((j.progress||0)*100);
            const fillClass = j.state==='completed'?'done':j.state==='failed'?'failed':'';
            return '<tr onclick="openJobById(\'' + esc(j.id) + '\')">' +
                '<td><span class="job-title">' + esc(j.source?.title || j.id) + '</span>' + (j.source?.year ? '<span class="job-year">'+j.source.year+'</span>' : '') +
                '<div class="job-progress-bar"><div class="job-progress-fill ' + fillClass + '" style="width:'+pct+'%"></div></div></td>' +
                '<td><span class="job-state-badge state-'+esc(j.state)+'">' + esc(j.state) + '</span></td>' +
                '<td style="color:#888;font-size:0.78rem">' + esc(j.stage||'—') + '</td>' +
                '<td style="color:#555;font-size:0.72rem;white-space:nowrap">' + fmtTime(j.created_at) + '</td>' +
                '</tr>';
        }).join('') + '</tbody></table>';
}

function openJobById(id) {
    const job = allJobs.find(j => j.id === id);
    if (job) openDrawer(job);
}

// ── Job drawer ───────────────────────────────────────
function openDrawer(j) {
    document.getElementById('drawer-title').textContent = (j.source?.title || j.id) + (j.source?.year ? ' ('+j.source.year+')' : '');
    document.getElementById('drawer-subtitle').textContent = j.id + ' · ' + esc(j.state) + ' · ' + esc(j.stage||'—');
    const body = document.getElementById('drawer-body');
    let html = '';

    // Validation
    if (j.validation) {
        const v = j.validation;
        const checks = v.checks || {};
        html += '<div class="drawer-section"><div class="drawer-section-label">Validation ' + (v.passed ? '✓ passed' : '✗ failed') + '</div>';
        html += '<div class="check-grid">';
        ['integrity','duration','sample'].forEach(k => {
            const val = checks[k] || 'pending';
            const cls = val === 'pass' ? 'check-pass' : val === 'fail' ? 'check-fail' : val === 'warn' ? 'check-warn' : val === 'skip' ? 'check-skip' : 'check-pending';
            html += '<div class="check-item"><div class="check-name">' + k + '</div><div class="check-val ' + cls + '">' + val + '</div></div>';
        });
        html += '</div>';
        if (checks.codecs) {
            const c = checks.codecs;
            html += '<div class="codec-row">';
            if (c.video) html += '<span class="codec-item">video: ' + esc(c.video) + (c.width ? ' ' + c.width + 'x' + c.height : '') + '</span>';
            if (c.audio) html += '<span class="codec-item">audio: ' + esc(c.audio) + '</span>';
            if (c.subtitles && c.subtitles.length) html += '<span class="codec-item">subs: ' + c.subtitles.map(esc).join(', ') + '</span>';
            html += '</div>';
        }
        html += '</div>';
    }

    // Missing subs
    if (j.missing_subs && j.missing_subs.length) {
        html += '<div class="drawer-section"><div class="drawer-section-label">Missing subtitles</div>';
        html += '<div class="codec-row">' + j.missing_subs.map(s => '<span class="codec-item" style="color:#f0c060">'+esc(s)+'</span>').join('') + '</div></div>';
    }

    // Transcode
    if (j.dualsub_outputs && j.dualsub_outputs.length) {
        html += '<div class="drawer-section"><div class="drawer-section-label">Dual Subtitles</div>';
        html += '<div class="output-list">' + j.dualsub_outputs.map(o => '<div class="output-item">' + esc(o) + '</div>').join('') + '</div>';
        html += '</div>';
    } else if (j.dualsub_error) {
        html += '<div class="drawer-section"><div class="drawer-section-label">Dual Subtitles</div><div class="error-box">' + esc(j.dualsub_error) + '</div></div>';
    }

    if (j.transcode_profile || j.transcode_decision) {
        html += '<div class="drawer-section"><div class="drawer-section-label">Transcoding</div>';
        html += '<div class="check-grid">';
        if (j.transcode_profile) html += '<div class="check-item"><div class="check-name">profile</div><div class="check-val" style="color:#c8a2ff;font-size:0.82rem">' + esc(j.transcode_profile) + '</div></div>';
        if (j.transcode_decision) {
            const dc = j.transcode_decision==='transcoded'?'check-pass':j.transcode_decision==='failed'?'check-fail':'check-skip';
            html += '<div class="check-item"><div class="check-name">decision</div><div class="check-val ' + dc + '">' + esc(j.transcode_decision) + '</div></div>';
        }
        html += '</div>';
        if (j.transcode_outputs && j.transcode_outputs.length) {
            html += '<div class="output-list">' + j.transcode_outputs.map(o => '<div class="output-item">' + esc(o) + '</div>').join('') + '</div>';
        }
        if (j.transcode_error) html += '<div class="error-box">' + esc(j.transcode_error) + '</div>';
        html += '</div>';
    }

    // Error
    if (j.error && !j.transcode_error) {
        html += '<div class="drawer-section"><div class="drawer-section-label">Error</div><div class="error-box">' + esc(j.error) + '</div></div>';
    }

    // Source path
    if (j.source && j.source.path) {
        html += '<div class="drawer-section"><div class="drawer-section-label">Source</div>';
        html += '<div class="output-item">' + esc(j.source.path) + '</div>';
        if (j.source.size) html += '<div style="font-size:0.72rem;color:#555;margin-top:0.2rem">' + fmtBytes(j.source.size) + '</div>';
        html += '</div>';
    }

    // Timestamps
    html += '<div class="drawer-section"><div class="drawer-section-label">Timeline</div>';
    html += '<div style="font-size:0.78rem;color:#666">Created: ' + fmtTime(j.created_at) + '<br>Updated: ' + fmtTime(j.updated_at);
    if (j.retry_count) html += '<br>Retries: ' + j.retry_count;
    html += '</div></div>';

    // Actions
    html += '<div class="drawer-actions">';
    if (j.state === 'failed' || j.state === 'cancelled') {
        html += '<button class="btn-action" onclick="retryJobDrawer(\'' + esc(j.id) + '\')">&#8635; Retry</button>';
    }
    if (j.state === 'queued' || j.state === 'processing' || j.state === 'failed') {
        html += '<button class="btn-action danger" onclick="cancelJobDrawer(\'' + esc(j.id) + '\')">Cancel</button>';
    }
    html += '</div>';

    body.innerHTML = html;
    document.getElementById('drawer-backdrop').classList.add('open');
    document.getElementById('job-drawer').classList.add('open');
}

function closeDrawer() {
    document.getElementById('drawer-backdrop').classList.remove('open');
    document.getElementById('job-drawer').classList.remove('open');
}

async function retryJobDrawer(id) {
    await fetch('/api/procula/jobs/' + id + '/retry', {method:'POST'});
    closeDrawer();
    loadJobs();
}

async function cancelJobDrawer(id) {
    await fetch('/api/procula/jobs/' + id + '/cancel', {method:'POST'});
    closeDrawer();
    loadJobs();
}

function fmtBytes(b) {
    if (!b) return '';
    const units = ['B','KB','MB','GB','TB'];
    let i = 0;
    while (b >= 1024 && i < units.length-1) { b /= 1024; i++; }
    return b.toFixed(1) + ' ' + units[i];
}

// ── Event Log tab ─────────────────────────────────────
let eventsOffset = 0;
const eventsLimit = 50;
let eventsTypeFilter = '';
let eventsTotal = 0;

function setEventFilter(btn, type) {
    document.querySelectorAll('.filter-chip').forEach(c => c.classList.remove('active'));
    btn.classList.add('active');
    // multi-type filters are comma-separated; API only accepts one type at a time
    // for combined filters we fetch all and filter client-side
    eventsTypeFilter = type;
    eventsOffset = 0;
    loadEvents();
}

async function loadEvents() {
    const el = document.getElementById('events-list');
    el.innerHTML = '<div class="empty-state">Loading…</div>';

    // For multi-type filters (comma-separated), fetch all without type param and filter client-side
    const types = eventsTypeFilter ? eventsTypeFilter.split(',') : [];
    const singleType = types.length === 1 ? types[0] : '';
    const params = new URLSearchParams({limit: eventsLimit, offset: eventsOffset});
    if (singleType) params.set('type', singleType);

    const res = await fetch('/api/procula/events?' + params);
    if (!res.ok) { el.innerHTML = '<div class="empty-state">Failed to load events</div>'; return; }
    const data = await res.json();
    let events = data.events || [];

    // Client-side filter for multi-type chips
    if (types.length > 1) {
        events = events.filter(e => types.includes(e.type));
    }

    eventsTotal = data.total || 0;
    document.getElementById('events-total').textContent = eventsTotal + ' total';
    updatePager();

    if (!events.length) { el.innerHTML = '<div class="empty-state">No events</div>'; return; }

    el.innerHTML = events.map(e => {
        const dotClass = {
            validation_passed:'dot-passed', validation_failed:'dot-failed',
            transcode_started:'dot-started', transcode_done:'dot-done', transcode_failed:'dot-failed',
            catalog_refreshed:'dot-catalog',
            job_cancelled:'dot-canceled', job_retried:'dot-started', release_blocklisted:'dot-blocked'
        }[e.type] || 'dot-default';

        const detailStr = e.details ? JSON.stringify(e.details, null, 2) : '';
        const durStr = e.duration_s ? ' · ' + fmtDur(e.duration_s) : '';

        return '<div class="event-row">' +
            '<div class="event-dot ' + dotClass + '"></div>' +
            '<div class="event-body">' +
            '<div class="event-msg">' + esc(e.message || e.type) + '</div>' +
            '<div class="event-meta">' +
                '<span>' + fmtTime(e.timestamp) + '</span>' +
                '<span>' + esc(e.type) + '</span>' +
                (e.title ? '<span>' + esc(e.title) + (e.year?' ('+e.year+')':'') + '</span>' : '') +
                (durStr ? '<span>' + durStr.slice(3) + '</span>' : '') +
            '</div>' +
            (detailStr ? '<div class="event-detail" onclick="toggleDetail(this)">' +
                '<span style="color:#444">&#9654; details</span>' +
                '<div class="event-detail-expanded">' + esc(detailStr) + '</div>' +
            '</div>' : '') +
            '</div>' +
        '</div>';
    }).join('');
}

function toggleDetail(el) {
    el.classList.toggle('expanded');
}

function eventsPage(dir) {
    const newOffset = eventsOffset + dir * eventsLimit;
    if (newOffset < 0) return;
    eventsOffset = newOffset;
    loadEvents();
}

function updatePager() {
    const prev = document.getElementById('pager-prev');
    const next = document.getElementById('pager-next');
    const info = document.getElementById('pager-info');
    const start = eventsOffset + 1;
    const end = Math.min(eventsOffset + eventsLimit, eventsTotal);
    info.textContent = eventsTotal > 0 ? start + '–' + end + ' of ' + eventsTotal : '—';
    prev.disabled = eventsOffset === 0;
    next.disabled = eventsOffset + eventsLimit >= eventsTotal;
}

// ── Init ─────────────────────────────────────────────
document.getElementById('footer').textContent = new Date().toLocaleTimeString();
loadStatus();
loadSettings();
checkFragment();
setInterval(loadStatus, 15000);
setInterval(() => {
    if (document.getElementById('tab-jobs').classList.contains('active')) loadJobs();
    if (document.getElementById('tab-events').classList.contains('active')) loadEvents();
}, 15000);
</script>
</body>
</html>`
