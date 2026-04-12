// ── App store ────────────────────────────
// Initialised here; framework.js must be loaded first.
const store = PeliculaFW.initStore({
    role: 'admin',        // 'admin' | 'manager' | 'viewer'
    username: '',
});
// ── Resilient fetch (auto-abort after ms) ──
function tfetch(url, opts, ms) {
    ms = ms || 4000;
    const ctrl = new AbortController();
    const t = setTimeout(() => ctrl.abort(), ms);
    return fetch(url, Object.assign({}, opts, {signal: ctrl.signal})).finally(() => clearTimeout(t));
}

// ── Auth ──────────────────────────────────

async function checkAuth() {
    try {
        const res = await tfetch('/api/pelicula/auth/check');
        const data = await res.json();
        if (!data.valid) {
            document.getElementById('login-overlay').classList.remove('hidden');
        } else {
            applyRole(data.role || 'admin', data.username || '');
        }
    } catch {
        // Network error — default to locked state rather than granting admin
        document.getElementById('login-overlay').classList.remove('hidden');
    }
}

async function doLogin() {
    const username = document.getElementById('login-username').value;
    const pw = document.getElementById('login-password').value;
    const errEl = document.getElementById('login-error');
    try {
        const res = await fetch('/api/pelicula/auth/login', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({username, password: pw})
        });
        const data = await res.json();
        if (res.ok) {
            document.getElementById('login-overlay').classList.add('hidden');
            errEl.style.display = 'none';
            applyRole(data.role || 'admin', data.username || '');
            refresh();
        } else {
            errEl.style.display = 'block';
        }
    } catch { errEl.style.display = 'block'; }
}

// Apply role-based visibility to UI elements.
// viewer:  no search, no download actions
// manager: search + add, pause/resume; no cancel/blocklist
// admin:   everything
function applyRole(role, username) {
    store.set('role', role);
    store.set('username', username || '');
    document.body.dataset.username = username || '';
    const isManager = role === 'manager' || role === 'admin';
    const isAdmin = role === 'admin';

    // User badge
    const badge = document.getElementById('user-badge');
    if (badge) {
        if (username) { badge.textContent = username; badge.classList.remove('hidden'); }
        else { badge.classList.add('hidden'); }
    }

    // Search section
    const searchSection = document.querySelector('.search-section');
    if (searchSection) searchSection.style.display = isManager ? '' : 'none';

    // Admin-only elements (e.g. settings gear icon)
    document.querySelectorAll('.admin-only').forEach(el => {
        el.style.display = isAdmin ? '' : 'none';
    });

    // Requests section: visible to all authenticated users
    // State flags are owned by users.js; accessed via window accessors.
    const requestsSection = document.getElementById('requests-section');
    if (requestsSection) {
        requestsSection.classList.remove('hidden');
        if (window._users_getRequestsLoaded && !window._users_getRequestsLoaded()) {
            if (window.loadRequests) window.loadRequests();
            if (window._users_setRequestsLoaded) window._users_setRequestsLoaded(true);
        }
        // loadArrMeta's own guard (in settings.js) prevents duplicate loads
        if (isAdmin && window.loadArrMeta) window.loadArrMeta();
    }

    // Users section: visible to admins
    const usersSection = document.getElementById('users-section');
    if (usersSection) {
        if (isAdmin) {
            usersSection.classList.remove('hidden');
            if (window._users_getUsersLoaded && !window._users_getUsersLoaded()) {
                if (window.loadUsers) window.loadUsers();
                if (window.loadInvites) window.loadInvites();
                if (window._users_setUsersLoaded) window._users_setUsersLoaded(true);
            }
        } else {
            usersSection.classList.add('hidden');
        }
    }

    // Store role on body for use by dynamically rendered download/pipeline cards
    document.body.dataset.role = role;

    // Start SSE connection — guard lives inside sse.js (_started flag)
    if (window.connectSSE) window.connectSSE();
}

document.getElementById('login-form').addEventListener('submit', e => { e.preventDefault(); doLogin(); });

// ── Status + Indexer check ────────────────
async function checkStatus() {
    try {
        const res = await tfetch('/api/pelicula/status');
        if (!res.ok) return;
        const data = await res.json();
        const statusBar = document.getElementById('indexer-status');
        const hint = document.getElementById('footer-hint');
        if (data.indexers === 0) {
            if (statusBar) statusBar.classList.add('visible');
            if (hint) hint.textContent = 'Prowlarr needs an indexer';
        } else {
            if (statusBar) statusBar.classList.remove('visible');
            if (hint) hint.textContent = '';
        }
    } catch (e) { console.warn('[pelicula] error:', e); }
}

// Search code is in search.js (PeliculaFW component 'search').
// Downloads code is in downloads.js (PeliculaFW component 'downloads').
// Both are mounted below after DOMContentLoaded.

