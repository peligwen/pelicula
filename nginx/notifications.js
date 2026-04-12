// nginx/notifications.js
// Notifications component — registered with PeliculaFW; mounted by dashboard.js.
// Depends on: framework.js (PeliculaFW), dashboard.js (tfetch, showAdminToast).

'use strict';

(function () {
    const { component, html, raw } = PeliculaFW;

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

    // Dismiss a single notification by id, then re-fetch and re-render.
    async function dismissNotification(id) {
        try {
            const res = await tfetch('/api/pelicula/notifications/' + id, { method: 'DELETE' });
            if (!res.ok) {
                showAdminToast('Could not dismiss notification', true);
                return;
            }
        } catch (e) {
            showAdminToast('Could not dismiss notification', true);
            return;
        }
        // Re-fetch so the dropdown and badge both update correctly.
        try {
            const res = await tfetch('/api/pelicula/notifications');
            if (!res.ok) return;
            const events = await res.json();
            renderNotifications(events);
        } catch (e) { console.warn('[pelicula] notifications refresh error:', e); }
    }

    // Clear all notifications, then re-render empty state.
    async function clearAllNotifications() {
        try {
            const res = await tfetch('/api/pelicula/notifications', { method: 'DELETE' });
            if (!res.ok) {
                showAdminToast('Could not clear notifications', true);
                return;
            }
        } catch (e) {
            showAdminToast('Could not clear notifications', true);
            return;
        }
        renderNotifications([]);
    }

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
            <button class="notif-clear-all" onclick="clearAllNotifications()">Clear all</button>
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
                <button class="notif-dismiss" title="Dismiss" onclick="dismissNotification('${id}')">&#10005;</button>
            </div>`;
        });

        dropdown.innerHTML = clearBtn.str + items.map(i => i.str).join('');
    }

    function toggleNotifications() {
        const dropdown = document.getElementById('notif-dropdown');
        const isHidden = dropdown.classList.contains('hidden');
        if (isHidden) {
            dropdown.classList.remove('hidden');
            // Mark all as read
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
            // Close notification dropdown on click outside
            document.addEventListener('click', function (e) {
                if (!e.target.closest('#bell-wrap')) {
                    const dropdown = document.getElementById('notif-dropdown');
                    if (dropdown) dropdown.classList.add('hidden');
                }
            });
        }

        return {
            render: function () {},   // operates on existing DOM; no template render needed
            loadOnce: init,
        };
    });

    // ── Window exports (for onclick handlers in index.html and dashboard.js) ───
    window.renderNotifications    = renderNotifications;
    window.toggleNotifications    = toggleNotifications;
    window.notifIcon              = notifIcon;
    window.notifClass             = notifClass;
    window.formatNotifTime        = formatNotifTime;
    window.dismissNotification    = dismissNotification;
    window.clearAllNotifications  = clearAllNotifications;
}());
