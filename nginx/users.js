// nginx/users.js
// Users component — registered with PeliculaFW; mounted by dashboard.js.
// Depends on: framework.js (PeliculaFW), dashboard.js (tfetch, store).

'use strict';

(function () {
    const { component, html, raw } = PeliculaFW;

    // ── Module-level state ────────────────────────────────────────────────────
    let usersLoaded = false;
    let requestsLoaded = false;
    // arrMetaLoaded / _arrMeta / populateRequestsSettings / saveRequestsSettings
    // live in settings.js (canonical owner). users.js calls them via window.*.

    // ── Users ─────────────────────────────────────────────────────────────────

    async function loadUsers() {
        const list = document.getElementById('users-list');
        if (!list) return;
        try {
            const resp = await fetch('/api/pelicula/users');
            if (!resp.ok) return;
            const users = await resp.json();
            const countEl = document.getElementById('users-count');
            const metricEl = document.getElementById('um-metric-accounts');
            if (!users || users.length === 0) {
                list.innerHTML = '<li style="color:var(--muted,#9080a8);font-size:0.8rem;padding:0.5rem 1rem;background:var(--panel2,#fdf5ff);border:1.5px solid var(--border2,rgba(180,140,220,0.4));border-radius:16px;">No users yet.</li>';
                if (countEl) countEl.textContent = '';
                if (metricEl) metricEl.textContent = '0';
                return;
            }
            if (countEl) countEl.textContent = ' (' + users.length + ')';
            if (metricEl) metricEl.textContent = users.length;
            list.innerHTML = users.map(u => {
                const lastSeen = u.lastLoginDate
                    ? new Date(u.lastLoginDate).toLocaleDateString()
                    : 'never';
                const adminBadge = u.isAdmin ? html`<span class="user-admin-badge">admin</span>`.str : '';
                return html`<li data-user-id="${u.id}" data-user-name="${u.name}">
                    <div class="user-info"><span class="user-name">${u.name}</span>${raw(adminBadge)}<span class="user-meta">last login: ${lastSeen}</span></div>
                    <div class="user-actions">
                    <button class="user-action-btn" onclick="startResetPassword(this)" title="Reset password">Reset</button>
                    <button class="user-action-btn user-action-delete" onclick="startDeleteUser(this)" title="Delete user">Delete</button>
                    </div>
                    <form class="user-reset-form hidden" onsubmit="event.preventDefault(); submitResetPassword(this);">
                    <input type="password" class="user-reset-input" placeholder="New password" autocomplete="new-password">
                    <button type="submit" class="user-action-btn">Set</button>
                    <button type="button" class="user-action-btn" onclick="cancelResetPassword(this)">Cancel</button>
                    </form>
                </li>`.str;
            }).join('');
        } catch (e) {
            console.warn('[pelicula] loadUsers error:', e);
        }
    }

    function startResetPassword(btn) {
        const li = btn.closest('li');
        li.querySelector('.user-actions').classList.add('hidden');
        li.querySelector('.user-reset-form').classList.remove('hidden');
        li.querySelector('.user-reset-input').focus();
    }

    function cancelResetPassword(btn) {
        const li = btn.closest('li');
        li.querySelector('.user-reset-form').classList.add('hidden');
        li.querySelector('.user-actions').classList.remove('hidden');
        li.querySelector('.user-reset-input').value = '';
    }

    async function submitResetPassword(form) {
        const li = form.closest('li');
        const id = li.dataset.userId;
        const input = li.querySelector('.user-reset-input');
        const password = input.value;
        if (!password) { input.focus(); return; }
        const btn = form.querySelector('button[type="submit"]');
        btn.disabled = true;
        try {
            const resp = await fetch('/api/pelicula/users/' + encodeURIComponent(id) + '/password', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ password }),
            });
            if (!resp.ok) {
                const data = await resp.json().catch(() => ({}));
                alert(data.error || 'Failed to reset password.');
                return;
            }
            cancelResetPassword(btn);
        } catch (e) {
            alert('Network error resetting password.');
        } finally {
            btn.disabled = false;
        }
    }

    function startDeleteUser(btn) {
        if (btn.dataset.confirming) {
            deleteUser(btn);
            return;
        }
        btn.dataset.confirming = '1';
        btn.textContent = 'Confirm?';
        btn.classList.add('user-action-delete-confirm');
        // Auto-reset after 4s if not confirmed
        setTimeout(() => {
            if (btn.dataset.confirming) {
                btn.dataset.confirming = '';
                btn.textContent = 'Delete';
                btn.classList.remove('user-action-delete-confirm');
            }
        }, 4000);
    }

    async function deleteUser(btn) {
        const li = btn.closest('li');
        const id = li.dataset.userId;
        const name = li.dataset.userName;
        btn.disabled = true;
        try {
            const resp = await fetch('/api/pelicula/users/' + encodeURIComponent(id), {
                method: 'DELETE',
            });
            if (!resp.ok) {
                const data = await resp.json().catch(() => ({}));
                alert(data.error || 'Failed to delete ' + name + '.');
                btn.disabled = false;
                btn.dataset.confirming = '';
                btn.textContent = 'Delete';
                btn.classList.remove('user-action-delete-confirm');
                return;
            }
            loadUsers();
        } catch (e) {
            alert('Network error deleting user.');
            btn.disabled = false;
        }
    }

    // ── Sessions / Now Playing ─────────────────────────────────────────────────

    async function loadSessions() {
        const list = document.getElementById('sessions-list');
        const section = document.getElementById('now-playing-section');
        if (!list || !section) return;
        try {
            const resp = await tfetch('/api/pelicula/sessions');
            if (!resp.ok) { section.classList.add('hidden'); return; }
            const sessions = await resp.json();
            const active = (sessions || []).filter(s => s.nowPlayingTitle);
            const sessMetric = document.getElementById('um-metric-sessions');
            if (sessMetric) sessMetric.textContent = active.length;
            if (active.length === 0) {
                section.classList.add('hidden');
                return;
            }
            section.classList.remove('hidden');
            list.innerHTML = active.map(s => {
                const what = s.nowPlayingType === 'Episode'
                    ? html`episode of ${s.nowPlayingTitle}`.str
                    : html`${s.nowPlayingTitle}`.str;
                return html`<li class="session-item"><span class="session-user">${s.userName}</span><span class="session-sep">\u00b7</span><span class="session-title">${raw(what)}</span><span class="session-sep">\u00b7</span><span class="session-device">${s.client || s.deviceName}</span></li>`.str;
            }).join('');
        } catch (e) {
            section.classList.add('hidden');
            console.warn('[pelicula] loadSessions error:', e);
        }
    }

    // ── Requests ──────────────────────────────────────────────────────────────

    async function loadRequests() {
        try {
            const resp = await fetch('/api/pelicula/requests');
            if (!resp.ok) return;
            const requests = await resp.json();
            renderRequests(requests || []);
        } catch (e) { console.warn('[pelicula] loadRequests error', e); }
    }

    function renderRequests(requests) {
        const isAdmin = store.get('role') === 'admin';
        const username = document.body.dataset.username || '';

        const pendingList = document.getElementById('requests-pending-list');
        const pendingEmpty = document.getElementById('requests-pending-empty');
        const mineList = document.getElementById('requests-mine-list');
        const mineEmpty = document.getElementById('requests-mine-empty');

        const pending = requests.filter(r => r.state === 'pending' && isAdmin);
        const mine = requests.filter(r => r.requested_by === username || (!username && !isAdmin));

        if (pendingList) {
            pendingList.innerHTML = pending.map(r => {
                const poster = r.poster
                    ? html`<img class="request-poster" src="${r.poster}" alt="">`.str
                    : '<div class="request-poster request-poster-placeholder"></div>';
                const yearSpan = r.year ? html` <span class="request-year">(${r.year})</span>`.str : '';
                return html`<li class="request-item" data-id="${r.id}">
                    ${raw(poster)}
                    <div class="request-info">
                        <div class="request-title">${r.title}${raw(yearSpan)}</div>
                        <div class="request-meta">${r.type} \u00b7 requested by ${r.requested_by}</div>
                    </div>
                    <div class="request-actions">
                        <button class="request-btn request-btn-approve" onclick="approveRequest('${r.id}')">Approve</button>
                        <button class="request-btn request-btn-deny" onclick="denyRequest('${r.id}')">Deny</button>
                    </div>
                </li>`.str;
            }).join('');
            if (pendingEmpty) pendingEmpty.classList.toggle('hidden', pending.length > 0);
        }

        if (mineList) {
            mineList.innerHTML = mine.map(r => {
                const poster = r.poster
                    ? html`<img class="request-poster" src="${r.poster}" alt="">`.str
                    : '<div class="request-poster request-poster-placeholder"></div>';
                const yearSpan = r.year ? html` <span class="request-year">(${r.year})</span>`.str : '';
                const reasonDiv = r.reason ? html`<div class="request-reason">${r.reason}</div>`.str : '';
                return html`<li class="request-item request-item-${r.state}" data-id="${r.id}">
                    ${raw(poster)}
                    <div class="request-info">
                        <div class="request-title">${r.title}${raw(yearSpan)}</div>
                        <div class="request-meta">${r.type}</div>
                        ${raw(reasonDiv)}
                    </div>
                    <span class="request-state request-state-${r.state}">${r.state}</span>
                </li>`.str;
            }).join('');
            if (mineEmpty) mineEmpty.classList.toggle('hidden', mine.length > 0);
        }
    }

    async function approveRequest(id) {
        const btn = document.querySelector('.request-item[data-id="' + id + '"] .request-btn-approve');
        if (btn) { btn.disabled = true; btn.textContent = 'Approving\u2026'; }
        try {
            const resp = await fetch('/api/pelicula/requests/' + id + '/approve', {method: 'POST'});
            if (!resp.ok) {
                const data = await resp.json().catch(() => ({}));
                alert('Approve failed: ' + (data.error || resp.status));
                if (btn) { btn.disabled = false; btn.textContent = 'Approve'; }
                return;
            }
            await loadRequests();
        } catch (e) {
            alert('Network error');
            if (btn) { btn.disabled = false; btn.textContent = 'Approve'; }
        }
    }

    async function denyRequest(id) {
        const reason = prompt('Reason for denial (optional):') ?? null;
        if (reason === null) return; // cancelled
        try {
            const resp = await fetch('/api/pelicula/requests/' + id + '/deny', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({reason})
            });
            if (!resp.ok) {
                const data = await resp.json().catch(() => ({}));
                alert('Deny failed: ' + (data.error || resp.status));
                return;
            }
            await loadRequests();
        } catch (e) { alert('Network error'); }
    }

    // ── Arr-meta for admin request settings dropdowns ──────────────────────────
    // Canonical implementations live in settings.js — called here via window.*.

    // ── Invites ───────────────────────────────────────────────────────────────

    async function loadInvites() {
        const list = document.getElementById('invites-list');
        if (!list) return;
        try {
            const resp = await fetch('/api/pelicula/invites');
            if (!resp.ok) { list.innerHTML = ''; return; }
            const invites = await resp.json();
            const invMetric = document.getElementById('um-metric-invites');
            if (!invites || invites.length === 0) {
                list.innerHTML = html`<li class="invite-empty">No invite links yet.</li>`.str;
                if (invMetric) invMetric.textContent = '0';
                return;
            }
            if (invMetric) invMetric.textContent = invites.filter(i => i.state === 'active').length;
            list.innerHTML = invites.map(inv => {
                const stateClassMap = {active:'invite-active', expired:'invite-dead', exhausted:'invite-dead', revoked:'invite-dead'};
                const stateClass = stateClassMap[inv.state] || 'invite-dead';
                const stateLabelMap = {active:'active', expired:'expired', exhausted:'used up', revoked:'revoked'};
                const stateLabel = stateLabelMap[inv.state] || inv.state;
                const uses = inv.max_uses != null ? (inv.uses + '/' + inv.max_uses) : (inv.uses + '/\u221e');
                const expiry = inv.expires_at ? 'expires ' + new Date(inv.expires_at).toLocaleDateString() : 'no expiry';
                const link = window.location.origin + '/register?t=' + encodeURIComponent(inv.token);
                const isActive = inv.state === 'active';
                const labelSpan = inv.label ? html`<span class="invite-label-text">${inv.label}</span>`.str : '';
                const copyBtn = isActive ? html`<button class="user-action-btn" onclick="copyInviteItemLink(this, '${link}')" title="Copy invite link">Copy link</button>`.str : '';
                const revokeBtn = isActive ? html`<button class="user-action-btn" onclick="revokeInvite(this)" title="Deactivate this invite">Revoke</button>`.str : '';
                return html`<li class="invite-item" data-token="${inv.token}">
                    <div class="invite-row">
                    <span class="invite-badge ${stateClass}">${stateLabel}</span>
                    <span class="invite-meta">${uses} use${inv.uses !== 1 ? 's' : ''} \u00b7 ${expiry}</span>
                    ${raw(labelSpan)}
                    </div>
                    <div class="invite-actions">
                    ${raw(copyBtn)}${raw(revokeBtn)}
                    <button class="user-action-btn user-action-delete" onclick="deleteInvite(this)" title="Delete record">Delete</button>
                    </div>
                </li>`.str;
            }).join('');
        } catch (e) {
            console.warn('[pelicula] loadInvites error:', e);
        }
    }

    function copyInviteItemLink(btn, link) {
        const doCopy = () => {
            const prev = btn.textContent;
            btn.textContent = 'Copied!';
            setTimeout(() => { btn.textContent = prev; }, 2000);
        };
        if (navigator.clipboard) {
            navigator.clipboard.writeText(link).then(doCopy).catch(() => { btn.textContent = 'Copy failed'; });
        } else {
            doCopy(); // best-effort fallback
        }
    }

    async function revokeInvite(btn) {
        const li = btn.closest('li');
        const token = li.dataset.token;
        btn.disabled = true;
        try {
            const resp = await fetch('/api/pelicula/invites/' + encodeURIComponent(token) + '/revoke', { method: 'POST' });
            if (!resp.ok) {
                const d = await resp.json().catch(() => ({}));
                alert(d.error || 'Failed to revoke invite.');
                btn.disabled = false;
                return;
            }
            loadInvites();
        } catch (e) {
            alert('Network error revoking invite.');
            btn.disabled = false;
        }
    }

    async function deleteInvite(btn) {
        if (!btn.dataset.confirming) {
            btn.dataset.confirming = '1';
            btn.textContent = 'Confirm?';
            btn.classList.add('user-action-delete-confirm');
            setTimeout(() => {
                if (btn.dataset.confirming) {
                    btn.dataset.confirming = '';
                    btn.textContent = 'Delete';
                    btn.classList.remove('user-action-delete-confirm');
                }
            }, 4000);
            return;
        }
        const li = btn.closest('li');
        const token = li.dataset.token;
        btn.disabled = true;
        try {
            const resp = await fetch('/api/pelicula/invites/' + encodeURIComponent(token), { method: 'DELETE' });
            if (!resp.ok) {
                const d = await resp.json().catch(() => ({}));
                alert(d.error || 'Failed to delete invite.');
                btn.disabled = false;
                return;
            }
            loadInvites();
        } catch (e) {
            alert('Network error deleting invite.');
            btn.disabled = false;
        }
    }

    // ── Invite modal ──────────────────────────────────────────────────────────

    function openInviteModal() {
        // Reset to step 1
        document.getElementById('invite-step-create').style.display = '';
        document.getElementById('invite-step-share').style.display = 'none';
        document.getElementById('invite-modal-title').textContent = 'Create invite link';
        document.getElementById('invite-label').value = '';
        document.getElementById('invite-expires').value = '168';
        document.getElementById('invite-uses').value = '1';
        document.getElementById('invite-create-error').classList.add('hidden');
        document.getElementById('invite-create-btn').disabled = false;
        document.getElementById('invite-modal').classList.remove('hidden');
    }

    function closeInviteModal() {
        document.getElementById('invite-modal').classList.add('hidden');
        loadInvites();
    }

    async function submitCreateInvite() {
        const btn = document.getElementById('invite-create-btn');
        const errEl = document.getElementById('invite-create-error');
        errEl.classList.add('hidden');

        const label = document.getElementById('invite-label').value.trim();
        const expiresHours = parseInt(document.getElementById('invite-expires').value, 10);
        const maxUsesVal = parseInt(document.getElementById('invite-uses').value, 10);

        const body = {
            label: label || undefined,
            expires_in_hours: expiresHours > 0 ? expiresHours : null,
            max_uses: maxUsesVal > 0 ? maxUsesVal : null,
        };

        btn.disabled = true;
        btn.textContent = 'Creating\u2026';
        try {
            const resp = await fetch('/api/pelicula/invites', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(body),
            });
            const data = await resp.json().catch(() => ({}));
            if (!resp.ok) {
                errEl.textContent = data.error || 'Failed to create invite.';
                errEl.classList.remove('hidden');
                return;
            }
            showInviteShareStep(data);
        } catch (e) {
            errEl.textContent = 'Network error.';
            errEl.classList.remove('hidden');
        } finally {
            btn.disabled = false;
            btn.textContent = 'Create link';
        }
    }

    function showInviteShareStep(invite) {
        const link = window.location.origin + '/register?t=' + encodeURIComponent(invite.token);
        document.getElementById('invite-link-val').value = link;
        document.getElementById('invite-step-create').style.display = 'none';
        document.getElementById('invite-step-share').style.display = '';
        document.getElementById('invite-modal-title').textContent = 'Share invite link';

        // QR code
        if (typeof qrSVG === 'function') {
            const svg = qrSVG(link, 4);
            if (svg) {
                document.getElementById('invite-qr-svg').innerHTML = raw(svg).str;
                document.getElementById('invite-qr-wrap').style.display = '';
            }
        }
    }

    function copyInviteLink() {
        const input = document.getElementById('invite-link-val');
        const btn = document.getElementById('invite-copy-btn');
        const doCopy = () => {
            const prev = btn.textContent;
            btn.textContent = 'Copied!';
            setTimeout(() => { btn.textContent = prev; }, 2000);
        };
        if (navigator.clipboard) {
            navigator.clipboard.writeText(input.value).then(doCopy).catch(() => {
                input.select();
            });
        } else {
            input.select();
        }
    }

    // ── State accessors for applyRole in dashboard.js ─────────────────────────
    // applyRole needs to read/write the module-scoped load-once flags without
    // duplicating state. These thin wrappers keep the flags private to this IIFE.

    function getUsersLoaded() { return usersLoaded; }
    function setUsersLoaded(v) { usersLoaded = v; }
    function getRequestsLoaded() { return requestsLoaded; }
    function setRequestsLoaded(v) { requestsLoaded = v; }

    // ── Component registration ────────────────────────────────────────────────

    component('users', function (el, storeProxy) {
        function init() {
            // add-user-form submit handler
            const addForm = document.getElementById('add-user-form');
            if (addForm) {
                addForm.addEventListener('submit', async (e) => {
                    e.preventDefault();
                    const username = document.getElementById('new-username').value.trim();
                    const password = document.getElementById('new-password').value;
                    const errEl = document.getElementById('add-user-error');
                    errEl.classList.add('hidden');
                    try {
                        const resp = await fetch('/api/pelicula/users', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ username, password }),
                        });
                        if (!resp.ok) {
                            const data = await resp.json().catch(() => ({}));
                            errEl.textContent = data.error || 'Failed to create user.';
                            errEl.classList.remove('hidden');
                            return;
                        }
                        const createdUsername = username;
                        document.getElementById('new-username').value = '';
                        document.getElementById('new-password').value = '';
                        const successEl = document.getElementById('add-user-success');
                        if (successEl) {
                            successEl.innerHTML = html`User <strong>${createdUsername}</strong> created. <a href="/jellyfin/" target="_blank" style="color:#7dda93">Open Jellyfin &rarr;</a>`.str;
                            successEl.classList.remove('hidden');
                            setTimeout(() => successEl.classList.add('hidden'), 8000);
                        }
                        loadUsers();
                    } catch (e) {
                        errEl.textContent = 'Network error.';
                        errEl.classList.remove('hidden');
                    }
                });
            }
        }

        return {
            render: function() {},  // no template rendering — operates on existing DOM
            loadOnce: init,
        };
    });

    // ── Window exports (for onclick handlers and cross-file access) ───────────
    window.loadUsers             = loadUsers;
    window.loadSessions          = loadSessions;
    window.loadRequests          = loadRequests;
    window.loadInvites           = loadInvites;
    // loadArrMeta + saveRequestsSettings exported by settings.js (canonical owner)
    window.approveRequest        = approveRequest;
    window.denyRequest           = denyRequest;
    window.startResetPassword    = startResetPassword;
    window.cancelResetPassword   = cancelResetPassword;
    window.submitResetPassword   = submitResetPassword;
    window.startDeleteUser       = startDeleteUser;
    window.openInviteModal       = openInviteModal;
    window.closeInviteModal      = closeInviteModal;
    window.submitCreateInvite    = submitCreateInvite;
    window.copyInviteLink        = copyInviteLink;
    window.copyInviteItemLink    = copyInviteItemLink;
    window.revokeInvite          = revokeInvite;
    window.deleteInvite          = deleteInvite;
    // State accessors for applyRole in dashboard.js
    window._users_getUsersLoaded    = getUsersLoaded;
    window._users_setUsersLoaded    = setUsersLoaded;
    window._users_getRequestsLoaded = getRequestsLoaded;
    window._users_setRequestsLoaded = setRequestsLoaded;
    // arrMeta load-once guard lives in settings.js's window.loadArrMeta wrapper
}());
