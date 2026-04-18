// nginx/notifications.js
// Notifications component — registered with PeliculaFW; mounted by dashboard.js.
'use strict';

import { component, html, raw, toast } from '/framework.js';
import { get, del } from '/api.js';

// ── Module-level state ────────────────────────────────────────────────────
let lastSeenTs = localStorage.getItem('peliculaLastSeen') || '1970-01-01T00:00:00Z';

// ── Helpers ───────────────────────────────────────────────────────────────
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

function formatNotifTime(ts) {
    try {
        const d = new Date(ts);
        const now = new Date();
        const diff = Math.floor((now - d) / 1000);
        if (diff < 60) return 'just now';
        if (diff < 3600) return Math.floor(diff / 60) + 'm ago';
        if (diff < 86400) return Math.floor(diff / 3600) + 'h ago';
        return d.toLocaleDateString();
    } catch { return ''; }
}

// ── Notification list rendering ───────────────────────────────────────────

async function dismissNotification(id) {
    try {
        await del('/api/pelicula/notifications/' + id);
    } catch (e) {
        toast('Could not dismiss notification', { error: true });
        return;
    }
    try {
        const events = await get('/api/pelicula/notifications');
        if (events !== null) renderNotifications(events);
    } catch (e) { console.warn('[pelicula] notifications refresh error:', e); }
}

async function clearAllNotifications() {
    try {
        await del('/api/pelicula/notifications');
    } catch (e) {
        toast('Could not clear notifications', { error: true });
        return;
    }
    renderNotifications([]);
}

// renderNotifications renders notification items using the framework's html``
// tagged template, which auto-escapes all string interpolations.
function renderNotifications(events) {
    if (!Array.isArray(events)) return;
    const badge = document.getElementById('bell-badge');
    const dropdown = document.getElementById('notif-dropdown');

    const unread = events.filter(e => e.timestamp > lastSeenTs);
    if (unread.length > 0) {
        badge.textContent = unread.length > 9 ? '9+' : String(unread.length);
        badge.classList.remove('hidden');
    } else {
        badge.classList.add('hidden');
    }

    if (!events.length) {
        dropdown.innerHTML = html`<div class="notif-empty">No notifications</div>`.str;
        return;
    }

    const clearBtn = html`<div class="notif-actions">
        <button class="notif-clear-all" data-action="clear-all">Clear all</button>
    </div>`;

    const items = events.slice(0, 20).map(e => {
        const isUnread = e.timestamp > lastSeenTs;
        const typeClass = notifClass(e.type);
        const icon = notifIcon(e.type);
        const time = formatNotifTime(e.timestamp);
        const id = e.id || '';
        return html`<div class="notif-item ${isUnread ? 'unread' : ''} ${typeClass}">
            <span class="notif-icon">${raw(icon)}</span>
            <div class="notif-body">
                <div class="notif-msg">${e.message}</div>
                <div class="notif-time">${time}</div>
            </div>
            <button class="notif-dismiss" title="Dismiss" data-action="dismiss" data-id="${id}">&#10005;</button>
        </div>`;
    });

    dropdown.innerHTML = clearBtn.str + items.map(i => i.str).join('');
}

function toggleNotifications() {
    const dropdown = document.getElementById('notif-dropdown');
    const isHidden = dropdown.classList.contains('hidden');
    if (isHidden) {
        dropdown.classList.remove('hidden');
        lastSeenTs = new Date().toISOString();
        localStorage.setItem('peliculaLastSeen', lastSeenTs);
        document.getElementById('bell-badge').classList.add('hidden');
    } else {
        dropdown.classList.add('hidden');
    }
}

// ── Component registration ────────────────────────────────────────────────
component('notifications', function (el, storeProxy) {
    function init() {
        document.addEventListener('click', function (e) {
            if (!e.target.closest('#bell-wrap')) {
                const dropdown = document.getElementById('notif-dropdown');
                if (dropdown) dropdown.classList.add('hidden');
            }
        });
    }

    return {
        render: function () {},
        loadOnce: init,
    };
});

document.getElementById('bell-btn').addEventListener('click', toggleNotifications);

document.getElementById('notif-dropdown').addEventListener('click', e => {
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    if (btn.dataset.action === 'dismiss') dismissNotification(btn.dataset.id);
    else if (btn.dataset.action === 'clear-all') clearAllNotifications();
});

// ── Window exports ────────────────────────────────────────────────────────
// renderNotifications is called by sse.js, activity.js, and dashboard.js refresh.
window.renderNotifications = renderNotifications;