// data-* bridge helpers for pipeline cards — keep user-controlled strings out of JS string literals in onclick
// dlPauseFromBtn, dlCancelFromBtn, openBlocklistFromBtn are defined in downloads.js.
function retryFromBtn(btn) { retryJob(btn.dataset.jobId); }
function formatSpeed(bps) { if (bps > 1048576) return (bps/1048576).toFixed(1)+' MB/s'; if (bps > 1024) return (bps/1024).toFixed(0)+' KB/s'; if (bps > 0) return bps+' B/s'; return 'idle'; }
function formatSize(b) { if (!b) return '0 B'; const u=['B','KB','MB','GB','TB']; let i=0,n=b; while(n>=1024&&i<u.length-1){n/=1024;i++;} return n.toFixed(1)+' '+u[i]; }
function formatETA(s) { if (s > 86400) return Math.floor(s/86400)+'d'; if (s > 3600) return Math.floor(s/3600)+'h '+Math.floor((s%3600)/60)+'m'; if (s > 60) return Math.floor(s/60)+'m'; return s+'s'; }

// updatePanelAlert(), _panelVPNDegraded — moved to services.js.

// ── Side panel collapse state ─────────────
// Collapse state is persisted under this localStorage key. When no preference
// is stored, mobile viewports default to collapsed and desktops default to open.
const _SIDE_COLLAPSED_KEY = 'pelicula_side_collapsed';
const _SIDE_MOBILE_MAX = 768;

function _isMobileViewport() {
    return window.innerWidth <= _SIDE_MOBILE_MAX;
}

function setSidePanelCollapsed(collapsed) {
    document.body.classList.toggle('side-collapsed', !!collapsed);
    try { localStorage.setItem(_SIDE_COLLAPSED_KEY, collapsed ? '1' : '0'); } catch (e) {}
}

function toggleSidePanel() {
    setSidePanelCollapsed(!document.body.classList.contains('side-collapsed'));
}

function initSidePanelState() {
    let stored = null;
    try { stored = localStorage.getItem(_SIDE_COLLAPSED_KEY); } catch (e) {}
    if (stored === '1') { setSidePanelCollapsed(true); return; }
    if (stored === '0') { setSidePanelCollapsed(false); return; }
    // No preference — default based on viewport.
    setSidePanelCollapsed(_isMobileViewport());
}

// Strip click opens the panel.
document.addEventListener('DOMContentLoaded', () => {
    initSidePanelState();
    const strip = document.getElementById('side-strip');
    if (strip) {
        strip.addEventListener('click', (e) => {
            e.stopPropagation();
            setSidePanelCollapsed(false);
        });
    }
});

// Click-outside-to-close: only on mobile, only when panel is currently open.
document.addEventListener('click', (e) => {
    if (!_isMobileViewport()) return;
    if (document.body.classList.contains('side-collapsed')) return;
    // Don't collapse while a modal is open — modal clicks are outside .pane-side
    // but should not trigger the side panel's tap-outside behavior.
    if (document.querySelector('.modal-overlay:not(.hidden)')) return;
    const paneSide = document.querySelector('.pane-side');
    if (!paneSide || paneSide.contains(e.target)) return;
    const strip = document.getElementById('side-strip');
    if (strip && strip.contains(e.target)) return;
    setSidePanelCollapsed(true);
});

// checkServices, checkVPN, checkHost, updateTimestamp, manualRefreshServices,
// toggleStackMenu, stackRestart, showServiceLogs, closeLogModal,
// refreshServiceLogs, copyServiceLogs, runSpeedTest — moved to services.js.
// ── Notifications bell ────────────────────
// renderNotifications, toggleNotifications, notifIcon, notifClass, formatNotifTime,
// dismissNotification, clearAllNotifications — all live in notifications.js (PeliculaFW component).

async function checkNotifications() {
    try {
        const res = await tfetch('/api/pelicula/notifications');
        if (!res.ok) return;
        const events = await res.json();
        renderNotifications(events);
        renderActivity(events);
    } catch (e) { console.warn('[pelicula] error:', e); }
}

// ── Activity feed ─────────────────────────
function renderActivity(events) {
    const section = document.getElementById('activity-section');
    const list = document.getElementById('activity-list');
    if (!section || !list) return;
    if (!Array.isArray(events) || !events.length) {
        list.innerHTML = html`<div class="activity-empty">No recent activity yet.</div>`.str;
        return;
    }
    list.innerHTML = events.slice(0, 15).map(e => {
        const icon = notifIcon(e.type);
        const cls = notifClass(e.type);
        const time = formatNotifTime(e.timestamp);
        return html`<div class="activity-item ${cls}">
            <span class="activity-icon">${raw(icon)}</span>
            <span class="activity-msg">${e.message}</span>
            <span class="activity-time">${time}</span>
        </div>`.str;
    }).join('');
}

