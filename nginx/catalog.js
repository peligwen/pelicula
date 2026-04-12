// catalog.js — Catalog tab: library browser with per-item action menu
'use strict';

(function () {
const { component, html, raw, esc, toast } = PeliculaFW;

const REGISTRY_TTL = 60_000;
const SUB_REQ_DEFAULT_LANGS = ['en', 'es', 'fr', 'de', 'pt', 'it', 'ja', 'zh'];

// ── Helpers ──────────────────────────────────────────────────────────────────
function catFetch(url, opts) {
    return fetch(url, Object.assign({ credentials: 'same-origin' }, opts || {}));
}

function fmtSize(bytes) {
    if (bytes >= 1_073_741_824) return (bytes / 1_073_741_824).toFixed(1) + ' GB';
    if (bytes >= 1_048_576) return Math.round(bytes / 1_048_576) + ' MB';
    return Math.round(bytes / 1024) + ' KB';
}

// setHTML: safely set element content from a framework html`` result (pre-escaped).
// All interpolations in html`` are auto-escaped by the framework's _escapeHtml().
function setHTML(el, htmlResult) {
    const tpl = document.createElement('template');
    tpl.innerHTML = typeof htmlResult === 'string' ? htmlResult : htmlResult.str;
    el.replaceChildren(tpl.content.cloneNode(true));
}

// ── Context menu (module-level singleton) ─────────────────────────────────────
let _openMenu = null;
document.addEventListener('click', () => { if (_openMenu) { _openMenu.remove(); _openMenu = null; } });

// ── Component ─────────────────────────────────────────────────────────────────
component('catalog', function (el, store, _props) {

    // ── Store initialisation ──────────────────────────────────────────────────
    store.set('catalog.items', []);
    store.set('catalog.query', '');
    store.set('catalog.type', '');
    store.set('catalog.loading', false);
    store.set('catalog.loaded', false);
    store.set('catalog.registry', null);
    store.set('catalog.registryExpires', 0);
    store.set('catalog.flagsByPath', {});
    store.set('catalog.flaggedRows', []);
    store.set('catalog.subReq.target', null);
    store.set('catalog.subReq.selected', new Set());

    // ── Action registry ───────────────────────────────────────────────────────
    async function loadActionRegistry() {
        const now = Date.now();
        const reg = store.get('catalog.registry');
        if (reg && now < store.get('catalog.registryExpires')) return reg;
        try {
            const res = await catFetch('/api/pelicula/actions/registry');
            if (!res.ok) return reg || [];
            const data = await res.json();
            const registry = Array.isArray(data) ? data : [];
            store.set('catalog.registry', registry);
            store.set('catalog.registryExpires', now + REGISTRY_TTL);
            return registry;
        } catch (e) {
            return reg || [];
        }
    }

    // ── Catalog load ──────────────────────────────────────────────────────────
    async function loadCatalog() {
        if (store.get('catalog.loading')) return;
        store.set('catalog.loading', true);
        const list = document.getElementById('cat-list');
        if (list && !store.get('catalog.loaded')) {
            setHTML(list, html`<div class="no-items">Loading\u2026</div>`);
        }
        try {
            const params = new URLSearchParams();
            const q = store.get('catalog.query');
            const t = store.get('catalog.type');
            if (q) params.set('q', q);
            if (t) params.set('type', t);
            const res = await catFetch('/api/pelicula/catalog?' + params);
            if (!res.ok) throw new Error('HTTP ' + res.status);
            const data = await res.json();
            store.set('catalog.items', [...(data.movies || []), ...(data.series || [])]);
            store.set('catalog.loaded', true);
        } catch (e) {
            if (list) setHTML(list, html`<div class="no-items">Failed to load catalog. Is the stack running?</div>`);
        } finally {
            store.set('catalog.loading', false);
        }
    }

    // ── Flags ─────────────────────────────────────────────────────────────────
    async function loadFlags() {
        try {
            const res = await catFetch('/api/pelicula/catalog/flags');
            if (!res.ok) return;
            const data = await res.json();
            const rows = Array.isArray(data.rows) ? data.rows : [];
            const byPath = {};
            for (const r of rows) byPath[r.path] = r;
            store.set('catalog.flagsByPath', byPath);
            store.set('catalog.flaggedRows', rows);
        } catch (e) {
            console.warn('[catalog] flag fetch failed', e);
        }
    }

    function renderAttention() {
        const wrap = document.getElementById('cat-attention');
        const list = document.getElementById('cat-attention-list');
        const count = document.getElementById('cat-attention-count');
        if (!wrap || !list) return;
        const rows = store.get('catalog.flaggedRows').filter(r => r.severity === 'error');
        if (!rows.length) { wrap.style.display = 'none'; return; }
        wrap.style.display = '';
        if (count) count.textContent = String(rows.length);
        const frag = document.createDocumentFragment();
        for (const row of rows) {
            const div = document.createElement('div');
            div.className = 'cat-attention-row';
            div.addEventListener('click', () => openDetail(row.path));
            const title = document.createElement('span');
            title.className = 'cat-row-title';
            title.textContent = row.path.split('/').slice(-1)[0] || row.path;
            title.title = row.path;
            const pills = document.createElement('span');
            setHTML(pills, html`${(row.flags || []).map(f =>
                html`<span class="cat-pill cat-pill-${f.severity || 'info'}" title="${f.detail || ''}">${f.code}</span>`
            )}`);
            div.appendChild(title);
            div.appendChild(pills);
            frag.appendChild(div);
        }
        list.replaceChildren(frag);
    }

    // ── Render list ───────────────────────────────────────────────────────────
    function renderCatalog() {
        const list = document.getElementById('cat-list');
        if (!list) return;
        const items = store.get('catalog.items');
        if (!items.length) {
            setHTML(list, html`<div class="no-items">No items found.</div>`);
            return;
        }
        const frag = document.createDocumentFragment();
        for (const item of items) {
            frag.appendChild(Array.isArray(item.seasons) ? buildSeriesRow(item) : buildMovieRow(item));
        }
        list.replaceChildren(frag);
    }

    function buildMovieRow(item) {
        const metaParts = [];
        if (item.year) metaParts.push(item.year);
        if (item.sizeOnDisk) metaParts.push(fmtSize(item.sizeOnDisk));
        const div = document.createElement('div');
        div.className = 'cat-row cat-row-movie';
        div.dataset.id = item.id;
        setHTML(div, html`
            <span class="cat-row-title" title="${item.title || ''}">${item.title || '(untitled)'}</span>
            <span class="cat-row-meta">${metaParts.join(' \u00b7 ')}</span>
            <div class="cat-row-actions"><button class="cat-ctx-btn" title="Actions">\u22ef</button></div>`);
        div.addEventListener('click', (e) => {
            if (e.target.closest('.cat-ctx-btn')) return;
            const path = item.movieFile ? item.movieFile.path : '';
            if (path) openDetail(path);
        });
        div.addEventListener('contextmenu', (e) => {
            e.preventDefault();
            openSubRequest({ label: item.title || 'Movie', arrType: 'radarr', arrID: item.id, episodeID: 0 });
        });
        div.querySelector('.cat-ctx-btn').addEventListener('click', (e) => { e.stopPropagation(); openContextMenu(e, item, 'movie'); });
        return div;
    }

    function buildSeriesRow(item) {
        const metaParts = [];
        if (item.year) metaParts.push(item.year);
        if (item.statistics && item.statistics.sizeOnDisk) metaParts.push(fmtSize(item.statistics.sizeOnDisk));
        const div = document.createElement('div');
        div.className = 'cat-row cat-row-series';
        div.dataset.id = item.id;
        setHTML(div, html`
            <span class="cat-row-title" title="${item.title || ''}">${item.title || '(untitled)'}</span>
            <span class="cat-row-meta">${metaParts.join(' \u00b7 ')}</span>
            <div class="cat-row-actions">
                <button class="cat-expand-btn" title="Expand">\u25b6</button>
                <button class="cat-ctx-btn" title="Actions">\u22ef</button>
            </div>`);
        div.querySelector('.cat-expand-btn').addEventListener('click', (e) => { e.stopPropagation(); toggleSeries(div, item); });
        div.querySelector('.cat-ctx-btn').addEventListener('click', (e) => { e.stopPropagation(); openContextMenu(e, item, 'series'); });
        div.addEventListener('click', (e) => {
            if (!e.target.closest('.cat-ctx-btn') && !e.target.closest('.cat-ctx-menu')) toggleSeries(div, item);
        });
        return div;
    }

    // ── Series / season expansion ─────────────────────────────────────────────
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
            const el = buildSeasonRow(series, season);
            insertAfter.after(el);
            insertAfter = el;
        }
    }

    function buildSeasonRow(series, season) {
        const epStats = season.statistics
            ? season.statistics.episodeFileCount + '/' + season.statistics.totalEpisodeCount + ' ep'
            : '';
        const div = document.createElement('div');
        div.className = 'cat-row cat-row-season';
        div.dataset.seriesId = series.id;
        div.dataset.seasonNumber = season.seasonNumber;
        setHTML(div, html`
            <span class="cat-row-title">Season ${season.seasonNumber}</span>
            <span class="cat-row-meta">${epStats}</span>
            <div class="cat-row-actions">
                <button class="cat-expand-btn" title="Expand">\u25b6</button>
                <button class="cat-ctx-btn" title="Actions">\u22ef</button>
            </div>`);
        const ctxItem = { ...series, season: season.seasonNumber };
        div.querySelector('.cat-expand-btn').addEventListener('click', (e) => { e.stopPropagation(); toggleSeason(div, series, season.seasonNumber); });
        div.querySelector('.cat-ctx-btn').addEventListener('click', (e) => { e.stopPropagation(); openContextMenu(e, ctxItem, 'season'); });
        div.addEventListener('click', (e) => {
            if (!e.target.closest('.cat-ctx-btn') && !e.target.closest('.cat-ctx-menu')) toggleSeason(div, series, season.seasonNumber);
        });
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
            const el = buildEpisodeRow(series, ep);
            insertAfter.after(el);
            insertAfter = el;
        }
    }

    function buildEpisodeRow(series, ep) {
        const epNum = 'S' + String(ep.seasonNumber).padStart(2, '0') + 'E' + String(ep.episodeNumber).padStart(2, '0');
        const filePath = ep.file ? ep.file.path : '';
        const hasFile = !!(ep.hasFile || filePath);
        const metaParts = [];
        if (!hasFile) {
            metaParts.push('(no file)');
        } else {
            if (ep.file && ep.file.quality && ep.file.quality.quality) metaParts.push(ep.file.quality.quality.name);
            if (ep.file && ep.file.size) metaParts.push(fmtSize(ep.file.size));
        }
        const div = document.createElement('div');
        div.className = 'cat-row cat-row-episode';
        const titleText = epNum + (ep.title ? ' \u2013 ' + ep.title : '');
        setHTML(div, html`
            <span class="cat-row-title" title="${ep.title || ''}">${titleText}</span>
            <span class="cat-row-meta">${metaParts.join(' \u00b7 ')}</span>
            ${hasFile ? html`<div class="cat-row-actions"><button class="cat-ctx-btn" title="Actions">\u22ef</button></div>` : raw('')}`);
        if (hasFile) {
            const epItem = { id: series.id, title: series.title + ' ' + epNum, episodeId: ep.id, path: filePath, arrType: 'sonarr' };
            div.querySelector('.cat-ctx-btn').addEventListener('click', (e) => { e.stopPropagation(); openContextMenu(e, epItem, 'episode'); });
        }
        div.addEventListener('contextmenu', (e) => {
            if (!hasFile) return;
            e.preventDefault();
            openSubRequest({ label: series.title + ' ' + epNum, arrType: 'sonarr', arrID: series.id, episodeID: ep.id });
        });
        return div;
    }

    function collapseSiblings(rowEl, classes) {
        const toRemove = [];
        let next = rowEl.nextElementSibling;
        while (next && classes.some(c => next.classList.contains(c))) { toRemove.push(next); next = next.nextElementSibling; }
        toRemove.forEach(el => el.remove());
    }

    // ── Context menu ──────────────────────────────────────────────────────────
    async function openContextMenu(event, item, level) {
        if (_openMenu) { _openMenu.remove(); _openMenu = null; }
        const menu = document.createElement('div');
        menu.className = 'cat-ctx-menu';
        menu.addEventListener('click', (e) => e.stopPropagation());
        _openMenu = menu;

        const registry = await loadActionRegistry();
        const isFanout = level === 'series' || level === 'season';
        const defs = isFanout
            ? registry.filter(d => d.applies_to && d.applies_to.includes('episode'))
            : registry.filter(d => d.applies_to && d.applies_to.includes(level));

        if (defs.length) {
            for (const def of defs) {
                const btn = document.createElement('button');
                btn.className = 'cat-ctx-item';
                btn.textContent = def.label + (isFanout ? ' (all episodes)' : '');
                btn.addEventListener('click', () => { closeMenu(); isFanout ? runFanout(item, level, def) : runAction(def, item, level); });
                menu.appendChild(btn);
            }
        } else {
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
        menu.style.right = Math.max(4, window.innerWidth - rect.right) + 'px';
        menu.style.left = 'auto';
    }

    // ── Detail drawer ─────────────────────────────────────────────────────────
    async function openDetail(path) {
        if (!path) return;
        const backdrop = document.getElementById('cat-drawer-backdrop');
        const drawer = document.getElementById('cat-drawer');
        const titleEl = document.getElementById('cat-drawer-title');
        const sub = document.getElementById('cat-drawer-sub');
        const body = document.getElementById('cat-drawer-body');
        if (!drawer) return;
        PeliculaFW.openDrawer(drawer, backdrop);
        titleEl.textContent = path.split('/').slice(-1)[0] || 'Details';
        sub.textContent = path;
        setHTML(body, html`<div style="color:var(--muted);padding:1rem 0">Loading\u2026</div>`);
        try {
            const res = await catFetch('/api/pelicula/catalog/detail?path=' + encodeURIComponent(path));
            if (!res.ok) throw new Error('HTTP ' + res.status);
            const data = await res.json();
            setHTML(body, renderDetailHtml(data));
        } catch (e) {
            setHTML(body, html`<div style="color:var(--danger);padding:1rem 0">Failed to load details: ${e.message}</div>`);
        }
    }

    function renderDetailHtml(data) {
        const job = data.job || {};
        const val = job.validation || null;
        const codecs = val && val.checks && val.checks.codecs;
        const parts = [];

        if (Array.isArray(data.flags) && data.flags.length) {
            parts.push(html`<div class="drawer-section-title">Flags</div>`);
            parts.push(html`<div>${data.flags.map(f =>
                html`<span class="cat-pill cat-pill-${f.severity || 'info'}" title="${f.detail || ''}">${f.code}</span>`)}</div>`);
        }

        parts.push(html`<div class="drawer-section-title">Encoding</div>`);
        if (codecs) {
            parts.push(html`<div>
                <span class="cat-pill cat-pill-encoding">video: ${codecs.video || '?'}</span>
                <span class="cat-pill cat-pill-encoding">audio: ${codecs.audio || '?'}</span>
                ${codecs.width && codecs.height ? html`<span class="cat-pill cat-pill-encoding">${codecs.width}x${codecs.height}</span>` : raw('')}
            </div>`);
        } else {
            parts.push(html`<div style="color:var(--muted);padding:1rem 0">No codec info yet.</div>`);
        }

        const embedded = codecs && Array.isArray(codecs.subtitles) ? codecs.subtitles : [];
        const missing = Array.isArray(job.missing_subs) ? job.missing_subs : [];
        parts.push(html`<div class="drawer-section-title">Subtitles</div>`);
        parts.push(html`<div>
            ${embedded.length
                ? embedded.map(lang => html`<span class="cat-pill cat-pill-subs">${lang}</span>`)
                : html`<div style="color:var(--muted);padding:1rem 0">No embedded subtitle tracks.</div>`}
            ${missing.length ? html`<div style="margin-top:0.5rem;color:var(--muted)">Missing:</div>` : raw('')}
            ${missing.map(lang => html`<span class="cat-pill cat-pill-warn">${lang}</span>`)}
        </div>`);

        parts.push(html`<div class="drawer-section-title">Status</div>`);
        const statusParts = [];
        if (val && val.checks) {
            for (const k of ['integrity', 'duration', 'sample']) {
                const v = val.checks[k] || 'skip';
                const cls = v === 'pass' ? 'cat-pill-status-pass' : v === 'fail' ? 'cat-pill-status-fail' : 'cat-pill';
                statusParts.push(html`<span class="cat-pill ${cls}">${k}: ${v}</span>`);
            }
        }
        if (job.transcode_decision) statusParts.push(html`<span class="cat-pill">transcode: ${job.transcode_decision}</span>`);
        if (job.catalog && job.catalog.jellyfin_synced) statusParts.push(html`<span class="cat-pill cat-pill-status-pass">jellyfin synced</span>`);
        if (job.error) statusParts.push(html`<div class="drawer-error">${job.error}</div>`);
        parts.push(html`<div>${statusParts}</div>`);

        return html`<div>${parts}</div>`;
    }

    // ── Action runner ─────────────────────────────────────────────────────────
    async function runAction(def, item, level) {
        const waitSec = def.sync ? 10 : 0;
        const target = buildTarget(item, level);
        const params = buildParams(def, item, level);
        const body = JSON.stringify({ action: def.name, target, params });
        try {
            const url = '/api/pelicula/actions' + (waitSec > 0 ? '?wait=' + waitSec : '');
            const res = await catFetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body });
            const data = await res.json();
            if (!res.ok) { toast((def.label || def.name) + ' failed: ' + (data.error || 'unknown'), { error: true }); return; }
            if (def.sync && data.state === 'completed') {
                const passed = (data.result || {}).passed;
                const summary = passed === true ? '\u2713 Passed' : passed === false ? '\u2717 Failed' : 'Done';
                toast((def.label || def.name) + ': ' + summary, passed === false ? { error: true } : undefined);
            } else {
                toast((def.label || def.name) + ' queued');
            }
        } catch (e) {
            toast((def.label || def.name) + ' error: ' + e.message, { error: true });
        }
    }

    function buildTarget(item, level) {
        if (level === 'movie') return { path: item.movieFile ? item.movieFile.path : '', arr_type: 'radarr', arr_id: item.id };
        if (level === 'episode') return { path: item.path || '', arr_type: 'sonarr', arr_id: item.id, episode_id: item.episodeId || 0 };
        return { arr_type: (item.arrType || 'radarr'), arr_id: item.id };
    }

    function buildParams(def, item, level) {
        if (def.name === 'validate' || def.name === 'transcode') {
            return { path: level === 'movie' ? (item.movieFile ? item.movieFile.path : '') : (item.path || '') };
        }
        if (def.name === 'subtitle_refresh') {
            return { arr_type: level === 'movie' ? 'radarr' : 'sonarr', arr_id: item.id, episode_id: item.episodeId || 0 };
        }
        return {};
    }

    // ── Fan-out ───────────────────────────────────────────────────────────────
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
                if (r.ok) episodes = (await r.json()).filter(e => e.hasFile || (e.file && e.file.id));
            }
        } catch (e) { /* non-critical */ }

        if (!episodes.length) { toast('No episodes with files found'); return; }

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
            await runAction(def, { id: item.id, episodeId: ep.id, path: ep.file ? ep.file.path : '', arrType: 'sonarr' }, 'episode');
            const pct = Math.round(((i + 1) / episodes.length) * 100);
            progressEl.style.setProperty('--pct', pct + '%');
            countEl.textContent = (i + 1) + '/' + episodes.length;
        }
        if (!stopped) setTimeout(() => strip.remove(), 2000);
    }

    // ── Subtitle request dialog ───────────────────────────────────────────────
    function openSubRequest(target) {
        store.set('catalog.subReq.target', target);
        store.set('catalog.subReq.selected', new Set());

        (async () => {
            try {
                const res = await catFetch('/api/pelicula/settings');
                if (res.ok) {
                    const s = await res.json();
                    const configured = (s.sub_langs || '').split(',').map(x => x.trim().toLowerCase()).filter(Boolean);
                    store.set('catalog.subReq.selected', new Set(configured));
                }
            } catch (e) { /* non-critical */ }
            renderSubReqLangs();
        })();

        document.getElementById('sub-req-sub').textContent = target.label;
        document.getElementById('sub-req-hi').checked = false;
        document.getElementById('sub-req-forced').checked = false;
        document.getElementById('sub-req-status').textContent = '';
        renderSubReqLangs();
        PeliculaFW.openDrawer(
            document.getElementById('sub-req-dialog'),
            document.getElementById('sub-req-backdrop')
        );
    }

    function renderSubReqLangs() {
        const wrap = document.getElementById('sub-req-langs');
        if (!wrap) return;
        const selected = store.get('catalog.subReq.selected');
        const merged = new Set([...SUB_REQ_DEFAULT_LANGS, ...selected]);
        const frag = document.createDocumentFragment();
        for (const code of merged) {
            const span = document.createElement('span');
            span.className = 'sub-req-lang' + (selected.has(code) ? ' active' : '');
            span.textContent = code;
            span.addEventListener('click', () => {
                const sel = store.get('catalog.subReq.selected');
                if (sel.has(code)) sel.delete(code); else sel.add(code);
                store.set('catalog.subReq.selected', sel);
                renderSubReqLangs();
            });
            frag.appendChild(span);
        }
        wrap.replaceChildren(frag);
    }

    // ── Public API (window.*) ─────────────────────────────────────────────────
    window.catLoad = function () { loadCatalog(); };

    window.catSearch = function (value) {
        store.set('catalog.query', (value || '').trim());
        store.set('catalog.loaded', false);
        loadCatalog();
    };

    window.catSetType = function (btn, type) {
        store.set('catalog.type', type);
        store.set('catalog.loaded', false);
        document.querySelectorAll('.cat-chip').forEach(b => b.classList.toggle('cat-chip-active', b.dataset.type === type));
        loadCatalog();
    };

    window.catOpenDetail = function (path) { openDetail(path); };

    window.catCloseDetail = function () {
        PeliculaFW.closeDrawer(
            document.getElementById('cat-drawer'),
            document.getElementById('cat-drawer-backdrop')
        );
    };

    window.catAction = function (action, itemJson) {
        const item = typeof itemJson === 'string' ? JSON.parse(itemJson) : itemJson;
        runAction(action, item, item.level || 'movie');
    };

    window.catRefreshFlags = function () { loadFlags(); };

    window.subReqOpen = function (target) { openSubRequest(target); };

    window.subReqClose = function () {
        PeliculaFW.closeDrawer(
            document.getElementById('sub-req-dialog'),
            document.getElementById('sub-req-backdrop')
        );
    };

    window.subReqSubmit = async function () {
        const t = store.get('catalog.subReq.target');
        if (!t) return;
        const langs = Array.from(store.get('catalog.subReq.selected'));
        const statusEl = document.getElementById('sub-req-status');
        if (!langs.length) { statusEl.textContent = 'Select at least one language.'; return; }
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
        statusEl.textContent = 'Queuing\u2026';
        try {
            const res = await catFetch('/api/pelicula/actions?wait=10', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body });
            const data = await res.json();
            if (!res.ok) { statusEl.textContent = 'Failed: ' + (data.error || res.status); return; }
            if (data.state === 'completed') {
                statusEl.textContent = 'Queued for ' + langs.join(', ');
                setTimeout(window.subReqClose, 1200);
            } else {
                statusEl.textContent = 'State: ' + data.state;
            }
        } catch (e) {
            statusEl.textContent = 'Error: ' + e.message;
        }
    };

    // ── Subscribe to store changes ────────────────────────────────────────────
    store.subscribe('catalog.items', renderCatalog);
    store.subscribe('catalog.flaggedRows', renderAttention);

    // ── Component lifecycle ───────────────────────────────────────────────────
    return {
        render() { /* initial render handled via store subscriptions + loadOnce */ },
        loadOnce() {
            loadCatalog();
            loadFlags();
            loadActionRegistry(); // warm cache early
        },
    };
});

// ── Tab activation ────────────────────────────────────────────────────────────
// dashboard.js dispatches pelicula:tab-changed; we mount on first activation.
PeliculaFW.onTab('catalog', function () {
    const el = document.getElementById('catalog-section');
    if (el && !el.dataset.mounted) {
        el.dataset.mounted = '1';
        PeliculaFW.mount('catalog', el);
    }
});

if (document.body && document.body.dataset.tab === 'catalog') {
    const el = document.getElementById('catalog-section');
    if (el && !el.dataset.mounted) {
        el.dataset.mounted = '1';
        PeliculaFW.mount('catalog', el);
    }
}
})();
