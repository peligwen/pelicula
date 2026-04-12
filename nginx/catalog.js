// catalog.js — Catalog tab: library browser with per-item action menu
(function () {
'use strict';

// ── State ───────────────────────────────────────────────────────────────────
const catState = {
    items: [],
    query: '',
    type: '',
    loaded: false,
    loading: false,
    registry: null,
    registryExpires: 0,
    flagsByPath: {},
    flaggedRows: [],
};
const REGISTRY_TTL = 60_000;

// ── Helpers ─────────────────────────────────────────────────────────────────
function catFetch(url, opts) {
    return fetch(url, Object.assign({ credentials: 'same-origin' }, opts || {}));
}

function catToast(msg, isError) {
    if (typeof showAdminToast === 'function') {
        showAdminToast(msg, isError);
        return;
    }
    console.info('[catalog]', isError ? 'error' : 'info', msg);
}

// ── Action registry ──────────────────────────────────────────────────────────
async function loadActionRegistry() {
    const now = Date.now();
    if (catState.registry && now < catState.registryExpires) return catState.registry;
    try {
        const res = await catFetch('/api/pelicula/actions/registry');
        if (!res.ok) return catState.registry || [];
        const data = await res.json();
        catState.registry = Array.isArray(data) ? data : [];
        catState.registryExpires = now + REGISTRY_TTL;
    } catch (e) {
        catState.registry = catState.registry || [];
    }
    return catState.registry;
}

// ── Catalog load ─────────────────────────────────────────────────────────────
async function loadCatalog() {
    if (catState.loading) return;
    catState.loading = true;
    const list = document.getElementById('cat-list');
    if (list && !catState.loaded) {
        const loading = document.createElement('div');
        loading.className = 'no-items';
        loading.textContent = 'Loading\u2026';
        list.replaceChildren(loading);
    }
    try {
        const params = new URLSearchParams();
        if (catState.query) params.set('q', catState.query);
        if (catState.type) params.set('type', catState.type);
        const res = await catFetch('/api/pelicula/catalog?' + params);
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const data = await res.json();
        catState.items = [...(data.movies || []), ...(data.series || [])];
        catState.loaded = true;
        renderCatalog();
    } catch (e) {
        if (list) {
            const err = document.createElement('div');
            err.className = 'no-items';
            err.textContent = 'Failed to load catalog. Is the stack running?';
            list.replaceChildren(err);
        }
    } finally {
        catState.loading = false;
    }
}

// ── Flags / Needs Attention ──────────────────────────────────────────────────
async function loadFlags() {
    try {
        const res = await catFetch('/api/pelicula/catalog/flags');
        if (!res.ok) return;
        const data = await res.json();
        const rows = Array.isArray(data.rows) ? data.rows : [];
        const byPath = {};
        for (const r of rows) byPath[r.path] = r;
        catState.flagsByPath = byPath;
        catState.flaggedRows = rows;
        renderAttention();
    } catch (e) {
        console.warn('[catalog] flag fetch failed', e);
    }
}

function renderAttention() {
    const wrap = document.getElementById('cat-attention');
    const list = document.getElementById('cat-attention-list');
    const count = document.getElementById('cat-attention-count');
    if (!wrap || !list) return;
    // Only error-severity rows promote to the attention section.
    const rows = catState.flaggedRows.filter(r => r.severity === 'error');
    if (!rows.length) {
        wrap.style.display = 'none';
        return;
    }
    wrap.style.display = '';
    if (count) count.textContent = String(rows.length);
    const frag = document.createDocumentFragment();
    for (const row of rows) {
        frag.appendChild(renderAttentionRow(row));
    }
    list.replaceChildren(frag);
}

function renderAttentionRow(row) {
    const div = document.createElement('div');
    div.className = 'cat-attention-row';
    div.addEventListener('click', () => openDetail(row.path));

    const title = document.createElement('span');
    title.className = 'cat-row-title';
    title.textContent = row.path.split('/').slice(-1)[0] || row.path;
    title.title = row.path;

    const pills = document.createElement('span');
    for (const f of (row.flags || [])) {
        const pill = document.createElement('span');
        pill.className = 'cat-pill cat-pill-' + (f.severity || 'info');
        pill.textContent = f.code;
        if (f.detail) pill.title = f.detail;
        pills.appendChild(pill);
    }

    div.appendChild(title);
    div.appendChild(pills);
    return div;
}

// ── Detail drawer ────────────────────────────────────────────────────────────
async function openDetail(path) {
    if (!path) return;
    const backdrop = document.getElementById('cat-drawer-backdrop');
    const drawer = document.getElementById('cat-drawer');
    const title = document.getElementById('cat-drawer-title');
    const sub = document.getElementById('cat-drawer-sub');
    const body = document.getElementById('cat-drawer-body');
    if (!drawer) return;
    backdrop.classList.remove('hidden');
    drawer.classList.remove('hidden');
    title.textContent = path.split('/').slice(-1)[0] || 'Details';
    sub.textContent = path;
    body.replaceChildren(makeTextNode('Loading\u2026', 'var(--muted)'));

    try {
        const res = await catFetch('/api/pelicula/catalog/detail?path=' + encodeURIComponent(path));
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const data = await res.json();
        body.replaceChildren(renderDetail(data));
    } catch (e) {
        body.replaceChildren(makeTextNode('Failed to load details: ' + e.message, 'var(--danger)'));
    }
}

window.catCloseDetail = function () {
    document.getElementById('cat-drawer-backdrop').classList.add('hidden');
    document.getElementById('cat-drawer').classList.add('hidden');
};

function makeTextNode(text, color) {
    const span = document.createElement('div');
    span.style.color = color || 'var(--text)';
    span.style.padding = '1rem 0';
    span.textContent = text;
    return span;
}

function renderDetail(data) {
    const root = document.createElement('div');

    // Section: Flags
    if (Array.isArray(data.flags) && data.flags.length) {
        root.appendChild(sectionTitle('Flags'));
        const wrap = document.createElement('div');
        for (const f of data.flags) {
            const p = document.createElement('span');
            p.className = 'cat-pill cat-pill-' + (f.severity || 'info');
            p.textContent = f.code;
            if (f.detail) p.title = f.detail;
            wrap.appendChild(p);
        }
        root.appendChild(wrap);
    }

    const job = data.job || {};
    const val = job.validation || null;
    const codecs = val && val.checks && val.checks.codecs;

    // Section: Encoding
    root.appendChild(sectionTitle('Encoding'));
    const enc = document.createElement('div');
    if (codecs) {
        enc.appendChild(pill('video: ' + (codecs.video || '?'), 'cat-pill-encoding'));
        enc.appendChild(pill('audio: ' + (codecs.audio || '?'), 'cat-pill-encoding'));
        if (codecs.width && codecs.height) {
            enc.appendChild(pill(codecs.width + 'x' + codecs.height, 'cat-pill-encoding'));
        }
    } else {
        enc.appendChild(makeTextNode('No codec info yet.', 'var(--muted)'));
    }
    root.appendChild(enc);

    // Section: Subtitles
    root.appendChild(sectionTitle('Subtitles'));
    const subs = document.createElement('div');
    const embedded = codecs && Array.isArray(codecs.subtitles) ? codecs.subtitles : [];
    if (embedded.length) {
        for (const lang of embedded) subs.appendChild(pill(lang, 'cat-pill-subs'));
    } else {
        subs.appendChild(makeTextNode('No embedded subtitle tracks.', 'var(--muted)'));
    }
    const missing = Array.isArray(job.missing_subs) ? job.missing_subs : [];
    if (missing.length) {
        const label = document.createElement('div');
        label.style.marginTop = '0.5rem';
        label.style.color = 'var(--muted)';
        label.textContent = 'Missing:';
        subs.appendChild(label);
        for (const lang of missing) subs.appendChild(pill(lang, 'cat-pill-warn'));
    }
    root.appendChild(subs);

    // Section: Status
    root.appendChild(sectionTitle('Status'));
    const status = document.createElement('div');
    if (val && val.checks) {
        for (const k of ['integrity', 'duration', 'sample']) {
            const v = val.checks[k] || 'skip';
            const cls = v === 'pass' ? 'cat-pill-status-pass'
                : v === 'fail' ? 'cat-pill-status-fail'
                : 'cat-pill';
            status.appendChild(pill(k + ': ' + v, cls));
        }
    }
    if (job.transcode_decision) status.appendChild(pill('transcode: ' + job.transcode_decision, 'cat-pill'));
    if (job.catalog && job.catalog.jellyfin_synced) status.appendChild(pill('jellyfin synced', 'cat-pill-status-pass'));
    if (job.error) {
        const err = document.createElement('div');
        err.className = 'drawer-error';
        err.textContent = job.error;
        status.appendChild(err);
    }
    root.appendChild(status);

    return root;
}

function sectionTitle(text) {
    const div = document.createElement('div');
    div.className = 'drawer-section-title';
    div.textContent = text;
    return div;
}

function pill(text, cls) {
    const span = document.createElement('span');
    span.className = 'cat-pill ' + (cls || '');
    span.textContent = text;
    return span;
}

// ── Render ────────────────────────────────────────────────────────────────────
function renderCatalog() {
    const list = document.getElementById('cat-list');
    if (!list) return;
    if (!catState.items.length) {
        const empty = document.createElement('div');
        empty.className = 'no-items';
        empty.textContent = 'No items found.';
        list.replaceChildren(empty);
        return;
    }
    const frag = document.createDocumentFragment();
    for (const item of catState.items) {
        const isSeries = Array.isArray(item.seasons);
        frag.appendChild(isSeries ? renderSeriesRow(item) : renderMovieRow(item));
    }
    list.replaceChildren(frag);
}

function makeActions(...btns) {
    const div = document.createElement('div');
    div.className = 'cat-row-actions';
    btns.forEach(b => div.appendChild(b));
    return div;
}

function makeCtxBtn(item, level) {
    const btn = document.createElement('button');
    btn.className = 'cat-ctx-btn';
    btn.textContent = '\u22ef'; // ⋯
    btn.title = 'Actions';
    btn.addEventListener('click', (e) => { e.stopPropagation(); openContextMenu(e, item, level); });
    return btn;
}

function makeExpandBtn() {
    const btn = document.createElement('button');
    btn.className = 'cat-expand-btn';
    btn.textContent = '\u25b6'; // ▶
    btn.title = 'Expand';
    return btn;
}

function renderMovieRow(item) {
    const div = document.createElement('div');
    div.className = 'cat-row cat-row-movie';
    div.dataset.id = item.id;

    const title = document.createElement('span');
    title.className = 'cat-row-title';
    title.textContent = item.title || '(untitled)';
    title.title = item.title || '';

    const meta = document.createElement('span');
    meta.className = 'cat-row-meta';
    const parts = [];
    if (item.year) parts.push(item.year);
    if (item.sizeOnDisk) parts.push(fmtSize(item.sizeOnDisk));
    meta.textContent = parts.join(' \u00b7 ');

    div.appendChild(title);
    div.appendChild(meta);
    div.appendChild(makeActions(makeCtxBtn(item, 'movie')));
    div.addEventListener('click', (e) => {
        if (e.target.closest('.cat-ctx-btn')) return;
        const path = item.movieFile ? item.movieFile.path : '';
        if (path) openDetail(path);
    });
    div.addEventListener('contextmenu', (e) => {
        e.preventDefault();
        openSubRequest({
            label: item.title || 'Movie',
            arrType: 'radarr',
            arrID: item.id,
            episodeID: 0,
        });
    });
    return div;
}

function renderSeriesRow(item) {
    const div = document.createElement('div');
    div.className = 'cat-row cat-row-series';
    div.dataset.id = item.id;

    const title = document.createElement('span');
    title.className = 'cat-row-title';
    title.textContent = item.title || '(untitled)';
    title.title = item.title || '';

    const meta = document.createElement('span');
    meta.className = 'cat-row-meta';
    const parts = [];
    if (item.year) parts.push(item.year);
    if (item.statistics && item.statistics.sizeOnDisk) parts.push(fmtSize(item.statistics.sizeOnDisk));
    meta.textContent = parts.join(' \u00b7 ');

    const expandBtn = makeExpandBtn();
    expandBtn.addEventListener('click', (e) => { e.stopPropagation(); toggleSeries(div, item); });

    div.addEventListener('click', (e) => {
        if (!e.target.closest('.cat-ctx-btn') && !e.target.closest('.cat-ctx-menu')) toggleSeries(div, item);
    });

    div.appendChild(title);
    div.appendChild(meta);
    div.appendChild(makeActions(expandBtn, makeCtxBtn(item, 'series')));
    return div;
}

// ── Series expansion ─────────────────────────────────────────────────────────
async function toggleSeries(rowEl, series) {
    if (rowEl.classList.contains('expanded')) {
        collapseSiblings(rowEl, ['cat-row-season', 'cat-row-episode']);
        rowEl.classList.remove('expanded');
        return;
    }
    rowEl.classList.add('expanded');
    let seasons = [];
    try {
        const res = await catFetch('/api/pelicula/catalog/series/' + series.id);
        if (res.ok) {
            const d = await res.json();
            seasons = (d.seasons || []).filter(s => s.seasonNumber > 0).sort((a, b) => a.seasonNumber - b.seasonNumber);
        }
    } catch (e) { /* non-critical */ }
    let insertAfter = rowEl;
    for (const season of seasons) {
        const el = renderSeasonRow(series, season);
        insertAfter.after(el);
        insertAfter = el;
    }
}

function renderSeasonRow(series, season) {
    const div = document.createElement('div');
    div.className = 'cat-row cat-row-season';
    div.dataset.seriesId = series.id;
    div.dataset.seasonNumber = season.seasonNumber;

    const title = document.createElement('span');
    title.className = 'cat-row-title';
    title.textContent = 'Season ' + season.seasonNumber;

    const meta = document.createElement('span');
    meta.className = 'cat-row-meta';
    if (season.statistics) {
        meta.textContent = season.statistics.episodeFileCount + '/' + season.statistics.totalEpisodeCount + ' ep';
    }

    const expandBtn = makeExpandBtn();
    expandBtn.addEventListener('click', (e) => { e.stopPropagation(); toggleSeason(div, series, season.seasonNumber); });

    const ctxItem = { ...series, season: season.seasonNumber };
    div.addEventListener('click', (e) => {
        if (!e.target.closest('.cat-ctx-btn') && !e.target.closest('.cat-ctx-menu')) toggleSeason(div, series, season.seasonNumber);
    });

    div.appendChild(title);
    div.appendChild(meta);
    div.appendChild(makeActions(expandBtn, makeCtxBtn(ctxItem, 'season')));
    return div;
}

async function toggleSeason(rowEl, series, seasonNumber) {
    if (rowEl.classList.contains('expanded')) {
        collapseSiblings(rowEl, ['cat-row-episode']);
        rowEl.classList.remove('expanded');
        return;
    }
    rowEl.classList.add('expanded');
    let eps = [];
    try {
        const res = await catFetch('/api/pelicula/catalog/series/' + series.id + '/season/' + seasonNumber);
        if (res.ok) eps = await res.json();
    } catch (e) { /* non-critical */ }
    let insertAfter = rowEl;
    for (const ep of eps) {
        const el = renderEpisodeRow(series, ep);
        insertAfter.after(el);
        insertAfter = el;
    }
}

function renderEpisodeRow(series, ep) {
    const div = document.createElement('div');
    div.className = 'cat-row cat-row-episode';

    const epNum = 'S' + String(ep.seasonNumber).padStart(2, '0') + 'E' + String(ep.episodeNumber).padStart(2, '0');
    const filePath = ep.file ? ep.file.path : '';
    const hasFile = !!(ep.hasFile || filePath);

    const title = document.createElement('span');
    title.className = 'cat-row-title';
    title.textContent = epNum + (ep.title ? ' \u2013 ' + ep.title : '');
    title.title = ep.title || '';

    const meta = document.createElement('span');
    meta.className = 'cat-row-meta';
    const parts = [];
    if (!hasFile) {
        parts.push('(no file)');
    } else {
        if (ep.file && ep.file.quality && ep.file.quality.quality) parts.push(ep.file.quality.quality.name);
        if (ep.file && ep.file.size) parts.push(fmtSize(ep.file.size));
    }
    meta.textContent = parts.join(' \u00b7 ');

    div.appendChild(title);
    div.appendChild(meta);

    if (hasFile) {
        const epItem = { id: series.id, title: series.title + ' ' + epNum, episodeId: ep.id, path: filePath, arrType: 'sonarr' };
        div.appendChild(makeActions(makeCtxBtn(epItem, 'episode')));
    }
    div.addEventListener('contextmenu', (e) => {
        if (!hasFile) return;
        e.preventDefault();
        openSubRequest({
            label: series.title + ' ' + epNum,
            arrType: 'sonarr',
            arrID: series.id,
            episodeID: ep.id,
        });
    });
    return div;
}

function collapseSiblings(rowEl, classes) {
    const toRemove = [];
    let next = rowEl.nextElementSibling;
    while (next && classes.some(c => next.classList.contains(c))) {
        toRemove.push(next);
        next = next.nextElementSibling;
    }
    toRemove.forEach(el => el.remove());
}

// ── Context menu ─────────────────────────────────────────────────────────────
let _openMenu = null;
document.addEventListener('click', () => {
    if (_openMenu) { _openMenu.remove(); _openMenu = null; }
});

async function openContextMenu(event, item, level) {
    if (_openMenu) { _openMenu.remove(); _openMenu = null; }

    const menu = document.createElement('div');
    menu.className = 'cat-ctx-menu';
    menu.addEventListener('click', (e) => e.stopPropagation());
    _openMenu = menu;

    const registry = await loadActionRegistry();

    if (level === 'series' || level === 'season') {
        // Fan-out: apply episode-level actions to all episodes
        const fanoutDefs = registry.filter(d => d.applies_to && d.applies_to.includes('episode'));
        for (const def of fanoutDefs) {
            const btn = document.createElement('button');
            btn.className = 'cat-ctx-item';
            btn.textContent = def.label + ' (all episodes)';
            btn.addEventListener('click', () => { closeMenu(); runFanout(item, level, def); });
            menu.appendChild(btn);
        }
    } else {
        // Per-item actions from registry
        const applicable = registry.filter(d => d.applies_to && d.applies_to.includes(level));
        for (const def of applicable) {
            const btn = document.createElement('button');
            btn.className = 'cat-ctx-item';
            btn.textContent = def.label;
            btn.addEventListener('click', () => { closeMenu(); runAction(def, item, level); });
            menu.appendChild(btn);
        }
    }

    if (menu.childElementCount === 0) {
        const empty = document.createElement('div');
        empty.className = 'cat-ctx-item';
        empty.style.color = 'var(--muted)';
        empty.textContent = 'No actions available';
        menu.appendChild(empty);
    }

    document.body.appendChild(menu);
    positionMenu(menu, event);

    function closeMenu() { if (_openMenu === menu) { menu.remove(); _openMenu = null; } }
}

function positionMenu(menu, event) {
    const trigger = event.currentTarget || event.target;
    const rect = trigger ? trigger.getBoundingClientRect() : { bottom: event.clientY, right: event.clientX };
    menu.style.position = 'fixed';
    menu.style.top = (rect.bottom + 4) + 'px';
    const rightEdge = window.innerWidth - rect.right;
    menu.style.right = Math.max(4, rightEdge) + 'px';
    menu.style.left = 'auto';
}

// ── Action runner ─────────────────────────────────────────────────────────────
async function runAction(def, item, level) {
    const waitSec = def.sync ? 10 : 0;
    const target = buildTarget(item, level);
    const params = buildParams(def, item, level);
    const body = JSON.stringify({ action: def.name, target, params });
    try {
        const url = '/api/pelicula/actions' + (waitSec > 0 ? '?wait=' + waitSec : '');
        const res = await catFetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body });
        const data = await res.json();
        if (!res.ok) {
            catToast((def.label || def.name) + ' failed: ' + (data.error || 'unknown'), true);
            return;
        }
        if (def.sync && data.state === 'completed') {
            const r = data.result || {};
            const passed = r.passed;
            const summary = passed === true ? '\u2713 Passed' : passed === false ? '\u2717 Failed' : 'Done';
            catToast((def.label || def.name) + ': ' + summary, passed === false);
        } else {
            catToast((def.label || def.name) + ' queued', false);
        }
    } catch (e) {
        catToast((def.label || def.name) + ' error: ' + e.message, true);
    }
}

