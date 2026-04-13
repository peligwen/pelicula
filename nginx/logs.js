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
