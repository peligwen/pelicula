// network.js — Bandwidth stats drawer for the pelicula dashboard.
// Fetches /api/pelicula/network on open; renders a per-container table.
// Exposed as window.openNetworkDrawer / window.closeNetworkDrawer.

import { get } from './api.js';
import { openDrawer, closeDrawer } from './framework.js';

// ── Relative time ────────────────────────────────────────────────────────────

function relTime(isoStr) {
    const secs = Math.floor((Date.now() - new Date(isoStr).getTime()) / 1000);
    if (secs < 30) return 'just now';
    if (secs < 60) return `${secs}s ago`;
    const mins = Math.floor(secs / 60);
    if (mins < 60) return `${mins}m ago`;
    const hrs = Math.floor(mins / 60);
    return `${hrs}h ago`;
}

// ── Bytes formatter ───────────────────────────────────────────────────────────

function fmtBytes(n) {
    if (n >= 1024 * 1024 * 1024) return (n / (1024 * 1024 * 1024)).toFixed(1) + ' GiB';
    if (n >= 1024 * 1024)        return (n / (1024 * 1024)).toFixed(1) + ' MiB';
    if (n >= 1024)               return (n / 1024).toFixed(1) + ' KiB';
    return n + ' B';
}

// ── DOM helpers ───────────────────────────────────────────────────────────────

function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
}

// ── Render ────────────────────────────────────────────────────────────────────

function renderStats(data, body) {
    body.replaceChildren();

    const containers = data.containers || [];

    if (containers.length === 0) {
        body.appendChild(el('div', 'no-items', 'No container stats available.'));
        return;
    }

    const table = el('table', 'net-table');
    const thead = document.createElement('thead');
    const hrow = document.createElement('tr');
    for (const col of ['Container', 'In', 'Out', 'Route']) {
        const th = el('th', null, col);
        hrow.appendChild(th);
    }
    thead.appendChild(hrow);
    table.appendChild(thead);

    const tbody = document.createElement('tbody');
    for (const c of containers) {
        const tr = document.createElement('tr');

        const tdName = el('td', 'net-td-name', c.name);
        const tdIn   = el('td', 'net-td-bytes', fmtBytes(c.bytes_in));
        const tdOut  = el('td', 'net-td-bytes', fmtBytes(c.bytes_out));

        const tdRoute = el('td', 'net-td-route');
        const badge = el('span', c.vpn_routed ? 'net-badge net-badge-vpn' : 'net-badge net-badge-host',
            c.vpn_routed ? 'VPN' : 'Host');
        tdRoute.appendChild(badge);

        tr.appendChild(tdName);
        tr.appendChild(tdIn);
        tr.appendChild(tdOut);
        tr.appendChild(tdRoute);
        tbody.appendChild(tr);
    }
    table.appendChild(tbody);
    body.appendChild(table);

    if (data.as_of) {
        const sub = document.getElementById('net-drawer-asof');
        if (sub) sub.textContent = 'As of ' + relTime(data.as_of);
    }
}

// ── Fetch ─────────────────────────────────────────────────────────────────────

async function fetchStats() {
    const body = document.getElementById('net-drawer-body');
    if (!body) return;

    body.replaceChildren();
    const spinner = el('div', 'apply-spinner');
    body.appendChild(spinner);

    try {
        const data = await get('/api/pelicula/network');
        if (data === null) {
            body.replaceChildren();
            body.appendChild(el('div', 'no-items', 'Not authorised.'));
            return;
        }
        renderStats(data, body);
    } catch (_err) {
        body.replaceChildren();
        body.appendChild(el('div', 'no-items', 'Failed to load bandwidth stats.'));
    }
}

// ── Drawer open / close ───────────────────────────────────────────────────────

function openNetworkDrawer() {
    const drawer = document.getElementById('net-drawer');
    const backdrop = document.getElementById('net-drawer-backdrop');
    if (!drawer || !backdrop) return;
    openDrawer(drawer, backdrop);
    fetchStats();
}

function closeNetworkDrawer() {
    const drawer = document.getElementById('net-drawer');
    const backdrop = document.getElementById('net-drawer-backdrop');
    if (!drawer || !backdrop) return;
    closeDrawer(drawer, backdrop);
}

// ── Wiring ────────────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
    const triggerBtn = document.getElementById('net-drawer-btn');
    const closeBtn = document.getElementById('net-drawer-close');
    const refreshBtn = document.getElementById('net-drawer-refresh');
    const backdrop = document.getElementById('net-drawer-backdrop');

    if (triggerBtn) triggerBtn.addEventListener('click', openNetworkDrawer);
    if (closeBtn) closeBtn.addEventListener('click', closeNetworkDrawer);
    if (refreshBtn) refreshBtn.addEventListener('click', fetchStats);
    if (backdrop) backdrop.addEventListener('click', closeNetworkDrawer);

    document.addEventListener('keydown', e => {
        if (e.key === 'Escape') {
            const drawer = document.getElementById('net-drawer');
            if (drawer && !drawer.classList.contains('hidden')) {
                closeNetworkDrawer();
            }
        }
    });
});

window.openNetworkDrawer = openNetworkDrawer;
window.closeNetworkDrawer = closeNetworkDrawer;