function buildTarget(item, level) {
    if (level === 'movie') {
        return { path: item.movieFile ? item.movieFile.path : '', arr_type: 'radarr', arr_id: item.id };
    }
    if (level === 'episode') {
        return { path: item.path || '', arr_type: 'sonarr', arr_id: item.id, episode_id: item.episodeId || 0 };
    }
    return { arr_type: (item.arrType || 'radarr'), arr_id: item.id };
}

function buildParams(def, item, level) {
    if (def.name === 'validate' || def.name === 'transcode') {
        const path = level === 'movie' ? (item.movieFile ? item.movieFile.path : '') : (item.path || '');
        return { path };
    }
    if (def.name === 'subtitle_refresh') {
        return { arr_type: level === 'movie' ? 'radarr' : 'sonarr', arr_id: item.id, episode_id: item.episodeId || 0 };
    }
    return {};
}

// ── Fan-out ──────────────────────────────────────────────────────────────────
async function runFanout(item, level, def) {
    let episodes = [];
    try {
        if (level === 'series') {
            const res = await catFetch('/api/pelicula/catalog/series/' + item.id);
            if (res.ok) {
                const detail = await res.json();
                for (const s of (detail.seasons || []).filter(s => s.seasonNumber > 0)) {
                    const r2 = await catFetch('/api/pelicula/catalog/series/' + item.id + '/season/' + s.seasonNumber);
                    if (r2.ok) {
                        const eps = await r2.json();
                        episodes.push(...eps.filter(e => e.hasFile || (e.file && e.file.id)));
                    }
                }
            }
        } else if (level === 'season') {
            const r = await catFetch('/api/pelicula/catalog/series/' + item.id + '/season/' + item.season);
            if (r.ok) {
                const eps = await r.json();
                episodes = eps.filter(e => e.hasFile || (e.file && e.file.id));
            }
        }
    } catch (e) { /* non-critical */ }

    if (!episodes.length) { catToast('No episodes with files found', false); return; }

    // Build progress strip using DOM methods (no innerHTML)
    const strip = document.createElement('div');
    strip.className = 'cat-fanout-strip';

    const labelSpan = document.createElement('span');
    labelSpan.className = 'cat-fanout-label';
    labelSpan.textContent = (def.label || def.name) + '\u2026 ';

    const countEl = document.createElement('span');
    countEl.className = 'cat-fanout-count';
    countEl.textContent = '0/' + episodes.length;
    labelSpan.appendChild(countEl);

    const progressEl = document.createElement('div');
    progressEl.className = 'cat-fanout-progress';
    progressEl.style.setProperty('--pct', '0%');

    const stopBtn = document.createElement('button');
    stopBtn.className = 'cat-fanout-stop';
    stopBtn.textContent = 'Stop';

    strip.appendChild(labelSpan);
    strip.appendChild(progressEl);
    strip.appendChild(stopBtn);

    let stopped = false;
    stopBtn.addEventListener('click', () => { stopped = true; strip.remove(); });

    const list = document.getElementById('cat-list');
    const seriesRow = list && list.querySelector('[data-id="' + item.id + '"]');
    if (seriesRow) seriesRow.after(strip); else if (list) list.prepend(strip);

    for (let i = 0; i < episodes.length; i++) {
        if (stopped) break;
        const ep = episodes[i];
        await runAction(def, {
            id: item.id,
            episodeId: ep.id,
            path: ep.file ? ep.file.path : '',
            arrType: 'sonarr',
        }, 'episode');
        const pct = Math.round(((i + 1) / episodes.length) * 100);
        progressEl.style.setProperty('--pct', pct + '%');
        countEl.textContent = (i + 1) + '/' + episodes.length;
    }
    if (!stopped) setTimeout(() => strip.remove(), 2000);
}

