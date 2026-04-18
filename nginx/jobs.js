// jobs.js — Jobs tab: every procula job grouped by state
'use strict';

import { get } from '/api.js';
import { onTab } from '/framework.js';

const jobsState = { loaded: false, loading: false };

async function loadJobs() {
    if (jobsState.loading) return;
    jobsState.loading = true;
    const root = document.getElementById('jobs-groups');
    if (!root) { jobsState.loading = false; return; }
    root.replaceChildren(makeMsg('Loading\u2026'));
    try {
        const data = await get('/api/pelicula/jobs');
        if (data === null) { root.replaceChildren(makeMsg('Session expired.')); return; }
        renderJobs(root, data.groups || {});
        jobsState.loaded = true;
    } catch (e) {
        root.replaceChildren(makeMsg('Failed to load jobs: ' + e.message, true));
    } finally {
        jobsState.loading = false;
    }
}

function renderJobs(root, groups) {
    const order = ['processing', 'queued', 'failed', 'cancelled', 'completed'];
    const frag = document.createDocumentFragment();
    for (const state of order) {
        const jobs = groups[state] || [];
        if (!jobs.length) continue;
        frag.appendChild(renderGroup(state, jobs));
    }
    if (!frag.childNodes.length) {
        frag.appendChild(makeMsg('No jobs.'));
    }
    root.replaceChildren(frag);
}

function renderGroup(state, jobs) {
    const wrap = document.createElement('details');
    wrap.className = 'jobs-group jobs-group-' + state;
    wrap.open = (state === 'processing' || state === 'failed');

    const summary = document.createElement('summary');
    summary.className = 'jobs-group-header';
    summary.textContent = state + ' (' + jobs.length + ')';
    wrap.appendChild(summary);

    for (const j of jobs) {
        wrap.appendChild(renderJobRow(j));
    }
    return wrap;
}

function renderJobRow(j) {
    const row = document.createElement('div');
    row.className = 'jobs-row';
    row.dataset.id = j.id;

    const title = document.createElement('div');
    title.className = 'jobs-row-title';
    const src = j.source || {};
    title.textContent = src.title || src.path || j.id;

    const meta = document.createElement('div');
    meta.className = 'jobs-row-meta';
    const parts = [];
    if (j.stage) parts.push('stage: ' + j.stage);
    if (j.action_type && j.action_type !== 'pipeline') parts.push(j.action_type);
    if (typeof j.progress === 'number') parts.push(Math.round(j.progress * 100) + '%');
    if (j.error) parts.push('error: ' + j.error);
    meta.textContent = parts.join(' \u00b7 ');

    row.appendChild(title);
    row.appendChild(meta);
    return row;
}

function makeMsg(text, isError) {
    const div = document.createElement('div');
    div.className = 'no-items';
    div.style.color = isError ? 'var(--danger)' : 'var(--muted)';
    div.textContent = text;
    return div;
}

document.getElementById('jobs-refresh-btn').addEventListener('click', () => { jobsState.loaded = false; loadJobs(); });

onTab('jobs', function () {
    if (!jobsState.loaded) loadJobs();
});

if (document.body && document.body.dataset.tab === 'jobs') loadJobs();
