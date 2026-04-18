// nginx/users.js
// Users component — registered with PeliculaFW; mounted by dashboard.js.
// Depends on: framework.js, api.js.

import { component, html, raw } from '/framework.js';
import { get, post, del } from '/api.js';

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
        const users = await get('/api/pelicula/users');
        if (!users) return;
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
            const disabledBadge = u.isDisabled
                ? '<span class="user-admin-badge" style="background:var(--danger-dim,#3a1a2a);color:var(--danger,#ff6b8a)">disabled</span>'
                : '';
            const disableBtn = html`<button class="user-action-btn" data-action="toggle-disable" data-disabled="${u.isDisabled ? 'true' : 'false'}" title="${u.isDisabled ? 'Re-enable account' : 'Disable account'}">${u.isDisabled ? 'Enable' : 'Disable'}</button>`.str;
            const moviesOn = u.enableAllFolders || false;
            const tvOn     = u.enableAllFolders || false;
            // TODO: when enableAllFolders is false, check u.enabledFolders to
            // pre-tick the correct boxes. Requires the frontend to know the Movies/
            // TV Shows folder IDs (not returned by the API yet). For now, partial
            // access shows both unchecked; saving applies the chosen coarse access.
            const libraryRow = html`<div class="user-library-row" style="font-size:0.8rem;padding:0.25rem 0;display:flex;gap:1rem;align-items:center"><label><input type="checkbox" class="user-lib-movies" data-action="save-library-access"${moviesOn ? ' checked' : ''}> Movies</label><label><input type="checkbox" class="user-lib-tv" data-action="save-library-access"${tvOn ? ' checked' : ''}> TV Shows</label></div>`.str;
            return html`<li data-user-id="${u.id}" data-user-name="${u.name}">
                <div class="user-info"><span class="user-name">${u.name}</span>${raw(adminBadge)}${raw(disabledBadge)}<span class="user-meta">last login: ${lastSeen}</span></div>
                <div class="user-actions">
                <button class="user-action-btn" data-action="start-reset-password" title="Reset password">Reset</button>
                ${raw(disableBtn)}
                <button class="user-action-btn user-action-delete" data-action="start-delete-user" title="Delete user">Delete</button>
                </div>
                ${raw(libraryRow)}
                <form class="user-reset-form hidden">
                <input type="password" class="user-reset-input" placeholder="New password" autocomplete="new-password">
                <button type="submit" class="user-action-btn">Set</button>
                <button type="button" class="user-action-btn" data-action="cancel-reset-password">Cancel</button>
                </form>
                <span class="users-error hidden"></span>
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
        await post('/api/pelicula/users/' + encodeURIComponent(id) + '/password', {password});
        cancelResetPassword(btn);
    } catch (e) {
        const errEl = li.querySelector('.users-error');
        if (errEl) { errEl.textContent = (e.body && e.body.error) || 'Failed to reset password.'; errEl.classList.remove('hidden'); }
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
        await del('/api/pelicula/users/' + encodeURIComponent(id));
        loadUsers();
    } catch (e) {
        const errEl = li.querySelector('.users-error');
        if (errEl) { errEl.textContent = (e.body && e.body.error) || 'Failed to delete ' + name + '.'; errEl.classList.remove('hidden'); }
        btn.disabled = false;
        btn.dataset.confirming = '';
        btn.textContent = 'Delete';
        btn.classList.remove('user-action-delete-confirm');
    }
}

async function saveLibraryAccess(checkbox) {
    const li    = checkbox.closest('li');
    const id    = li.dataset.userId;
    const movies = li.querySelector('.user-lib-movies').checked;
    const tv     = li.querySelector('.user-lib-tv').checked;
    const errEl  = li.querySelector('.users-error');
    if (errEl) errEl.classList.add('hidden');
    try {
        await post('/api/pelicula/users/' + encodeURIComponent(id) + '/library', {movies, tv});
    } catch (e) {
        if (errEl) { errEl.textContent = (e.body && e.body.error) || 'Failed to update library access.'; errEl.classList.remove('hidden'); }
    }
}

async function toggleDisableUser(btn) {
    const li        = btn.closest('li');
    const id        = li.dataset.userId;
    const isDisabled = btn.dataset.disabled === 'true';
    const action    = isDisabled ? 'enable' : 'disable';
    const errEl     = li.querySelector('.users-error');
    if (errEl) errEl.classList.add('hidden');
    btn.disabled = true;
    try {
        await post('/api/pelicula/users/' + encodeURIComponent(id) + '/' + action, {});
        loadUsers();
    } catch (e) {
        if (errEl) { errEl.textContent = (e.body && e.body.error) || 'Failed to ' + action + ' user.'; errEl.classList.remove('hidden'); }
        btn.disabled = false;
    }
}

// ── Sessions / Now Playing ─────────────────────────────────────────────────

async function loadSessions() {
    const list = document.getElementById('sessions-list');
    const section = document.getElementById('now-playing-section');
    if (!list || !section) return;
    try {
        const sessions = await get('/api/pelicula/sessions');
        if (!sessions) { section.classList.add('hidden'); return; }
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
        const requests = await get('/api/pelicula/requests');
        if (!requests) return;
        renderRequests(requests || []);
    } catch (e) { console.warn('[pelicula] loadRequests error', e); }
}

function renderRequests(requests) {
    const isAdmin = document.body.dataset.role === 'admin';
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
                    <button class="request-btn request-btn-approve" data-action="approve-request">Approve</button>
                    <button class="request-btn request-btn-deny" data-action="deny-request">Deny</button>
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
        await post('/api/pelicula/requests/' + id + '/approve', {});
        await loadRequests();
    } catch (e) {
        const li = btn ? btn.closest('li') : null;
        const errEl = li ? li.querySelector('.users-error') : null;
        if (errEl) { errEl.textContent = 'Approve failed: ' + ((e.body && e.body.error) || e.status || 'error'); errEl.classList.remove('hidden'); }
        if (btn) { btn.disabled = false; btn.textContent = 'Approve'; }
    }
}

async function denyRequest(id) {
    const reason = prompt('Reason for denial (optional):') ?? null;
    if (reason === null) return; // cancelled
    try {
        await post('/api/pelicula/requests/' + id + '/deny', {reason});
        await loadRequests();
    } catch (e) { alert('Deny failed: ' + ((e.body && e.body.error) || e.status || 'error')); }
}

// ── Arr-meta for admin request settings dropdowns ──────────────────────────
// Canonical implementations live in settings.js — called here via window.*.

// ── Invites ───────────────────────────────────────────────────────────────

async function loadInvites() {
    const list = document.getElementById('invites-list');
    if (!list) return;
    try {
        const invites = await get('/api/pelicula/invites');
        if (!invites) { list.innerHTML = ''; return; }
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
            const copyBtn = isActive ? html`<button class="user-action-btn" data-action="copy-invite" data-link="${link}" title="Copy invite link">Copy link</button>`.str : '';
            const revokeBtn = isActive ? html`<button class="user-action-btn" data-action="revoke-invite" title="Deactivate this invite">Revoke</button>`.str : '';
            return html`<li class="invite-item" data-token="${inv.token}">
                <div class="invite-row">
                <span class="invite-badge ${stateClass}">${stateLabel}</span>
                <span class="invite-meta">${uses} use${inv.uses !== 1 ? 's' : ''} \u00b7 ${expiry}</span>
                ${raw(labelSpan)}
                </div>
                <div class="invite-actions">
                ${raw(copyBtn)}${raw(revokeBtn)}
                <button class="user-action-btn user-action-delete" data-action="delete-invite" title="Delete record">Delete</button>
                </div>
                <span class="invite-error hidden" style="font-size:0.75rem;color:var(--danger,#ff6b8a);padding:0.2rem 0;display:block"></span>
            </li>`.str;
        }).join('');
    } catch (e) {
        console.warn('[pelicula] loadInvites error:', e);
    }
}

function copyInviteItemLink(btn) {
    const link = btn.dataset.link;
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
        await post('/api/pelicula/invites/' + encodeURIComponent(token) + '/revoke', {});
        loadInvites();
    } catch (e) {
        const errEl2 = li.querySelector('.invite-error');
        if (errEl2) { errEl2.textContent = (e.body && e.body.error) || 'Failed to revoke invite.'; errEl2.classList.remove('hidden'); }
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
        await del('/api/pelicula/invites/' + encodeURIComponent(token));
        loadInvites();
    } catch (e) {
        const errEl3 = li.querySelector('.invite-error');
        if (errEl3) { errEl3.textContent = (e.body && e.body.error) || 'Failed to delete invite.'; errEl3.classList.remove('hidden'); }
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
        const data = await post('/api/pelicula/invites', body);
        showInviteShareStep(data);
    } catch (e) {
        errEl.textContent = (e.body && e.body.error) || 'Failed to create invite.';
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
    if (typeof window.qrSVG === 'function') {
        const svg = window.qrSVG(link, 4);
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
// duplicating state. These thin wrappers keep the flags private to this module.

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
                    await post('/api/pelicula/users', {username, password});
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
                    errEl.textContent = (e.body && e.body.error) || 'Failed to create user.';
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

async function loadOperators() {
    const list   = document.getElementById('operators-list');
    const empty  = document.getElementById('operators-empty');
    const errEl  = document.getElementById('operators-error');
    if (!list) return;
    try {
        const [usersResult, rolesResult] = await Promise.allSettled([
            get('/api/pelicula/users'),
            get('/api/pelicula/operators'),
        ]);
        if (usersResult.status === 'rejected' || rolesResult.status === 'rejected') {
            if (errEl) { errEl.textContent = 'Failed to load operators.'; errEl.classList.remove('hidden'); }
            return;
        }
        const users = usersResult.value;
        const roles = rolesResult.value;
        if (!users || !roles) {
            if (errEl) { errEl.textContent = 'Failed to load operators.'; errEl.classList.remove('hidden'); }
            return;
        }
        const roleMap = {};
        (roles || []).forEach(r => { roleMap[r.jellyfin_id] = r.role; });

        if (!users || users.length === 0) {
            if (empty) empty.classList.remove('hidden');
            list.replaceChildren();
            return;
        }
        if (empty) empty.classList.add('hidden');

        const frag = document.createDocumentFragment();
        users.forEach(u => {
            const li = document.createElement('li');
            li.dataset.userId   = u.id;
            li.dataset.userName = u.name;
            li.className = 'users-list-item';

            const info = document.createElement('div');
            info.className = 'user-info';
            const nameSpan = document.createElement('span');
            nameSpan.className = 'user-name';
            nameSpan.textContent = u.name;
            info.appendChild(nameSpan);

            const actions = document.createElement('div');
            actions.className = 'user-actions';
            actions.style.gap = '0.5rem';

            const select = document.createElement('select');
            select.className = 'operator-role-select';
            const currentRole = roleMap[u.id] || '';
            [['', '\u2014 no access \u2014'], ['viewer', 'viewer'], ['manager', 'manager'], ['admin', 'admin']].forEach(([val, label]) => {
                const opt = document.createElement('option');
                opt.value = val;
                opt.textContent = label;
                if (currentRole === val) opt.selected = true;
                select.appendChild(opt);
            });
            select.addEventListener('change', () => setOperatorRole(select));
            actions.appendChild(select);

            if (currentRole) {
                const removeBtn = document.createElement('button');
                removeBtn.className = 'user-action-btn user-action-delete';
                removeBtn.title = 'Remove role';
                removeBtn.textContent = 'Remove';
                removeBtn.addEventListener('click', () => removeOperator(removeBtn));
                actions.appendChild(removeBtn);
            }

            const errSpan = document.createElement('span');
            errSpan.className = 'users-error hidden';

            li.appendChild(info);
            li.appendChild(actions);
            li.appendChild(errSpan);
            frag.appendChild(li);
        });
        list.replaceChildren(frag);
    } catch (e) {
        if (errEl) { errEl.textContent = 'Network error loading operators.'; errEl.classList.remove('hidden'); }
    }
}

async function setOperatorRole(select) {
    const li    = select.closest('li');
    const id    = li.dataset.userId;
    const name  = li.dataset.userName;
    const role  = select.value;
    const errEl = li.querySelector('.users-error');
    if (errEl) errEl.classList.add('hidden');
    if (!role) {
        await doRemoveOperator(id, name, li);
        return;
    }
    try {
        await post('/api/pelicula/operators/' + encodeURIComponent(id), {role, username: name});
        loadOperators();
    } catch (e) {
        if (errEl) { errEl.textContent = (e.body && e.body.error) || 'Failed to set role.'; errEl.classList.remove('hidden'); }
    }
}

async function removeOperator(btn) {
    const li = btn.closest('li');
    await doRemoveOperator(li.dataset.userId, li.dataset.userName, li);
}

async function doRemoveOperator(id, name, li) {
    const errEl = li ? li.querySelector('.users-error') : null;
    try {
        await del('/api/pelicula/operators/' + encodeURIComponent(id));
        loadOperators();
    } catch (e) {
        if (errEl) { errEl.textContent = (e.body && e.body.error) || 'Failed to remove role for ' + name + '.'; errEl.classList.remove('hidden'); }
    }
}

// ── Window exports (cross-file access from dashboard.js applyRole) ───────────
window.loadUsers     = loadUsers;
window.loadSessions  = loadSessions;
window.loadRequests  = loadRequests;
window.loadInvites   = loadInvites;
window.loadOperators = loadOperators;
window._users_getUsersLoaded    = getUsersLoaded;
window._users_setUsersLoaded    = setUsersLoaded;
window._users_getRequestsLoaded = getRequestsLoaded;
window._users_setRequestsLoaded = setRequestsLoaded;

// ── Invite modal button listeners ────────────────────────────────────────────
document.getElementById('invite-open-btn').addEventListener('click', openInviteModal);
document.getElementById('invite-cancel-btn').addEventListener('click', closeInviteModal);
document.getElementById('invite-create-btn').addEventListener('click', submitCreateInvite);
document.getElementById('invite-copy-btn').addEventListener('click', copyInviteLink);
document.getElementById('invite-done-btn').addEventListener('click', closeInviteModal);

// ── Event delegation for dynamically-rendered user/request/invite lists ──────
document.getElementById('users-list').addEventListener('click', e => {
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    const action = btn.dataset.action;
    if (action === 'toggle-disable') toggleDisableUser(btn);
    else if (action === 'start-reset-password') startResetPassword(btn);
    else if (action === 'start-delete-user') startDeleteUser(btn);
    else if (action === 'cancel-reset-password') cancelResetPassword(btn);
});

document.getElementById('users-list').addEventListener('change', e => {
    if (e.target.dataset.action === 'save-library-access') saveLibraryAccess(e.target);
});

document.getElementById('users-list').addEventListener('submit', e => {
    e.preventDefault();
    submitResetPassword(e.target);
});

document.getElementById('requests-pending-list').addEventListener('click', e => {
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    const id = btn.closest('li')?.dataset.id;
    if (!id) return;
    if (btn.dataset.action === 'approve-request') approveRequest(id);
    else if (btn.dataset.action === 'deny-request') denyRequest(id);
});

document.getElementById('invites-list').addEventListener('click', e => {
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    const action = btn.dataset.action;
    if (action === 'copy-invite') copyInviteItemLink(btn);
    else if (action === 'revoke-invite') revokeInvite(btn);
    else if (action === 'delete-invite') deleteInvite(btn);
});