// ── Utility ──────────────────────────────────────────────────────────────────
function fmtSize(bytes) {
    if (bytes >= 1_073_741_824) return (bytes / 1_073_741_824).toFixed(1) + ' GB';
    if (bytes >= 1_048_576) return Math.round(bytes / 1_048_576) + ' MB';
    return Math.round(bytes / 1024) + ' KB';
}

// ── Search / filter ───────────────────────────────────────────────────────────
window.catSearch = function (value) {
    catState.query = (value || '').trim();
    catState.loaded = false;
    loadCatalog();
};

window.catSetType = function (btn, type) {
    catState.type = type;
    catState.loaded = false;
    document.querySelectorAll('.cat-chip').forEach(b => b.classList.toggle('cat-chip-active', b.dataset.type === type));
    loadCatalog();
};

// ── Init ──────────────────────────────────────────────────────────────────────
function initCatalog() {
    if (catState.loaded || catState.loading) return;
    loadCatalog();
    loadFlags();
    loadActionRegistry(); // warm cache early
}

document.addEventListener('pelicula:tab-changed', function (e) {
    if (e.detail && e.detail.tab === 'catalog') initCatalog();
});

if (document.body && document.body.dataset.tab === 'catalog') initCatalog();

})();

// ── Subtitle request dialog ──────────────────────────────────────────────────
const _subReqState = { target: null, selected: new Set() };
const SUB_REQ_DEFAULT_LANGS = ['en', 'es', 'fr', 'de', 'pt', 'it', 'ja', 'zh'];