// ── Storage Management ────────────────────
async function checkStorage() {
    try {
        const res = await tfetch('/api/pelicula/storage');
        if (!res.ok) return;
        const data = await res.json();
        const filesystems = Array.isArray(data.filesystems) ? data.filesystems : [];
        if (!filesystems.length) return;
        document.getElementById('storage-section').classList.remove('hidden');
        renderStorage(data);
        renderStorageMetrics(data);
        renderStorageFolders(data);
        renderStorageTimestamp(data.timestamp);
    } catch (e) { console.warn('[pelicula] storage error:', e); }
}

// Load threshold settings into the Settings lane (admin only, best-effort)
async function loadStorageSettings() {
    try {
        const res = await tfetch('/api/pelicula/procula-settings');
        if (!res.ok) return;
        const cfg = await res.json();
        const warnEl = document.getElementById('sm-warn-pct');
        const critEl = document.getElementById('sm-crit-pct');
        if (warnEl && cfg.storage_warning_pct) warnEl.value = cfg.storage_warning_pct;
        if (critEl && cfg.storage_critical_pct) critEl.value = cfg.storage_critical_pct;
        if (warnEl) warnEl.addEventListener('change', saveStorageThreshold);
        if (critEl) critEl.addEventListener('change', saveStorageThreshold);
    } catch (e) { /* non-admin: silently skip */ }
}

async function saveStorageThreshold() {
    const warn = parseInt(document.getElementById('sm-warn-pct')?.value, 10);
    const crit = parseInt(document.getElementById('sm-crit-pct')?.value, 10);
    if (isNaN(warn) || isNaN(crit)) return;
    try {
        const res = await tfetch('/api/pelicula/procula-settings', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ storage_warning_pct: warn, storage_critical_pct: crit })
        });
        if (res.ok) {
            ['sm-warn-pct', 'sm-crit-pct'].forEach(id => {
                const el = document.getElementById(id);
                if (!el) return;
                el.classList.add('saved');
                setTimeout(() => el.classList.remove('saved'), 1200);
            });
        }
    } catch (e) { console.warn('[pelicula] save storage threshold error:', e); }
}

async function scanStorageNow() {
    const btn = document.getElementById('storage-scan-btn');
    if (btn) { btn.disabled = true; btn.textContent = 'Scanning\u2026'; }
    try {
        await tfetch('/api/pelicula/storage/scan', { method: 'POST' });
        await checkStorage();
    } catch (e) { console.warn('[pelicula] scan error:', e); }
    if (btn) { btn.disabled = false; btn.textContent = 'Scan now'; }
}

function folderColor(label) {
    const map = { downloads: '#7dda93', movies: '#7080e8', tv: '#40c8a8', processing: '#f0c060' };
    return map[(label || '').toLowerCase()] || '#888';
}

function renderStorageMetrics(data) {
    const filesystems = data.filesystems || [];
    let free = 0, pelicula = 0, allKnown = true;
    let hasCrit = false, hasWarn = false;
    for (const fs of filesystems) {
        free += fs.available || 0;
        for (const f of (fs.folders || [])) {
            if (f.size < 0) allKnown = false;
            else pelicula += f.size;
        }
        if (fs.status === 'critical') hasCrit = true;
        else if (fs.status === 'warning') hasWarn = true;
    }
    const freeEl = document.getElementById('sm-metric-free');
    const pelEl = document.getElementById('sm-metric-pelicula');
    const statEl = document.getElementById('sm-metric-status');
    if (freeEl) freeEl.textContent = formatSize(free);
    if (pelEl) pelEl.textContent = allKnown ? formatSize(pelicula) : 'Calculating\u2026';
    if (statEl) {
        if (hasCrit) { statEl.textContent = 'Critical'; statEl.className = 'um-metric-num sm-status-critical'; }
        else if (hasWarn) { statEl.textContent = 'Warning'; statEl.className = 'um-metric-num sm-status-warning'; }
        else { statEl.textContent = 'Healthy'; statEl.className = 'um-metric-num sm-status-ok'; }
    }
}

function renderStorageFolders(data) {
    const el = document.getElementById('sm-folder-list');
    if (!el) return;
    const filesystems = data.filesystems || [];
    // Aggregate across filesystems by label
    const totals = {};
    let grandTotal = 0;
    for (const fs of filesystems) {
        for (const f of (fs.folders || [])) {
            if (f.size < 0) continue;
            totals[f.label] = (totals[f.label] || 0) + f.size;
            grandTotal += f.size;
        }
    }
    if (!Object.keys(totals).length) { el.innerHTML = html`<div class="sm-last-scan">No data yet</div>`.str; return; }
    el.innerHTML = Object.entries(totals).sort((a,b) => b[1]-a[1]).map(([label, size]) => {
        const pct = grandTotal > 0 ? (size / grandTotal * 100).toFixed(0) : 0;
        const color = folderColor(label);
        return html`<div class="sm-folder-row">
            <div class="sm-folder-dot" style="background:${color}"></div>
            <div class="sm-folder-label">${label}</div>
            <div class="sm-folder-size">${formatSize(size)}</div>
            <div class="sm-folder-pct">${pct}%</div>
        </div>`.str;
    }).join('');
}

