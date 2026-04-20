// network.js — Network Connections drawer for the pelicula dashboard.
// Fetches /api/pelicula/network on open; renders VPN/Internet two-column layout.
// Exposed as window.openNetworkDrawer / window.closeNetworkDrawer.

import { get } from './api.js';

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

// ── DOM helpers ───────────────────────────────────────────────────────────────

function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
}

// ── Row rendering ─────────────────────────────────────────────────────────────

function renderRow(conn) {
    const row = el('div', 'net-row');

    // Host / dest
    const isPeer = conn.peer_count > 0;
    const hostSpan = el('span', 'net-host', isPeer ? `peers (${conn.peer_count})` : conn.dest_host);
    if (!isPeer && conn.dest_port) {
        const portSpan = el('span', 'net-port', `:${conn.dest_port}`);
        hostSpan.appendChild(portSpan);
    }

    // Container badge
    const badge = el('span', 'net-container-badge', conn.container);

    // Last seen
    const time = el('span', 'net-time', relTime(conn.last_seen));

    row.appendChild(hostSpan);
    row.appendChild(badge);
    row.appendChild(time);
    return row;
}

// ── Section builder ───────────────────────────────────────────────────────────

function renderSection(title, conns, body) {
    if (conns.length === 0) return;

    const hdr = el('h3', 'net-section-hdr', title);
    body.appendChild(hdr);

    const list = el('div', 'net-list');
    for (const conn of conns) {
        list.appendChild(renderRow(conn));
    }
    body.appendChild(list);
}

// ── Main render ───────────────────────────────────────────────────────────────

function renderConnections(data, body) {
    body.replaceChildren();

    // Degraded: netcap unavailable
    if (data.error) {
        const msg = el('div', 'no-items', 'Network monitor unavailable. Is netcap running?');
        body.appendChild(msg);
        return;
    }

    const conns = data.connections || [];
    if (conns.length === 0) {
        const msg = el('div', 'no-items', 'No active connections.');
        body.appendChild(msg);
        return;
    }

    const vpn = conns.filter(c => c.kind === 'vpn');
    const internet = conns.filter(c => c.kind === 'internet');

    const grid = el('div', 'net-grid');
    body.appendChild(grid);

    const vpnCol = el('div', 'net-col');
    const internetCol = el('div', 'net-col');
    grid.appendChild(vpnCol);
    grid.appendChild(internetCol);

    renderSection('VPN', vpn, vpnCol);
    renderSection('Internet', internet, internetCol);

    if (vpn.length === 0 && internet.length === 0) {
        grid.replaceChildren();
        const msg = el('div', 'no-items', 'No active connections.');
        body.appendChild(msg);
    }
}

// ── Fetch ─────────────────────────────────────────────────────────────────────

async function fetchConnections() {
    const body = document.getElementById('net-drawer-body');
    if (!body) return;

    body.replaceChildren();
    const spinner = el('div', 'apply-spinner');
    body.appendChild(spinner);

    try {
        const data = await get('/api/pelicula/network');
        if (data === null) {
            body.replaceChildren();
            const msg = el('div', 'no-items', 'Not authorised.');
            body.appendChild(msg);
            return;
        }
        renderConnections(data, body);
    } catch (_err) {
        body.replaceChildren();
        const msg = el('div', 'no-items', 'Failed to load network connections.');
        body.appendChild(msg);
    }
}

// ── Drawer open / close ───────────────────────────────────────────────────────

function openNetworkDrawer() {
    const drawer = document.getElementById('net-drawer');
    const backdrop = document.getElementById('net-drawer-backdrop');
    if (!drawer || !backdrop) return;
    backdrop.classList.remove('hidden');
    drawer.classList.remove('hidden');
    drawer.focus();
    fetchConnections();
}

function closeNetworkDrawer() {
    const drawer = document.getElementById('net-drawer');
    const backdrop = document.getElementById('net-drawer-backdrop');
    if (!drawer || !backdrop) return;
    drawer.classList.add('hidden');
    backdrop.classList.add('hidden');
}

// ── Wiring ────────────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
    const triggerBtn = document.getElementById('net-drawer-btn');
    const closeBtn = document.getElementById('net-drawer-close');
    const refreshBtn = document.getElementById('net-drawer-refresh');
    const backdrop = document.getElementById('net-drawer-backdrop');

    if (triggerBtn) triggerBtn.addEventListener('click', openNetworkDrawer);
    if (closeBtn) closeBtn.addEventListener('click', closeNetworkDrawer);
    if (refreshBtn) refreshBtn.addEventListener('click', fetchConnections);
    if (backdrop) backdrop.addEventListener('click', closeNetworkDrawer);

    // Escape key handler (scoped to when drawer is open)
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
