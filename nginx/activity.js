// nginx/activity.js
// Activity feed component -- registered with PeliculaFW; mounted by dashboard.js.
//
// Security: all event data is passed through PeliculaFW's html tagged template, which
// auto-escapes string interpolations. raw() is only used for static icon HTML entity strings.
'use strict';

import { component, html, raw, toast, router } from '/framework.js';
import { get, del, post } from '/api.js';

// 24 hours -- boundary between "active" and "older" tiers
const ACTIVE_MS = 24 * 60 * 60 * 1000;

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

function formatTime(ts) {
    try {
        const diff = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
        if (diff < 60) return 'just now';
        if (diff < 3600) return Math.floor(diff / 60) + 'm ago';
        if (diff < 86400) return Math.floor(diff / 3600) + 'h ago';
        return Math.floor(diff / 86400) + 'd ago';
    } catch { return ''; }
}

function buildDrawer(e) {
    const actions = [];
    if (e.type === 'validation_failed' || e.type === 'transcode_failed') {
        if (e.job_id) {
            actions.push(html`<button class="act-btn act-btn-primary" data-action="act-retry" data-job-id="${e.job_id}">Retry</button>`);
            actions.push(html`<button class="act-btn" data-action="act-jump" data-job-id="${e.job_id}">Jump to job</button>`);
        }
    } else if (e.type === 'storage_warning' || e.type === 'storage_critical') {
        actions.push(html`<button class="act-btn act-btn-primary" data-action="act-go-storage">Go to storage</button>`);
    }
    actions.push(html`<button class="act-btn" data-action="act-dismiss" data-id="${e.id}">Dismiss</button>`);

    const detail = e.detail ? html`<div class="act-detail">${e.detail}</div>` : raw('');
    return html`<div class="act-drawer">${detail}<div class="act-actions">${actions}</div></div>`;
}

function buildRow(e) {
    return html`<div class="act-item ${notifClass(e.type)}" data-action="act-toggle-drawer">
        <div class="act-row">
            <span class="act-icon">${raw(notifIcon(e.type))}</span>
            <span class="act-msg">${e.message}</span>
            <span class="act-time">${formatTime(e.timestamp)}</span>
            <button class="act-x" title="Dismiss" data-action="act-dismiss" data-id="${e.id}">&#10005;</button>
        </div>
        ${buildDrawer(e)}
    </div>`;
}

function renderActivity(events) {
    const list = document.getElementById('activity-list');
    if (!list) return;

    if (!Array.isArray(events) || !events.length) {
        list.innerHTML = html`<div class="act-empty">No recent activity yet.</div>`.str;
        return;
    }

    const now = Date.now();
    const active = events.filter(e => now - new Date(e.timestamp).getTime() <= ACTIVE_MS);
    const older  = events.filter(e => now - new Date(e.timestamp).getTime() >  ACTIVE_MS);

    let out = active.map(e => buildRow(e).str).join('');

    if (older.length > 0) {
        const label = older.length + ' older event' + (older.length !== 1 ? 's' : '');
        out += html`<div class="act-sep" id="act-sep" data-action="act-toggle-older">
            <span class="act-sep-line"></span>
            <span>${label}</span>
            <span class="act-sep-chevron" id="act-chevron">&#9660;</span>
            <span class="act-sep-line"></span>
        </div>`.str;
        out += html`<div class="act-older" id="act-older">${older.map(e => buildRow(e))}</div>`.str;
    }

    list.innerHTML = out;
}

// -- Action handlers ------------------------------------------------------

function actToggleDrawer(item) {
    const drawer = item.querySelector('.act-drawer');
    if (drawer) drawer.classList.toggle('open');
}

function actToggleOlder() {
    const older   = document.getElementById('act-older');
    const chevron = document.getElementById('act-chevron');
    if (older)   older.classList.toggle('visible');
    if (chevron) chevron.classList.toggle('open');
}

async function actDismiss(id) {
    try {
        await del('/api/pelicula/notifications/' + id);
    } catch { toast('Could not dismiss', { error: true }); return; }
    try {
        const events = await get('/api/pelicula/notifications');
        if (events === null) return;
        if (window.renderNotifications) window.renderNotifications(events);
        renderActivity(events);
    } catch (e) { console.warn('[activity] dismiss refresh error:', e); }
}

async function actRetry(jobId) {
    try {
        await post('/api/pelicula/procula/jobs/' + jobId + '/retry', {});
        toast('Job queued for retry');
    } catch { toast('Retry failed', { error: true }); }
}

function actJumpToJob(jobId) {
    router.navigate('jobs', { id: jobId });
}

function actGoToStorage() {
    router.navigate('storage');
}

// -- Component registration -----------------------------------------------

component('activity', function () {
    return { render: function () {} };
});

// -- Event delegation on activity list ------------------------------------
document.getElementById('activity-list').addEventListener('click', e => {
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    const action = btn.dataset.action;
    if (action === 'act-dismiss') actDismiss(btn.dataset.id);
    else if (action === 'act-retry') actRetry(btn.dataset.jobId);
    else if (action === 'act-jump') actJumpToJob(btn.dataset.jobId);
    else if (action === 'act-go-storage') actGoToStorage();
    else if (action === 'act-toggle-drawer') actToggleDrawer(btn);
    else if (action === 'act-toggle-older') actToggleOlder();
});

// -- Window exports -------------------------------------------------------
// renderActivity is called by sse.js and dashboard.js refresh cycle.
window.renderActivity = renderActivity;