function renderStorageTimestamp(ts) {
    const el = document.getElementById('sm-last-scan');
    if (!el || !ts) return;
    const d = new Date(ts);
    const diffMin = Math.round((Date.now() - d.getTime()) / 60000);
    el.textContent = diffMin < 2 ? 'just now' : diffMin < 60 ? diffMin + ' min ago' : d.toLocaleTimeString();
}

function renderStorage(data) {
    const list = document.getElementById('storage-list');
    if (!list) return;
    const filesystems = Array.isArray(data.filesystems) ? data.filesystems : [];

    list.innerHTML = filesystems.map(fs => {
        const pct = Math.round(fs.used_pct || 0);
        const folders = Array.isArray(fs.folders) ? fs.folders : [];
        const diskLabel = folders.map(f => f.label).join(', ') || fs.fs_id;

        let oursTotal = 0, allKnown = true;
        for (const f of folders) {
            if (f.size < 0) allKnown = false;
            else oursTotal += f.size;
        }
        const otherUsed = Math.max(0, fs.used - oursTotal);

        const folderSegs = fs.total > 0 ? folders.map(f => {
            if (f.size < 0) return '';
            const w = (f.size / fs.total * 100).toFixed(2);
            return html`<div class="storage-seg" style="width:${w}%;background:${folderColor(f.label)}"></div>`.str;
        }).join('') : '';
        const otherW = fs.total > 0 ? Math.max(0, otherUsed / fs.total * 100).toFixed(2) : 0;
        const otherSeg = otherW > 0
            ? html`<div class="storage-seg storage-seg-other" style="width:${otherW}%"></div>`.str : '';

        const showFolders = folders.length > 1;
        const folderRows = folders.map(f => {
            const folderPct = (fs.total > 0 && f.size >= 0)
                ? (f.size / fs.total * 100).toFixed(2) : 0;
            const sizeText = f.size < 0 ? 'Calculating\u2026' : formatSize(f.size);
            const color = folderColor(f.label);
            return html`<div class="storage-folder">
                <div class="storage-folder-header">
                    <span class="storage-folder-label" style="color:${color}">${f.label}</span>
                    <span class="storage-folder-size">${sizeText}</span>
                </div>
                <div class="download-bar-bg"><div class="download-bar storage-bar-folder" style="width:${folderPct}%;background:${color}"></div></div>
            </div>`.str;
        }).join('');

        const expandable = showFolders
            ? html`<div class="storage-folders collapsed">${raw(folderRows)}</div>`.str : '';
        const chevron = showFolders
            ? html`<span class="storage-chevron">&#9660;</span>`.str : '';
        const headerClick = showFolders ? ' onclick="toggleStorageDisk(this.parentElement)"' : '';
        const oursTotalText = allKnown ? formatSize(oursTotal) : 'Calculating\u2026';

        return html`<div class="download-item storage-disk">
            <div class="download-header"${raw(headerClick)}>
                <div class="download-name">${diskLabel}</div>
                <div class="download-actions">
                    <span class="dl-size">${formatSize(fs.used)} / ${formatSize(fs.total)}</span>
                    ${raw(chevron)}
                </div>
            </div>
            <div class="storage-stacked-bar">${raw(folderSegs)}${raw(otherSeg)}</div>
            <div class="download-meta">
                <span>Pelicula: ${oursTotalText}</span>
                <span>${formatSize(fs.available)} free · ${pct}%</span>
            </div>
            ${raw(expandable)}
        </div>`.str;
    }).join('');
}

function toggleStorageDisk(el) {
    const folders = el.querySelector('.storage-folders');
    const chevron = el.querySelector('.storage-chevron');
    if (!folders) return;
    const collapsed = folders.classList.toggle('collapsed');
    if (chevron) chevron.innerHTML = collapsed ? '&#9660;' : '&#9650;';
}

// ── Update checker ────────────────────────
async function checkUpdates() {
    try {
        const res = await fetch('/api/pelicula/updates');
        if (!res.ok) return;
        const data = await res.json();
        if (!data || typeof data !== 'object') return;
        const el = document.getElementById('footer-update');
        if (data.update_available && data.latest_version) {
            el.innerHTML = html`&#8593; Update available: <a href="https://github.com/peligwen/pelicula/releases" target="_blank" rel="noopener">${data.latest_version}</a> &nbsp;&bull;&nbsp;`.str;
        }
    } catch (e) { console.warn('[pelicula] updates error:', e); }
}

// ── Processing section ────────────────────
async function checkProcessing() {
    try {
        const res = await tfetch('/api/pelicula/processing');
        if (!res.ok) return;
        const data = await res.json();
        renderProcessing(data);
    } catch (e) { console.warn('[pelicula] error:', e); }
}

