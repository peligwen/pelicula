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
    activeFilter: null, // null = show all; string = show only that service
    lastEntries: [],
    userScrolled: false,
};

function lfetch(url) { return fetch(url, { credentials: 'same-origin' }); }

function initScrollAnchor(out) {
    if (out._scrollListenerAttached) return;
    out._scrollListenerAttached = true;
    out.addEventListener('scroll', () => {
        const atBottom = out.scrollHeight - out.scrollTop - out.clientHeight < 30;
        logsState.userScrolled = !atBottom;
    });
}

async function loadLogs() {
    if (logsState.loading) return;
    logsState.loading = true;
    const out = document.getElementById('logs-stream');
    if (!out) { logsState.loading = false; return; }
    out.textContent = 'Loading\u2026';
    const services = logsState.activeFilter || ALL_SERVICES.join(',');
    try {
        const res = await lfetch('/api/pelicula/logs/aggregate?tail=200&services=' + encodeURIComponent(services));
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
    initScrollAnchor(out);
    const frag = document.createDocumentFragment();
    let lastDate = null;
    for (const e of entries) {
        if (logsState.activeFilter && e.service !== logsState.activeFilter) continue;

        // date separator
        const ts = e.ts ? new Date(e.ts) : null;
        const dateStr = ts && !isNaN(ts) ? ts.toLocaleDateString('en-US', {month:'short', day:'numeric'}) : null;
        if (dateStr && dateStr !== lastDate) {
            const sep = document.createElement('div');
            sep.className = 'logs-date-sep';
            sep.textContent = dateStr;
            frag.appendChild(sep);
            lastDate = dateStr;
        }

        const row = document.createElement('div');
        row.className = 'logs-line logs-svc-' + e.service;

        const tsEl = document.createElement('span');
        tsEl.className = 'logs-line-ts';
        tsEl.textContent = ts && !isNaN(ts)
            ? ts.toLocaleTimeString('en-US', {hour:'2-digit', minute:'2-digit', second:'2-digit', hour12:false})
            : '';

        const svc = document.createElement('span');
        svc.className = 'logs-line-svc';
        svc.textContent = e.service;

        const msg = document.createElement('span');
        msg.className = 'logs-line-msg';
        msg.textContent = e.line;

        row.append(svc, tsEl, msg);
        frag.appendChild(row);
    }
    out.replaceChildren(frag);
    if (!logsState.userScrolled) out.scrollTop = out.scrollHeight;
}

function renderFilters() {
    const wrap = document.getElementById('logs-filters');
    if (!wrap) return;
    const frag = document.createDocumentFragment();
    for (const svc of ALL_SERVICES) {
        const chip = document.createElement('span');
        const isActive = logsState.activeFilter === svc;
        chip.className = 'logs-filter-chip logs-svc-' + svc + (isActive ? ' active' : '');
        chip.textContent = svc;
        chip.addEventListener('click', () => {
            // clicking the active filter clears it (back to show all);
            // clicking any other sets it as the exclusive filter
            logsState.activeFilter = logsState.activeFilter === svc ? null : svc;
            renderFilters();
            const out = document.getElementById('logs-stream');
            if (window.sseIsActive && window.sseIsActive() && out) {
                renderLogs(out, logsState.lastEntries);
            } else {
                loadLogs();
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
    if (window.sseIsActive && window.sseIsActive()) {
        // SSE is active — render from cache immediately if we have it,
        // otherwise fall through to a one-time fetch so the tab isn't blank.
        if (logsState.lastEntries.length > 0) {
            const out = document.getElementById('logs-stream');
            if (out) renderLogs(out, logsState.lastEntries);
            return;
        }
    }
    loadLogs();
});

})();
