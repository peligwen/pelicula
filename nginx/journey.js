// nginx/journey.js
// Shared per-title "journey" module — consumed by catalog.js (drawer),
// search.js (post-add status lines), and users.js (viewer request status
// lines). Wraps GET /api/pelicula/journey (see docs/API.md) behind three
// small, framework-idiomatic helpers so every surface renders the same
// canonical six-stage rail and the same compact one-liner from one place.

'use strict';

import { html, raw } from '/framework.js';
import { get, APIError } from '/api.js';

// ── Fetch ─────────────────────────────────────────────────────────────────
// fetchJourney(params) accepts either {type, tmdbId, tvdbId} (type is
// 'movie'|'series'; movies key on tmdbId, series on tvdbId — the other is
// simply omitted from the query) or {arrType, arrId} ('radarr'|'sonarr' +
// the *arr internal id). Returns the parsed journey response, or null when
// the backend has nothing to say about this title: a 401 (session expired —
// api.js's get() already resolves that to null) or a 404 ("title not
// found", per docs/API.md — an expected, non-error outcome for a title that
// hasn't been searched/requested/added yet). Any other non-2xx status is a
// real failure and rethrows, matching api.js's own get() semantics for
// every other 4xx/5xx.
export async function fetchJourney(params) {
    params = params || {};
    const qs = new URLSearchParams();
    if (params.arrType) {
        qs.set('arr_type', params.arrType);
        qs.set('arr_id', String(params.arrId || 0));
    } else {
        if (params.type) qs.set('type', params.type);
        if (params.tmdbId) qs.set('tmdb_id', String(params.tmdbId));
        if (params.tvdbId) qs.set('tvdb_id', String(params.tvdbId));
    }
    try {
        return await get('/api/pelicula/journey?' + qs.toString());
    } catch (e) {
        if (e instanceof APIError && e.status === 404) return null;
        throw e;
    }
}

// ── Shared stage metadata ────────────────────────────────────────────────
// Canonical six-stage rail order, mirrored from middleware/internal/app/journey/handler.go.
const STAGE_LABELS = {
    requested:   'Requested',
    approved:    'Approved',
    searching:   'Searching',
    downloading: 'Downloading',
    processing:  'Processing',
    available:   'Available',
};

function fmtAt(iso) {
    if (!iso) return '';
    const d = new Date(iso);
    if (isNaN(d.getTime())) return '';
    return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
        + ' ' + d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
}

function fmtEta(sec) {
    if (!sec || sec <= 0) return '';
    const h = Math.floor(sec / 3600);
    const m = Math.round((sec % 3600) / 60);
    if (h > 0) return h + 'h ' + m + 'm left';
    if (m > 0) return m + 'm left';
    return Math.round(sec) + 's left';
}

// ── Full stage rail (drawers) ────────────────────────────────────────────
// renderJourneyHtml(j) returns an html`` result (pass straight to setHTML).
// Renders every non-"skipped" entry from j.stages in rail order, with a
// status modifier class (journey-step-done/active/pending — "skipped"
// entries never reach this point, so no modifier class exists for them) and
// a small meta line: percent + detail on an active stage, "at"/"by" on a
// done request-derived stage (requested/approved), an ETA when the backend
// supplied one. A trailing muted note lists any degraded upstreams.
export function renderJourneyHtml(j) {
    if (!j || !Array.isArray(j.stages)) {
        return html`<div class="journey-unavailable">Journey unavailable.</div>`;
    }
    const steps = j.stages
        .filter(s => s.status !== 'skipped')
        .map(s => {
            const label = STAGE_LABELS[s.stage] || s.stage;
            const metaParts = [];
            if (s.status === 'active' && typeof s.progress === 'number') {
                metaParts.push(Math.round(s.progress * 100) + '%');
            }
            if (s.detail) metaParts.push(s.detail);
            if (s.status === 'active' && s.eta) {
                const eta = fmtEta(s.eta);
                if (eta) metaParts.push(eta);
            }
            if (s.at) {
                const at = fmtAt(s.at);
                if (at) metaParts.push(s.by ? at + ' · by ' + s.by : at);
            }
            const meta = metaParts.join(' · ');
            return html`<div class="journey-step journey-step-${s.status}">
                <span class="journey-step-label">${label}</span>
                ${meta ? html`<span class="journey-step-meta">${meta}</span>` : raw('')}
            </div>`;
        });

    const degradedNote = (Array.isArray(j.degraded) && j.degraded.length)
        ? html`<div class="journey-degraded-note">⚠ ${j.degraded.join(', ')} unavailable — this view may be incomplete.</div>`
        : raw('');

    return html`<div class="journey-rail">${steps}</div>${degradedNote}`;
}

// ── Compact one-liner (cards) ─────────────────────────────────────────────
// journeyStatusLine(j) — a short status string for a search-result or
// request card: "downloading 45%", "processing — transcode", "searching…",
// "available". Falls back to the bare stage name for requested/approved (no
// card surface shows those two — they're pre-add/pre-approve — but this
// keeps the function total rather than throwing on an unexpected shape).
export function journeyStatusLine(j) {
    if (!j) return '';
    const stage = j.current_stage;
    const s = (Array.isArray(j.stages) ? j.stages : []).find(st => st.stage === stage);
    switch (stage) {
        case 'available':
            return 'available';
        case 'downloading': {
            const progress = (s && typeof s.progress === 'number') ? s.progress
                : (typeof j.progress === 'number' ? j.progress : null);
            return progress != null ? 'downloading ' + Math.round(progress * 100) + '%' : 'downloading';
        }
        case 'processing':
            return (s && s.detail) ? 'processing — ' + s.detail : 'processing';
        case 'searching':
            return 'searching…';
        default:
            return stage ? (STAGE_LABELS[stage] || stage).toLowerCase() : '';
    }
}