function renderProcessing(data) {
    const section = document.getElementById('processing-section');
    const activeEl = document.getElementById('processing-active');
    const failedSection = document.getElementById('processing-failed');
    const failedList = document.getElementById('processing-failed-list');
    const statsEl = document.getElementById('processing-stats');

    const jobs = Array.isArray(data.jobs) ? data.jobs : [];
    const activeJobs = jobs.filter(j => j.state === 'queued' || j.state === 'processing');
    const failedJobs = jobs.filter(j => j.state === 'failed');

    if (!activeJobs.length && !failedJobs.length) {
        section.style.display = 'none';
        return;
    }
    section.style.display = '';

    const status = data.status && data.status.queue ? data.status.queue : {};
    const processing = status.processing || 0;
    const pending = status.queued || status.pending || 0;
    const completed = status.completed || 0;
    statsEl.textContent = `${pending} queued / ${processing} active / ${completed} done`;

    activeEl.innerHTML = activeJobs.map(j => renderJobCard(j)).join('');

    if (failedJobs.length) {
        failedSection.style.display = '';
        failedList.innerHTML = failedJobs.map(j => renderJobCard(j)).join('');
    } else {
        failedSection.style.display = 'none';
    }
}

function renderJobCard(j) {
    const pct = Math.round((j.progress || 0) * 100);
    const stageName = {
        validate: 'Validating',
        catalog: 'Cataloging',
        await_subs: 'Acquiring subs',
        dualsub: 'Subtitles',
        process: 'Processing',
        done: 'Done'
    }[j.stage] || j.stage;
    const stateClass = j.state === 'failed' ? 'proc-failed' : 'proc-active';
    const barClass = j.state === 'failed' ? 'proc-bar-failed' : 'proc-bar-active';
    const title = j.source ? j.source.title : j.id;

    const retryBtn = j.state === 'failed'
        ? html`<button class="dl-btn resume" title="Retry" data-job-id="${j.id}" onclick="retryFromBtn(this)">&#8635;</button>`.str
        : '';
    const cancelBtn = (j.state === 'queued' || j.state === 'processing' || j.state === 'failed')
        ? html`<button class="dl-btn cancel" title="Cancel" data-job-id="${j.id}" onclick="cancelJobFromBtn(this)">&#x2715;</button>`.str
        : '';
    // Show re-search subs button on completed/failed jobs that have arr_type set
    const resubBtn = (j.state === 'done' || j.state === 'failed') && j.source?.arr_type
        ? html`<button class="dl-btn" title="Re-search subtitles" data-job-id="${j.id}" onclick="resubFromBtn(this)" style="font-size:0.7rem;padding:0.2rem 0.4rem">CC</button>`.str
        : '';
    const viewLogLink = html`<button class="dl-btn" onclick="openJobDrawer('${j.id}')" title="View details" style="font-size:0.7rem;padding:0.2rem 0.4rem">&#9654;</button>`.str;

    let subsBadge = '';
    if (j.stage === 'await_subs') {
        const waiting = (j.missing_subs || []).filter(l => !(j.subs_acquired || []).includes(l));
        if (waiting.length) {
            subsBadge = html`<span class="proc-badge proc-info" title="Waiting for Bazarr to deliver subtitles">Acquiring: ${waiting.join(', ')}</span>`.str;
        }
    } else if (j.subs_acquired && j.subs_acquired.length) {
        subsBadge = html`<span class="proc-badge proc-ok" title="Subtitles acquired by Bazarr">Subs: ${j.subs_acquired.join(', ')}</span>`.str;
    } else if (j.missing_subs && j.missing_subs.length) {
        subsBadge = html`<span class="proc-badge proc-warn" title="Bazarr will fetch these">Missing subs: ${j.missing_subs.join(', ')}</span>`.str;
    }

    let checksHTML = '';
    if (j.state === 'failed' && j.validation) {
        const checks = j.validation.checks || {};
        const checkOrder = ['integrity', 'duration', 'sample'];
        checksHTML = html`<div class="proc-check-list">${raw(checkOrder.map(k => {
            const v = checks[k] || 'skip';
            const cls = ['pass', 'fail', 'warn'].includes(v) ? v : 'skip';
            return html`<span class="proc-check proc-check-${cls}">${k}: ${v}</span>`.str;
        }).join(''))}</div>`.str;
    }

    let metaRight = '';
    if (j.transcode_profile) {
        metaRight = html`${j.transcode_profile}${j.transcode_decision ? ' · ' + j.transcode_decision : ''}`.str;
    } else if (j.transcode_eta > 0) {
        metaRight = `ETA ${Math.round(j.transcode_eta)}s`;
    }

    return html`<div class="download-item">
        <div class="download-header">
            <div class="download-name">${title}</div>
            <div class="download-actions">
                <span class="proc-badge ${stateClass}">${stageName}</span>
                ${raw(subsBadge)}
                ${raw(resubBtn)}${raw(retryBtn)}${raw(cancelBtn)}${raw(viewLogLink)}
            </div>
        </div>
        <div class="download-bar-bg"><div class="download-bar ${barClass}" style="width:${pct}%"></div></div>
        <div class="download-meta">
            <span>${pct}%${j.error ? raw(' — ' + html`${j.error}`.str) : ''}</span>
            <span>${raw(metaRight)}</span>
        </div>
        ${raw(checksHTML)}
    </div>`.str;
}