function openSubRequest(target) {
    _subReqState.target = target;
    _subReqState.selected = new Set();

    // Pre-select from PELICULA_SUB_LANGS via the /settings endpoint if available.
    (async () => {
        try {
            const res = await catFetch('/api/pelicula/settings');
            if (res.ok) {
                const s = await res.json();
                const configured = (s.sub_langs || '').split(',').map(x => x.trim().toLowerCase()).filter(Boolean);
                for (const c of configured) _subReqState.selected.add(c);
            }
        } catch (e) { /* non-critical */ }
        renderSubReqLangs();
    })();

    document.getElementById('sub-req-sub').textContent = target.label;
    document.getElementById('sub-req-hi').checked = false;
    document.getElementById('sub-req-forced').checked = false;
    document.getElementById('sub-req-status').textContent = '';
    renderSubReqLangs();

    document.getElementById('sub-req-backdrop').classList.remove('hidden');
    document.getElementById('sub-req-dialog').classList.remove('hidden');
}

function renderSubReqLangs() {
    const wrap = document.getElementById('sub-req-langs');
    if (!wrap) return;
    const merged = new Set([...SUB_REQ_DEFAULT_LANGS, ..._subReqState.selected]);
    const frag = document.createDocumentFragment();
    for (const code of merged) {
        const el = document.createElement('span');
        el.className = 'sub-req-lang' + (_subReqState.selected.has(code) ? ' active' : '');
        el.textContent = code;
        el.addEventListener('click', () => {
            if (_subReqState.selected.has(code)) _subReqState.selected.delete(code);
            else _subReqState.selected.add(code);
            renderSubReqLangs();
        });
        frag.appendChild(el);
    }
    wrap.replaceChildren(frag);
}

