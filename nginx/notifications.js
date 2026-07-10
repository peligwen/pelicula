// nginx/notifications.js
// Notifications component — registered with PeliculaFW; mounted by dashboard.js.
'use strict';

import { component, html, raw } from '/framework.js';

// notif-helpers.js is a classic script loaded before this module; pull its
// exports into module scope so bare identifiers resolve without relying on
// the global object (ES modules do NOT fall through to window).
const notifIcon  = window.notifIcon;
const notifClass = window.notifClass;

// ── Module-level state ────────────────────────────────────────────────────
let lastSeenTs = localStorage.getItem('peliculaLastSeen') || '1970-01-01T00:00:00Z';

// ── Helpers ───────────────────────────────────────────────────────────────

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

    const items = events.slice(0, 20).map(e => {
        const isUnread = e.timestamp > lastSeenTs;
        const typeClass = notifClass(e.type);
        const icon = notifIcon(e.type);
        const time = formatNotifTime(e.timestamp);
        return html`<div class="notif-item ${isUnread ? 'unread' : ''} ${typeClass}">
            <span class="notif-icon">${raw(icon)}</span>
            <div class="notif-body">
                <div class="notif-msg">${e.message}</div>
                <div class="notif-time">${time}</div>
            </div>
        </div>`;
    });

    dropdown.innerHTML = items.map(i => i.str).join('');
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

// ── Window exports ────────────────────────────────────────────────────────
// renderNotifications is called by sse.js, activity.js, and dashboard.js refresh.
window.renderNotifications = renderNotifications;