async function retryJob(id) {
    try {
        await fetch(`/api/procula/jobs/${id}/retry`, {method: 'POST'});
        setTimeout(checkPipeline, 500);
    } catch (e) { console.warn('[pelicula] retry error:', e); }
}

async function cancelJob(id) {
    try {
        await fetch(`/api/procula/jobs/${id}/cancel`, {method: 'POST'});
        setTimeout(checkPipeline, 500);
    } catch (e) { console.warn('[pelicula] cancel error:', e); }
}

function cancelJobFromBtn(btn) { cancelJob(btn.dataset.jobId); }

async function resubJob(id) {
    try {
        const res = await tfetch(`/api/pelicula/procula/jobs/${id}/resub`, {method: 'POST'});
        if (!res.ok) console.warn('[pelicula] resub failed:', res.status);
    } catch (e) { console.warn('[pelicula] resub error:', e); }
}

function resubFromBtn(btn) { resubJob(btn.dataset.jobId); }

// ── Pipeline board ────────────────────────
// Moved to pipeline.js (PeliculaFW component). checkPipeline() is on window.*.


let lastRefreshAt = 0;

async function refresh() {
    console.log('[pelicula] refresh start');
    const results = await Promise.allSettled([
        checkServices(), checkVPN(), checkPipeline(), checkStatus(),
        checkNotifications(), checkStorage(), loadSessions(), checkHost()
    ]);
    const failed = results.filter(r => r.status === 'rejected').length;
    console.log('[pelicula] refresh done' + (failed ? ' (' + failed + ' failed)' : ''));
    lastRefreshAt = Date.now();
    updateTimestamp();
    updateStaleBanner();
}

function updateStaleBanner() {
    if (!lastRefreshAt) return;
    const age = Date.now() - lastRefreshAt;
    const stale = age > 30000;
    document.body.classList.toggle('stale', stale);
    const el = document.getElementById('footer-update');
    if (el && stale && !el.querySelector('a')) {
        el.textContent = 'stale \u2014 last updated ' + Math.round(age / 1000) + 's ago \u00b7 ';
    } else if (el && !stale && el.textContent.startsWith('stale')) {
        el.textContent = '';
    }
}

// ── Storage Explorer ──────────────────────────────────────────────────────────

// Load import.js once. Sets _seLoaded immediately as a guard against
// double-loading (e.g. when called from both switchTab and openStorageExplorer).
function _ensureStorageExplorerLoaded() {
    if (window._seLoaded) return;
    window._seLoaded = true;
    const s = document.createElement('script');
    s.src = '/import.js';
    s.onerror = () => {
        window._seLoaded = false;
        const tree = document.getElementById('browse-tree');
        if (tree) {
            const msg = document.createElement('div');
            msg.className = 'no-items';
            msg.textContent = 'Failed to load storage explorer. Try refreshing the page.';
            tree.replaceChildren(msg);
        }
    };
    document.head.appendChild(s);
}