window.subReqClose = function () {
    document.getElementById('sub-req-backdrop').classList.add('hidden');
    document.getElementById('sub-req-dialog').classList.add('hidden');
};

window.subReqSubmit = async function () {
    const t = _subReqState.target;
    if (!t) return;
    const langs = Array.from(_subReqState.selected);
    if (!langs.length) {
        document.getElementById('sub-req-status').textContent = 'Select at least one language.';
        return;
    }
    const body = JSON.stringify({
        action: 'subtitle_request',
        target: { arr_type: t.arrType, arr_id: t.arrID, episode_id: t.episodeID || 0 },
        params: {
            languages: langs,
            hi: document.getElementById('sub-req-hi').checked,
            forced: document.getElementById('sub-req-forced').checked,
            arr_type: t.arrType,
            arr_id: t.arrID,
            episode_id: t.episodeID || 0,
        },
    });
    document.getElementById('sub-req-status').textContent = 'Queuing\u2026';
    try {
        const res = await catFetch('/api/pelicula/actions?wait=10', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body,
        });
        const data = await res.json();
        if (!res.ok) {
            document.getElementById('sub-req-status').textContent = 'Failed: ' + (data.error || res.status);
            return;
        }
        if (data.state === 'completed') {
            document.getElementById('sub-req-status').textContent = 'Queued for ' + langs.join(', ');
            setTimeout(subReqClose, 1200);
        } else {
            document.getElementById('sub-req-status').textContent = 'State: ' + data.state;
        }
    } catch (e) {
        document.getElementById('sub-req-status').textContent = 'Error: ' + e.message;
    }
};
