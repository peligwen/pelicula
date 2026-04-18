// logs.js — Logs tab: aggregated container log stream, coloured by service.
'use strict';

import { get } from '/api.js';

const ALL_SERVICES = [
    'pelicula-api', 'procula', 'nginx',
    'sonarr', 'radarr', 'prowlarr',
    'qbittorrent', 'jellyfin', 'bazarr', 'gluetun', 'docker-proxy',
];

const logsState = {
    loaded: false,
    loading: false,
    activeFilter: null,
    lastEntries: [],
    userScrolled: false,
};

function initScrollAnchor(out) {
    if (out._scrollListenerAttached) return;
    out._scrollListenerAttached = true;
    out.addEventListener('scroll', () => {
        const atTop = out.scrollTop < 30;
        logsState.userScrolled = !atTop;
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
        const data = await get('/api/pelicula/logs/aggregate?tail=200&services=' + encodeURIComponent(services));
        if (data === null) { out.textContent = 'Session expired.'; return; }
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
    let lastHour = null;
    for (const e of entries) {
        if (logsState.activeFilter && e.service !== logsState.activeFilter) continue;

        const ts = e.ts ? new Date(e.ts) : null;
        const isValid = ts && !isNaN(ts);

        const dateStr = isValid ? ts.toLocaleDateString('en-US', {month:'short', day:'numeric'}) : null;
        if (dateStr && dateStr !== lastDate) {
            const sep = document.createElement('div');
            sep.className = 'logs-date-sep';
            sep.textContent = dateStr;
            frag.appendChild(sep);
            lastDate = dateStr;
            lastHour = isValid ? ts.getHours() : null;
        } else if (isValid) {
            const hour = ts.getHours();
            if (lastHour !== null && hour !== lastHour) {
                const sep = document.createElement('div');
                sep.className = 'logs-hour-sep';
                sep.textContent = String(hour).padStart(2, '0') + ':00';
                frag.appendChild(sep);
            }
            if (lastHour === null || hour !== lastHour) lastHour = hour;
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

        row.append(tsEl, svc, msg);
        frag.appendChild(row);
    }
    out.replaceChildren(frag);
    if (!logsState.userScrolled) out.scrollTop = 0;
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

document.getElementById('log-refresh-btn').addEventListener('click', function() {
    logsState.userScrolled = false;
    loadLogs();
});

window.renderLogsFromSSE = function(data) {
    const out = document.getElementById('logs-stream');
    if (!out) return;
    logsState.lastEntries = data.entries || [];
    renderLogs(out, logsState.lastEntries);
    logsState.loaded = true;
};

function openLogsSheet() {
    const sheet = document.getElementById('logs-sheet');
    const backdrop = document.getElementById('logs-sheet-backdrop');
    if (!sheet || !backdrop) return;
    backdrop.classList.remove('hidden');
    sheet.classList.remove('hidden');
    logsState.userScrolled = false;
    renderFilters();
    if (window.sseIsActive && window.sseIsActive()) {
        if (logsState.lastEntries.length > 0) {
            const out = document.getElementById('logs-stream');
            if (out) renderLogs(out, logsState.lastEntries);
            return;
        }
    }
    loadLogs();
}

function closeLogsSheet() {
    const sheet = document.getElementById('logs-sheet');
    const backdrop = document.getElementById('logs-sheet-backdrop');
    if (sheet) sheet.classList.add('hidden');
    if (backdrop) backdrop.classList.add('hidden');
}

document.getElementById('logs-sheet-panel-row').addEventListener('click', openLogsSheet);
document.getElementById('logs-sheet-open-btn').addEventListener('click', e => {
    e.stopPropagation();
    openLogsSheet();
});
document.getElementById('logs-sheet-backdrop').addEventListener('click', closeLogsSheet);
document.getElementById('logs-sheet-close-btn').addEventListener('click', closeLogsSheet);
