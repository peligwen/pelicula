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
.masthead-inner { max-width: 720px; margin: 0 auto; display: flex; align-items: flex-end; justify-content: space-between; }
.logo { text-decoration: none; font-family: "Helvetica Neue", Helvetica, Arial, sans-serif; font-weight: 700; font-size: 2rem; letter-spacing: 0.35em; text-transform: uppercase; color: #fff; }
.logo .accent { color: #c8a2ff; }
.logo-sub { font-size: 0.55rem; letter-spacing: 0.5em; text-transform: uppercase; color: #555; margin-top: 0.3rem; }
.back-link { font-size: 0.75rem; color: #555; text-decoration: none; letter-spacing: 0.05em; }
.back-link:hover { color: #888; }
.main { max-width: 720px; margin: 0 auto; padding: 1.5rem 2rem 2rem; }
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

  <div class="footer" id="footer"></div>
</div>

<script>
async function loadStatus() {
    try {
        const res = await fetch('/api/procula/status');
        if (!res.ok) { document.getElementById('s-health').textContent = 'down'; return; }
        const data = await res.json();
        const q = data.queue || {};
        document.getElementById('s-health').textContent = 'up';
        document.getElementById('s-health').classList.add('ok');
        document.getElementById('s-queued').textContent = q.pending ?? '0';
        document.getElementById('s-processing').textContent = q.processing ?? '0';
        document.getElementById('s-completed').textContent = q.completed ?? '0';
        document.getElementById('s-failed').textContent = q.failed ?? '0';
    } catch {
        document.getElementById('s-health').textContent = 'err';
    }
}

async function loadSettings() {
    try {
        const res = await fetch('/api/procula/settings');
        if (!res.ok) return;
        const s = await res.json();
        document.getElementById('opt-validation').checked = s.validation_enabled !== false;
        document.getElementById('opt-transcoding').checked = !!s.transcoding_enabled;
        document.getElementById('opt-catalog').checked = s.catalog_enabled !== false;
        const mode = s.notification_mode || 'internal';
        document.querySelector('input[name="notif-mode"][value="' + mode + '"]').checked = true;
        document.getElementById('apprise-urls').value = (s.apprise_urls || []).join('\n');
        document.getElementById('direct-url').value = s.direct_url || '';
        updateNotifExtras(mode);
    } catch {}
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
    const s = {
        validation_enabled: document.getElementById('opt-validation').checked,
        transcoding_enabled: document.getElementById('opt-transcoding').checked,
        catalog_enabled: document.getElementById('opt-catalog').checked,
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

document.getElementById('footer').textContent = new Date().toLocaleTimeString();
loadStatus();
loadSettings();
</script>
</body>
</html>`
