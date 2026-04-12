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
        renderLogs(out, data.entries || []);
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
            loadLogs();
        });
        frag.appendChild(chip);
    }
    wrap.replaceChildren(frag);
}

window.logsRefresh = function () { loadLogs(); };

PeliculaFW.onTab('logs', function () {
    renderFilters();
    loadLogs();
});

})();