function openStorageExplorer() {
    if (window.switchTab) switchTab('storage');
    const section = document.getElementById('storage-explorer-section');
    if (section) {
        section.classList.remove('hidden');
        section.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
    _ensureStorageExplorerLoaded();
}

function closeStorageExplorer() {
    const section = document.getElementById('storage-explorer-section');
    if (section) section.classList.add('hidden');
}

async function checkVPNStatus() {
    try {
        if (document.querySelector('.vpn-banner')) return;
        const res = await tfetch('/api/pelicula/status');
        if (!res.ok) return;
        const data = await res.json();
        if (data.vpn_configured === false) {
            const banner = document.createElement('div');
            banner.className = 'vpn-banner';
            banner.innerHTML = '⚡ VPN not configured — downloading is disabled. <a href="/settings">Set up VPN →</a>';
            const main = document.querySelector('.main-content') || document.body;
            main.prepend(banner);
        }
    } catch(e) { /* non-critical */ }
}

checkAuth();
checkVPNStatus();
if (window.location.hash === '#storage-explorer') {
    setTimeout(openStorageExplorer, 0);
}
setTimeout(refresh, 500);
setTimeout(loadStorageSettings, 600);
// Update check runs once on load — backend caches for 24h so no need to poll.
setTimeout(checkUpdates, 1000);
// Services auto-refresh is started by services.js loadOnce (PeliculaFW component).
window._refreshInterval = setInterval(refresh, 15000);
setInterval(updateStaleBanner, 5000);

// loadUsers, startResetPassword, cancelResetPassword, submitResetPassword,
// startDeleteUser, deleteUser, loadSessions, add-user-form handler,
// loadRequests, renderRequests, approveRequest, denyRequest,
// loadInvites, copyInviteItemLink, revokeInvite, deleteInvite,
// openInviteModal, closeInviteModal, submitCreateInvite, showInviteShareStep,
// copyInviteLink — moved to users.js.

// ── Window exports ────────────────────────
window.refresh       = refresh;
window.checkStorage  = checkStorage;

// ── Job drawer ────────────────────────────
window.openJobDrawer = async function(jobId) {
    const backdrop = document.getElementById('drawer-backdrop');
    const drawer = document.getElementById('job-drawer');
    const title = document.getElementById('drawer-title');
    const sub = document.getElementById('drawer-subtitle');
    const body = document.getElementById('drawer-body');
    const actions = document.getElementById('drawer-actions');
    if (!drawer) return;
    PeliculaFW.openDrawer(drawer, backdrop);
    title.textContent = 'Job Details';
    sub.textContent = jobId;
    body.innerHTML = '<div style="color:var(--muted);font-size:0.82rem;padding:1rem 0">Loading\u2026</div>';
    actions.innerHTML = '';
    try {
        const res = await fetch('/api/procula/jobs/' + encodeURIComponent(jobId));
        if (!res.ok) throw new Error('Not found');
        const j = await res.json();
        title.textContent = (j.source && j.source.title) ? j.source.title : jobId;
        sub.textContent = j.state + (j.stage ? ' \u00b7 ' + j.stage : '');
        // Action buttons
        if (j.state === 'failed') {
            actions.innerHTML = html`<button class="dl-btn resume" onclick="retryJob('${j.id}');closeJobDrawer()">&#8635; Retry</button>`.str;
        }
        if (j.state === 'queued' || j.state === 'processing' || j.state === 'failed') {
            actions.innerHTML += html`<button class="dl-btn cancel" onclick="cancelJob('${j.id}');closeJobDrawer()">&#10005; Cancel</button>`.str;
        }
        // Body
        let drawerHtml = '';
        // Validation checks
        if (j.validation && j.validation.checks) {
            const checks = j.validation.checks;
            const checkSpans = ['integrity', 'duration', 'sample'].map(k => {
                const v = checks[k] || 'skip';
                const cls = ['pass','fail','warn'].includes(v) ? v : 'skip';
                return html`<span class="proc-check proc-check-${cls}">${k}: ${v}</span>`.str;
            }).join('');
            drawerHtml += html`<div class="drawer-section"><div class="drawer-section-title">Validation</div><div class="drawer-check-list">${raw(checkSpans)}</div></div>`.str;
        }
        // File info
        if (j.source) {
            let fileRows = '';
            if (j.source.path) fileRows += html`<div class="drawer-kv"><span class="drawer-kv-key">Path</span><span class="drawer-kv-val" style="word-break:break-all">${j.source.path}</span></div>`.str;
            if (j.source.size) fileRows += html`<div class="drawer-kv"><span class="drawer-kv-key">Size</span><span class="drawer-kv-val">${formatSize(j.source.size)}</span></div>`.str;
            drawerHtml += html`<div class="drawer-section"><div class="drawer-section-title">File</div>${raw(fileRows)}</div>`.str;
        }
        // Transcode info
        if (j.transcode_profile || j.transcode_decision) {
            let txRows = '';
            if (j.transcode_profile) txRows += html`<div class="drawer-kv"><span class="drawer-kv-key">Profile</span><span class="drawer-kv-val">${j.transcode_profile}</span></div>`.str;
            if (j.transcode_decision) txRows += html`<div class="drawer-kv"><span class="drawer-kv-key">Decision</span><span class="drawer-kv-val">${j.transcode_decision}</span></div>`.str;
            drawerHtml += html`<div class="drawer-section"><div class="drawer-section-title">Transcoding</div>${raw(txRows)}</div>`.str;
        }
        // Error
        if (j.error) {
            drawerHtml += html`<div class="drawer-section"><div class="drawer-section-title">Error</div><div class="drawer-error">${j.error}</div></div>`.str;
        }
        // Timeline
        if (j.events && j.events.length) {
            const items = j.events.map(ev =>
                html`<li><span class="drawer-timeline-time">${new Date(ev.at).toLocaleTimeString()}</span><span>${ev.message || ev.event || ''}</span></li>`.str
            ).join('');
            drawerHtml += html`<div class="drawer-section"><div class="drawer-section-title">Timeline</div><ul class="drawer-timeline">${raw(items)}</ul></div>`.str;
        }
        body.innerHTML = drawerHtml || html`<div style="color:var(--muted);font-size:0.82rem;padding:1rem 0">No details available.</div>`.str;
    } catch (e) {
        body.innerHTML = '<div class="drawer-error">Failed to load job details.</div>';
    }
};

window.closeJobDrawer = function() {
    PeliculaFW.closeDrawer(
        document.getElementById('job-drawer'),
        document.getElementById('drawer-backdrop')
    );
};

// ── Tab routing (hash-based) ─────────────
// switchTab updates the DOM + hash; router.listen drives back/forward.

const _validTabs = new Set(['search', 'coming', 'catalog', 'jobs', 'logs', 'storage', 'users', 'settings']);

window.switchTab = function(tab, fromHash) {
    if (!_validTabs.has(tab)) tab = 'search';
    if (tab === document.body.dataset.tab) return;
    document.querySelectorAll('.tab[data-tab]').forEach(function(btn) {
        var isActive = btn.dataset.tab === tab;
        btn.classList.toggle('active', isActive);
        btn.setAttribute('aria-selected', isActive ? 'true' : 'false');
        btn.setAttribute('tabindex', isActive ? '0' : '-1');
    });
    document.body.dataset.tab = tab;
    // Sync hash: pushState for user clicks (enables back button), replaceState for
    // hash-driven navigation (avoids double history entry).
    const target = '#' + tab;
    if (location.hash !== target) {
        if (fromHash) history.replaceState(null, '', target);
        else history.pushState(null, '', target);
    }
    // Lazy-load storage explorer on first visit
    if (tab === 'storage') _ensureStorageExplorerLoaded();
    document.dispatchEvent(new CustomEvent('pelicula:tab-changed', { detail: { tab: tab } }));
};

// Drive tab navigation from hash changes (back/forward, deep links, manual hash edits)
PeliculaFW.router.listen(function(route) {
    var tab = route.tab || 'search';
    if (_validTabs.has(tab)) window.switchTab(tab, true);
});

// Arrow key navigation within the tabbar (WAI-ARIA tabs pattern)
document.getElementById('tabbar').addEventListener('keydown', function(e) {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
    var tabs = Array.from(this.querySelectorAll('.tab:not(.hidden):not([style*="display: none"])'));
    var idx = tabs.indexOf(document.activeElement);
    if (idx === -1) return;
    e.preventDefault();
    var next = e.key === 'ArrowRight' ? (idx + 1) % tabs.length : (idx - 1 + tabs.length) % tabs.length;
    tabs[next].focus();
    tabs[next].click();
});

// Settings functions are in settings.js (PeliculaFW component 'settings').
// loadSettingsTab, saveSettingsTab, toggleSetting, updateNotifMode, clearProfileForm,
// saveProfile, installDefaultProfiles, saveRequestsSettings, loadArrMeta on window.*.

// ── Event log ─────────────────────────────
// Moved to pipeline.js (PeliculaFW component). onEventLogToggle/setEventFilter/loadEventLog on window.*.

// ── Theme ─────────────────────────────────────────────────────────────────
function _isDarkActive() {
    const t = document.documentElement.dataset.theme;
    if (t === 'dark') return true;
    if (t === 'light') return false;
    return window.matchMedia('(prefers-color-scheme: dark)').matches;
}

function updateThemeIcon() {
    const btn = document.getElementById('theme-toggle');
    if (!btn) return;
    btn.textContent = _isDarkActive() ? '\u2600' : '\u263D'; // sun : crescent moon
    btn.title = _isDarkActive() ? 'Switch to light mode' : 'Switch to dark mode';
}

function toggleTheme() {
    const next = _isDarkActive() ? 'light' : 'dark';
    document.documentElement.dataset.theme = next;
    localStorage.setItem('pelicula-theme', next);
    updateThemeIcon();
    _syncAppearanceRadio();
}

function setThemePref(pref) {
    if (pref === 'system') {
        delete document.documentElement.dataset.theme;
        localStorage.removeItem('pelicula-theme');
    } else {
        document.documentElement.dataset.theme = pref;
        localStorage.setItem('pelicula-theme', pref);
    }
    updateThemeIcon();
}

function _syncAppearanceRadio() {
    const t = document.documentElement.dataset.theme || 'system';
    const radio = document.querySelector('input[name="theme-pref"][value="' + t + '"]');
    if (radio) radio.checked = true;
}

// Init theme icon on load and sync with system preference changes
document.addEventListener('DOMContentLoaded', function() {
    updateThemeIcon();
    _syncAppearanceRadio();
});
window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', function() {
    updateThemeIcon();
    _syncAppearanceRadio();
});

// Mount deferred components (registered by search.js et al., which load with defer —
// DOMContentLoaded fires after all deferred scripts have executed).
document.addEventListener('DOMContentLoaded', function() {
    PeliculaFW.mount('search', document.getElementById('search-section'));
    PeliculaFW.mount('downloads', document.getElementById('pipeline-section'));
    PeliculaFW.mount('pipeline', document.getElementById('pipeline-section'));
    PeliculaFW.mount('notifications', document.getElementById('bell-wrap'));
    PeliculaFW.mount('settings', document.getElementById('settings-section'));
    PeliculaFW.mount('users', document.getElementById('users-section'));
    PeliculaFW.mount('services', document.querySelector('.pane-side'));
});
