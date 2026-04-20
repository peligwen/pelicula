// catalog.js — Catalog tab: library browser with per-item action menu
'use strict';

import { component, html, raw, toast, onTab, mount, openDrawer, closeDrawer } from '/framework.js';
import { get, post, put, del } from '/api.js';

const REGISTRY_TTL = 60_000;
const SUB_REQ_DEFAULT_LANGS = ['en', 'es', 'fr', 'de', 'pt', 'it', 'ja', 'zh'];

// ── Helpers ──────────────────────────────────────────────────────────────────
function fmtSize(bytes) {
    if (bytes >= 1_073_741_824) return (bytes / 1_073_741_824).toFixed(1) + ' GB';
    if (bytes >= 1_048_576) return Math.round(bytes / 1_048_576) + ' MB';
    return Math.round(bytes / 1024) + ' KB';
}

// isMissing returns true for items with no downloaded content.
// Movies: hasFile === false. Series: episodeFileCount === 0.
// Partially-downloaded series (episodeFileCount > 0) stay in the main list.
function isMissing(item) {
    if (Array.isArray(item.seasons)) {
        return !(item.statistics && item.statistics.episodeFileCount > 0);
    }
    return !item.hasFile;
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
    store.set('catalog.qualityProfiles', null);

    // ── Action registry ───────────────────────────────────────────────────────
    async function loadActionRegistry() {
        const now = Date.now();
        const reg = store.get('catalog.registry');
        if (reg && now < store.get('catalog.registryExpires')) return reg;
        try {
            const data = await get('/api/pelicula/actions/registry');
            if (!data) return reg || [];
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
            const data = await get('/api/pelicula/catalog?' + params);
            if (!data) throw new Error('catalog unavailable');
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
            const data = await get('/api/pelicula/catalog/flags');
            if (!data) return;
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
        const missingItems = items.filter(isMissing);
        const presentItems = items.filter(item => !isMissing(item));
        const frag = document.createDocumentFragment();
        const missingSection = buildMissingSection(missingItems);
        if (missingSection) frag.appendChild(missingSection);
        for (const item of presentItems) {
            frag.appendChild(Array.isArray(item.seasons) ? buildSeriesRow(item) : buildMovieRow(item));
        }
        list.replaceChildren(frag);
    }

    function buildMissingSection(items) {
        if (!items.length) return null;
        const details = document.createElement('details');
        details.className = 'cat-missing-section';
        // collapsed by default — open attribute intentionally omitted

        const summary = document.createElement('summary');
        summary.className = 'cat-missing-header';
        const titleSpan = document.createElement('span');
        titleSpan.className = 'cat-missing-title';
        titleSpan.textContent = 'Missing / Searching';
        const countSpan = document.createElement('span');
        countSpan.className = 'cat-missing-count';
        countSpan.textContent = String(items.length);
        summary.appendChild(titleSpan);
        summary.appendChild(countSpan);
        details.appendChild(summary);

        const inner = document.createElement('div');
        inner.className = 'cat-missing-list';
        for (const item of items) inner.appendChild(buildMissingRow(item));
        details.appendChild(inner);
        return details;
    }

    function buildMissingRow(item) {
        const isSeries = Array.isArray(item.seasons);
        const metaParts = [];
        if (item.year) metaParts.push(item.year);
        if (isSeries && item.statistics) {
            metaParts.push(item.statistics.episodeFileCount + '/' + item.statistics.totalEpisodeCount + ' ep');
        }
        const div = document.createElement('div');
        div.className = 'cat-row cat-row-missing';
        setHTML(div, html`
            <span class="cat-row-title" title="${item.title || ''}">${item.title || '(untitled)'}</span>
            <span class="cat-row-meta">${metaParts.join(' \u00b7 ')}</span>
            <div class="cat-row-actions"><button class="cat-ctx-btn" title="Actions">\u22ef</button></div>`);
        div.addEventListener('click', (e) => {
            if (e.target.closest('.cat-ctx-btn')) return;
            openMissingDetail(item);
        });
        div.addEventListener('contextmenu', (e) => {
            e.preventDefault();
            openMissingContextMenu(e, item);
        });
        div.querySelector('.cat-ctx-btn').addEventListener('click', (e) => {
            e.stopPropagation();
            openMissingContextMenu(e, item);
        });
        return div;
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
            if (path) openDetail(path, item.movieFile && item.movieFile.mediaInfo);
        });
        div.addEventListener('contextmenu', (e) => {
            e.preventDefault();
            openContextMenu(e, item, 'movie');
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
            const d = await get('/api/pelicula/catalog/series/' + series.id);
            if (d) {
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
            const data = await get('/api/pelicula/catalog/series/' + series.id + '/season/' + seasonNumber);
            if (data) eps = data;
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
            div.addEventListener('click', (e) => {
                if (e.target.closest('.cat-ctx-btn')) return;
                openDetail(filePath, ep.file && ep.file.mediaInfo);
            });
            div.addEventListener('contextmenu', (e) => {
                e.preventDefault();
                openContextMenu(e, epItem, 'episode');
            });
        }
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
                btn.addEventListener('click', () => {
                    closeMenu();
                    if (def.name === 'replace') {
                        openReplaceDrawer(item, level);
                        return;
                    }
                    if (def.name === 'dualsub') {
                        openDualsubDialog(item, level);
                        return;
                    }
                    if (def.name === 'subtitle_search') {
                        openSubSearchDialog(item, level);
                        return;
                    }
                    isFanout ? runFanout(item, level, def) : runAction(def, item, level);
                });
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
    // radarrCodecFallback converts a Radarr/Sonarr mediaInfo object into the
    // same CodecInfo shape that procula emits, for items not yet run through the pipeline.
    function radarrCodecFallback(mi) {
        if (!mi) return null;
        const c = {};
        if (mi.videoCodec) c.video = mi.videoCodec;
        if (mi.audioCodec) c.audio = mi.audioCodec;
        if (mi.resolution) {
            const m = mi.resolution.match(/^(\d+)x(\d+)/i);
            if (m) { c.width = parseInt(m[1], 10); c.height = parseInt(m[2], 10); }
        }
        if (typeof mi.subtitles === 'string' && mi.subtitles) {
            c.subtitles = mi.subtitles.split(/\s*\/\s*/).filter(Boolean);
        } else {
            c.subtitles = [];
        }
        return (c.video || c.audio) ? c : null;
    }

    async function openDetail(path, mediaInfo) {
        if (!path) return;
        const backdrop = document.getElementById('cat-drawer-backdrop');
        const drawer = document.getElementById('cat-drawer');
        const titleEl = document.getElementById('cat-drawer-title');
        const sub = document.getElementById('cat-drawer-sub');
        const body = document.getElementById('cat-drawer-body');
        if (!drawer) return;
        openDrawer(drawer, backdrop);
        const filename = path.split('/').slice(-1)[0] || 'Details';
        // Strip extension for display; keep raw filename in title attr for hover
        titleEl.textContent = filename.replace(/\.[^.]+$/, '');
        titleEl.title = filename;
        sub.textContent = path;
        sub.title = path;
        setHTML(body, html`<div style="color:var(--muted);padding:1rem 0">Loading\u2026</div>`);
        try {
            const [detailResult, tracksResult] = await Promise.allSettled([
                get('/api/pelicula/catalog/detail?path=' + encodeURIComponent(path)),
                get('/api/procula/subtitle-tracks?path=' + encodeURIComponent(path)),
            ]);
            if (detailResult.status === 'rejected') throw detailResult.reason;
            const data = detailResult.value;
            if (!data) throw new Error('catalog unavailable');
            const tracksData = (tracksResult.status === 'fulfilled' && tracksResult.value) ? tracksResult.value : {};
            const drawerDualsubs = tracksData.dualsubs || [];
            if (data.title) {
                titleEl.textContent = data.title;
                titleEl.title = filename;
            }
            setHTML(body, renderDetailHtml(data, mediaInfo, drawerDualsubs));
        } catch (e) {
            setHTML(body, html`<div style="color:var(--danger);padding:1rem 0">Failed to load details: ${e.message}</div>`);
        }
    }

    async function openMissingDetail(item) {
        const isSeries = Array.isArray(item.seasons);
        const arrType = isSeries ? 'sonarr' : 'radarr';
        const backdrop = document.getElementById('cat-drawer-backdrop');
        const drawer = document.getElementById('cat-drawer');
        const titleEl = document.getElementById('cat-drawer-title');
        const sub = document.getElementById('cat-drawer-sub');
        const body = document.getElementById('cat-drawer-body');
        if (!drawer) return;
        openDrawer(drawer, backdrop);
        titleEl.textContent = item.title || '(untitled)';
        titleEl.title = item.title || '';
        sub.textContent = item.monitored ? 'Monitored \u2014 not downloaded' : 'Not monitored';
        sub.title = '';
        setHTML(body, html`<div style="color:var(--muted);padding:1rem 0">Loading\u2026</div>`);

        // Fetch and cache quality profiles
        let profilesData = store.get('catalog.qualityProfiles');
        if (!profilesData) {
            try {
                const data = await get('/api/pelicula/catalog/qualityprofiles');
                if (data) {
                    profilesData = data;
                    store.set('catalog.qualityProfiles', profilesData);
                }
            } catch (e) { /* non-critical — will show profile ID instead of name */ }
        }

        setHTML(body, renderMissingDetailHtml(item, arrType, profilesData));
    }

    function renderMissingDetailHtml(item, arrType, profilesData) {
        const isSeries = Array.isArray(item.seasons);
        const stats = item.statistics || {};
        const profileId = item.qualityProfileId;
        const profileMap = profilesData && profilesData[arrType] ? profilesData[arrType] : {};
        const profileName = profileId
            ? (profileMap[String(profileId)] || 'Profile #' + profileId)
            : '\u2014';

        const parts = [];

        // Poster image (Radarr/Sonarr both return images array with coverType)
        const poster = (item.images || []).find(img => img.coverType === 'poster');
        if (poster) {
            parts.push(html`<div class="cat-drawer-hero">
                <img class="cat-drawer-poster" src="${poster.remoteUrl || poster.url || ''}" alt="" loading="lazy">
            </div>`);
        }

        // Overview / synopsis
        if (item.overview) {
            parts.push(html`<div class="cat-drawer-synopsis">${item.overview}</div>`);
        }

        parts.push(html`<div class="drawer-section-title">Status</div>`);
        parts.push(html`<div>
            <span class="cat-pill">${item.monitored ? 'monitored' : 'unmonitored'}</span>
            <span class="cat-pill">${isSeries ? 'series' : 'movie'}</span>
            ${item.status ? html`<span class="cat-pill">${item.status}</span>` : raw('')}
            ${item.network ? html`<span class="cat-pill">${item.network}</span>` : raw('')}
        </div>`);

        parts.push(html`<div class="drawer-section-title">Quality Profile</div>`);
        parts.push(html`<div><span class="cat-pill cat-pill-encoding">${profileName}</span></div>`);

        if (isSeries) {
            const downloaded = stats.episodeFileCount || 0;
            const total = stats.totalEpisodeCount || 0;
            const monitored = stats.monitoredEpisodeCount || 0;
            parts.push(html`<div class="drawer-section-title">Episodes</div>`);
            parts.push(html`<div>
                <span class="cat-pill">${downloaded}/${total} downloaded</span>
                <span class="cat-pill">${monitored} monitored</span>
            </div>`);
        }

        if (Array.isArray(item.genres) && item.genres.length) {
            parts.push(html`<div class="drawer-section-title">Genres</div>`);
            parts.push(html`<div>${item.genres.map(g => html`<span class="cat-pill">${g}</span>`)}</div>`);
        }

        return html`<div>${parts}</div>`;
    }

    function openMissingContextMenu(event, item) {
        if (_openMenu) { _openMenu.remove(); _openMenu = null; }
        const menu = document.createElement('div');
        menu.className = 'cat-ctx-menu';
        menu.addEventListener('click', (e) => e.stopPropagation());
        _openMenu = menu;

        const isSeries = Array.isArray(item.seasons);
        const arrType = isSeries ? 'sonarr' : 'radarr';

        const actions = [
            {
                label: 'Force Search',
                fn: () => { closeMenu(); runArrCommand('search', arrType, item.id, item.title); },
            },
            {
                label: 'Unmonitor',
                fn: () => { closeMenu(); runArrCommand('unmonitor', arrType, item.id, item.title); },
            },
        ];

        for (const action of actions) {
            const btn = document.createElement('button');
            btn.className = 'cat-ctx-item';
            btn.textContent = action.label;
            btn.addEventListener('click', action.fn);
            menu.appendChild(btn);
        }

        document.body.appendChild(menu);
        positionMenu(menu, event);
        function closeMenu() { if (_openMenu === menu) { menu.remove(); _openMenu = null; } }
    }

    async function runArrCommand(command, arrType, arrId, title) {
        const label = command === 'search' ? 'Force Search' : 'Unmonitor';
        const successMsg = command === 'search'
            ? (title || 'Item') + ': search triggered'
            : (title || 'Item') + ': unmonitored';
        try {
            await post('/api/pelicula/catalog/command', { arr_type: arrType, arr_id: arrId, command });
            toast(successMsg);
        } catch (e) {
            const errMsg = (e.body && e.body.error) || e.message || 'unknown';
            toast(label + ' failed: ' + errMsg, { error: true });
        }
    }

    function renderDetailHtml(data, mediaInfo, dualsubs) {
        dualsubs = dualsubs || [];
        const job = data.job || {};
        const val = job.validation || null;
        const codecs = (val && val.checks && val.checks.codecs) || radarrCodecFallback(mediaInfo);
        const parts = [];

        const hasSynopsis = typeof data.synopsis === 'string' && data.synopsis.trim();
        const hasArtwork = typeof data.artwork_url === 'string' && data.artwork_url.trim();
        if (hasSynopsis || hasArtwork) {
            parts.push(html`<div class="cat-drawer-hero">
                ${hasArtwork ? html`<img class="cat-drawer-poster" src="${data.artwork_url}" alt="" loading="lazy">` : raw('')}
                ${hasSynopsis ? html`<div class="cat-drawer-synopsis">${data.synopsis}</div>` : raw('')}
            </div>`);
        } else {
            // Show a subtle indicator for why artwork/synopsis are absent.
            let jfNote = '';
            if (!data.in_catalog) {
                jfNote = 'Not in catalog — run Resync to index';
            } else if (!data.metadata_synced_at) {
                jfNote = 'Jellyfin sync pending\u2026';
            } else if (!hasArtwork && !hasSynopsis) {
                jfNote = 'Not yet in Jellyfin library';
            }
            if (jfNote) {
                parts.push(html`<div class="cat-meta-note">${jfNote}</div>`);
            }
        }

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

        const rawEmbedded = codecs && Array.isArray(codecs.subtitles) ? codecs.subtitles : [];
        const embedded = [...new Set(rawEmbedded)];
        const missing = Array.isArray(job.missing_subs) ? job.missing_subs : [];
        parts.push(html`<div class="drawer-section-title">Subtitles</div>`);
        parts.push(html`<div>
            ${embedded.length
                ? embedded.map(lang => html`<span class="cat-pill cat-pill-subs">${lang}</span>`)
                : html`<div style="color:var(--muted);padding:1rem 0">No embedded subtitle tracks.</div>`}
            ${dualsubs.map(ds => html`<span class="cat-pill cat-pill-subs" title="${ds.file}">${ds.pair} (dual)</span>`)}
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
        const body = { action: def.name, target, params };
        try {
            const url = '/api/pelicula/actions' + (waitSec > 0 ? '?wait=' + waitSec : '');
            const data = await post(url, body);
            if (def.sync && data && data.state === 'completed') {
                const passed = (data.result || {}).passed;
                const summary = passed === true ? '\u2713 Passed' : passed === false ? '\u2717 Failed' : 'Done';
                toast((def.label || def.name) + ': ' + summary, passed === false ? { error: true } : undefined);
            } else {
                toast((def.label || def.name) + ' queued');
            }
        } catch (e) {
            const errMsg = (e.body && e.body.error) || e.message || 'unknown';
            toast((def.label || def.name) + ' failed: ' + errMsg, { error: true });
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
        return {};
    }

    // ── Fan-out ───────────────────────────────────────────────────────────────
    async function runFanout(item, level, def) {
        let episodes = [];
        try {
            if (level === 'series') {
                const detail = await get('/api/pelicula/catalog/series/' + item.id);
                if (detail) {
                    for (const s of (detail.seasons || []).filter(s => s.seasonNumber > 0)) {
                        try {
                            const eps = await get('/api/pelicula/catalog/series/' + item.id + '/season/' + s.seasonNumber);
                            if (eps) episodes.push(...eps.filter(e => e.hasFile || (e.file && e.file.id)));
                        } catch (e) { /* non-critical */ }
                    }
                }
            } else if (level === 'season') {
                const eps = await get('/api/pelicula/catalog/series/' + item.id + '/season/' + item.season);
                if (eps) episodes = eps.filter(e => e.hasFile || (e.file && e.file.id));
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

    // ── Subtitle search dialog ────────────────────────────────────────────────────

    function openSubSearchDialog(item, level) {
        store.set('catalog.subsearch.item', item);
        store.set('catalog.subsearch.level', level);
        document.getElementById('sub-search-sub').textContent = item.title || item.seriesTitle || '';
        document.getElementById('sub-search-hi').checked = false;
        document.getElementById('sub-search-forced').checked = false;
        document.getElementById('sub-search-status').textContent = '';

        const langs = (item.subLangs || ['en']).slice();
        const allLangs = Array.from(new Set([...langs, 'en', 'es', 'fr', 'de']));
        const container = document.getElementById('sub-search-langs');
        container.replaceChildren();
        allLangs.forEach(lang => {
            const chip = document.createElement('span');
            chip.textContent = lang;
            chip.dataset.lang = lang;
            chip.dataset.active = langs.includes(lang) ? '1' : '0';
            const updateChip = () => {
                chip.className = 'sub-req-lang' + (chip.dataset.active === '1' ? ' active' : '');
            };
            updateChip();
            chip.addEventListener('click', () => {
                chip.dataset.active = chip.dataset.active === '1' ? '0' : '1';
                updateChip();
            });
            container.appendChild(chip);
        });

        openDrawer(document.getElementById('sub-search-dialog'), document.getElementById('sub-search-backdrop'));
    }

    window.subSearchClose = function () {
        closeDrawer(document.getElementById('sub-search-dialog'), document.getElementById('sub-search-backdrop'));
    };

    window.subSearchSubmit = async function () {
        const item = store.get('catalog.subsearch.item');
        const level = store.get('catalog.subsearch.level');
        const statusEl = document.getElementById('sub-search-status');
        const langs = Array.from(document.querySelectorAll('#sub-search-langs [data-active="1"]'))
            .map(c => c.dataset.lang);
        if (!langs.length) { statusEl.textContent = 'Select at least one language.'; return; }
        statusEl.textContent = 'Searching\u2026';
        try {
            await post('/api/pelicula/actions?wait=10', {
                action: 'subtitle_search',
                target: { arr_type: level === 'movie' ? 'radarr' : 'sonarr', arr_id: item.id, episode_id: item.episodeId || 0 },
                params: {
                    languages: langs,
                    hi: document.getElementById('sub-search-hi').checked,
                    forced: document.getElementById('sub-search-forced').checked,
                },
            });
            statusEl.textContent = 'Search triggered.';
            setTimeout(window.subSearchClose, 1500);
        } catch (e) {
            statusEl.textContent = 'Request failed.';
        }
    };

    // ── Dual subtitle dialog ──────────────────────────────────────────────────────

    let _dualsubTracks = [];
    let _dualsubEmbedded = [];
    let _dualsubDualsubs = [];
    let _dualsubProfiles = [];
    let _dualsubManageOpen = false;
    let _dualsubCurrentLayout = 'stacked_bottom';

    async function openDualsubDialog(item, level) {
        store.set('catalog.dualsub.item', item);
        store.set('catalog.dualsub.level', level);
        const path = level === 'movie' ? (item.movieFile ? item.movieFile.path : '') : (item.path || '');
        store.set('catalog.dualsub.path', path);

        document.getElementById('dualsub-sub').textContent = item.title || item.seriesTitle || '';
        document.getElementById('dualsub-status').textContent = '';
        document.getElementById('dualsub-profile-editor').style.display = 'none';
        _dualsubManageOpen = false;
        document.getElementById('dualsub-manage-btn').textContent = 'Manage \u25be';

        const [profResult, trackResult] = await Promise.allSettled([
            get('/api/procula/dualsub-profiles'),
            path ? get('/api/procula/subtitle-tracks?path=' + encodeURIComponent(path)) : Promise.resolve(null),
        ]);
        // Guard: bail if another open superseded this one
        if (store.get('catalog.dualsub.path') !== path) return;
        _dualsubProfiles = (profResult.status === 'fulfilled' && profResult.value) ? profResult.value : [];
        const tracksData = (trackResult.status === 'fulfilled' && trackResult.value) ? trackResult.value : {};
        _dualsubTracks = tracksData.tracks || [];
        _dualsubEmbedded = tracksData.embedded_tracks || [];
        _dualsubDualsubs = tracksData.dualsubs || [];

        dualsubRenderProfiles();
        dualsubRenderOnDisk();
        dualsubRenderPairs();

        openDrawer(document.getElementById('dualsub-dialog'), document.getElementById('dualsub-backdrop'));
    }

    function dualsubRenderProfiles() {
        const sel = document.getElementById('dualsub-profile-select');
        sel.replaceChildren();
        _dualsubProfiles.forEach(p => {
            const opt = document.createElement('option');
            opt.value = p.name;
            opt.textContent = p.name + (p.layout ? ' (' + p.layout.replace('_', ' ') + ', ' + p.font_size + 'pt)' : '');
            sel.appendChild(opt);
        });
    }

    function dualsubRenderOnDisk() {
        const container = document.getElementById('dualsub-ondisk');
        container.replaceChildren();
        if (_dualsubDualsubs.length === 0) {
            const msg = document.createElement('div');
            msg.style.cssText = 'color:var(--muted);font-size:.78rem';
            msg.textContent = '\u2014';
            container.appendChild(msg);
            return;
        }
        _dualsubDualsubs.forEach(ds => {
            const row = document.createElement('div');
            row.style.cssText = 'display:flex;align-items:center;gap:.4rem;margin-bottom:.25rem';

            const chip = document.createElement('span');
            chip.className = 'cat-pill cat-pill-subs';
            chip.textContent = ds.pair;
            chip.title = ds.file;

            const delBtn = document.createElement('button');
            delBtn.textContent = '\u00d7';
            delBtn.title = 'Delete sidecar (so it can be re-rendered)';
            delBtn.style.cssText = 'background:none;border:none;color:var(--muted);cursor:pointer;font-size:.95rem;padding:0 .15rem;line-height:1';
            delBtn.onclick = async () => {
                delBtn.disabled = true;
                try {
                    await del('/api/procula/dualsub-sidecars', { file: ds.file });
                    _dualsubDualsubs = _dualsubDualsubs.filter(x => x.file !== ds.file);
                    dualsubRenderOnDisk();
                } catch (e) {
                    const errMsg = (e.body && e.body.error) || e.message || 'Delete failed';
                    document.getElementById('dualsub-status').textContent = errMsg;
                    delBtn.disabled = false;
                }
            };

            row.appendChild(chip);
            row.appendChild(delBtn);
            container.appendChild(row);
        });
    }

    function dualsubRenderPairs() {
        const container = document.getElementById('dualsub-pairs');
        container.replaceChildren();
        if (_dualsubTracks.length === 0 && _dualsubEmbedded.length === 0) {
            const msg = document.createElement('div');
            msg.style.cssText = 'color:var(--muted);font-size:.8rem;margin-bottom:.75rem';
            msg.textContent = 'No subtitle tracks found (sidecar or embedded).';
            container.appendChild(msg);
            return;
        }
        dualsubAddPair();
    }

    function dualsubMakePairCard(topFile, bottomFile) {
        const card = document.createElement('div');
        card.className = 'dualsub-pair-card';
        card.style.cssText = 'background:var(--bg);border-radius:6px;padding:.55rem .65rem;margin-bottom:.4rem;display:flex;align-items:center;gap:.4rem';

        function makeSelect(selectedFile) {
            const sel = document.createElement('select');
            sel.style.cssText = 'flex:1;background:var(--surface);border:1px solid var(--border);border-radius:4px;padding:.25rem .4rem;font-size:.78rem;color:var(--text)';
            _dualsubTracks.forEach(tr => {
                const opt = document.createElement('option');
                opt.value = tr.file;
                const varLabel = tr.variant === 'hi' ? ' \u2014 hearing impaired' : tr.variant === 'forced' ? ' \u2014 forced' : '';
                opt.textContent = tr.lang.toUpperCase() + varLabel;
                if (tr.file === selectedFile) opt.selected = true;
                sel.appendChild(opt);
            });
            _dualsubEmbedded.forEach(et => {
                const opt = document.createElement('option');
                opt.value = 'embedded:' + et.sub_index;
                const codecLabel = et.codec === 'subrip' ? 'SRT' : et.codec.toUpperCase();
                opt.textContent = et.lang.toUpperCase() + ' \u2014 embedded (' + codecLabel + ')';
                if ('embedded:' + et.sub_index === selectedFile) opt.selected = true;
                sel.appendChild(opt);
            });
            return sel;
        }

        function makeRow(labelText, selectedFile) {
            const row = document.createElement('div');
            row.style.cssText = 'display:flex;align-items:center;gap:.4rem';
            const lbl = document.createElement('span');
            lbl.style.cssText = 'font-size:.65rem;color:var(--muted);width:2.6rem;flex-shrink:0';
            lbl.textContent = labelText;
            const sel = makeSelect(selectedFile);
            row.appendChild(lbl);
            row.appendChild(sel);
            return { row, sel };
        }

        const rows = document.createElement('div');
        rows.style.cssText = 'display:flex;flex-direction:column;gap:.25rem;flex:1';
        const { row: topRow, sel: topSel } = makeRow('Top', topFile);
        const { row: botRow, sel: botSel } = makeRow('Bottom', bottomFile);
        rows.appendChild(topRow);
        rows.appendChild(botRow);
        card.appendChild(rows);

        const btns = document.createElement('div');
        btns.style.cssText = 'display:flex;flex-direction:column;gap:.2rem;align-items:center';

        const swapBtn = document.createElement('button');
        swapBtn.title = 'Swap top/bottom';
        swapBtn.textContent = '\u21c5';
        swapBtn.style.cssText = 'width:1.6rem;height:1.6rem;border-radius:4px;border:1px solid var(--border);background:transparent;color:var(--muted);font-size:.8rem;cursor:pointer';
        swapBtn.addEventListener('click', () => { const t = topSel.value; topSel.value = botSel.value; botSel.value = t; });

        const removeBtn = document.createElement('button');
        removeBtn.title = 'Remove pair';
        removeBtn.textContent = '\u00d7';
        removeBtn.style.cssText = 'width:1.6rem;height:1.6rem;border-radius:4px;border:1px solid var(--border);background:transparent;color:var(--muted);font-size:.75rem;cursor:pointer';
        removeBtn.addEventListener('click', () => card.remove());

        btns.appendChild(swapBtn);
        btns.appendChild(removeBtn);
        card.appendChild(btns);

        card._getTopFile = () => topSel.value;
        card._getBotFile = () => botSel.value;
        return card;
    }

    window.dualsubAddPair = function () {
        const container = document.getElementById('dualsub-pairs');
        const allOptions = [
            ..._dualsubTracks.map(tr => tr.file),
            ..._dualsubEmbedded.map(et => 'embedded:' + et.sub_index),
        ];
        const topFile = allOptions[0] || '';
        const botFile = allOptions[1] || allOptions[0] || '';
        container.appendChild(dualsubMakePairCard(topFile, botFile));
    };

    window.dualsubClose = function () {
        closeDrawer(document.getElementById('dualsub-dialog'), document.getElementById('dualsub-backdrop'));
    };

    window.dualsubToggleManage = function () {
        _dualsubManageOpen = !_dualsubManageOpen;
        document.getElementById('dualsub-profile-editor').style.display = _dualsubManageOpen ? 'block' : 'none';
        document.getElementById('dualsub-manage-btn').textContent = _dualsubManageOpen ? 'Manage \u25b4' : 'Manage \u25be';
        if (_dualsubManageOpen) dualsubLoadProfileEditor();
    };

    function dualsubLoadProfileEditor() {
        const sel = document.getElementById('dualsub-profile-select');
        const prof = _dualsubProfiles.find(p => p.name === sel.value) || _dualsubProfiles[0];
        if (!prof) return;
        document.getElementById('dsp-name').value = prof.name;
        document.getElementById('dsp-font').value = prof.font_name || 'Arial';
        document.getElementById('dsp-size').value = prof.font_size || 52;
        document.getElementById('dsp-outline').value = prof.outline || 2;
        document.getElementById('dsp-margin').value = prof.margin_v || 40;
        document.getElementById('dsp-gap').value = prof.gap || 10;
        _dualsubCurrentLayout = prof.layout || 'stacked_bottom';
        dualsubHighlightLayout(_dualsubCurrentLayout);
        const isBuiltin = !!prof.builtin;
        document.getElementById('dsp-update-btn').disabled = isBuiltin;
        document.getElementById('dsp-delete-btn').disabled = isBuiltin;
    }

    window.dualsubProfileChanged = function () {
        if (_dualsubManageOpen) dualsubLoadProfileEditor();
    };

    window.dualsubSetLayout = function (btn) {
        _dualsubCurrentLayout = btn.dataset.layout;
        dualsubHighlightLayout(_dualsubCurrentLayout);
    };

    function dualsubHighlightLayout(layout) {
        document.querySelectorAll('#dsp-layout-btns button').forEach(b => {
            const active = b.dataset.layout === layout;
            b.style.borderColor = active ? 'var(--accent)' : 'var(--border)';
            b.style.background = active ? 'rgba(255,200,0,.12)' : 'transparent';
            b.style.color = active ? 'var(--accent)' : 'var(--muted)';
        });
    }

    function dualsubReadEditorFields() {
        return {
            name: document.getElementById('dsp-name').value.trim(),
            font_name: document.getElementById('dsp-font').value.trim() || 'Arial',
            font_size: parseInt(document.getElementById('dsp-size').value) || 52,
            outline: parseFloat(document.getElementById('dsp-outline').value) || 2,
            margin_v: parseInt(document.getElementById('dsp-margin').value) || 40,
            gap: parseInt(document.getElementById('dsp-gap').value) || 10,
            layout: _dualsubCurrentLayout,
        };
    }

    window.dualsubSaveAsNew = async function () {
        const fields = dualsubReadEditorFields();
        if (!fields.name) { toast('Profile name required'); return; }
        try {
            const saved = await post('/api/procula/dualsub-profiles', fields);
            _dualsubProfiles.push(saved);
            dualsubRenderProfiles();
            document.getElementById('dualsub-profile-select').value = saved.name;
            toast('Profile saved');
        } catch (e) {
            const errMsg = (e.body && e.body.error) || e.message || 'Save failed';
            toast(errMsg, { error: true });
        }
    };

    window.dualsubUpdateProfile = async function () {
        const fields = dualsubReadEditorFields();
        if (!fields.name) return;
        try {
            await put('/api/procula/dualsub-profiles/' + encodeURIComponent(fields.name), fields);
            const idx = _dualsubProfiles.findIndex(p => p.name === fields.name);
            if (idx !== -1) _dualsubProfiles[idx] = fields;
            dualsubRenderProfiles();
            document.getElementById('dualsub-profile-select').value = fields.name;
            toast('Profile updated');
        } catch (e) {
            const errMsg = (e.body && e.body.error) || e.message || 'Update failed';
            toast(errMsg, { error: true });
        }
    };

    window.dualsubDeleteProfile = async function () {
        const name = document.getElementById('dsp-name').value.trim();
        if (!name) return;
        try {
            await del('/api/procula/dualsub-profiles/' + encodeURIComponent(name));
            _dualsubProfiles = _dualsubProfiles.filter(p => p.name !== name);
            dualsubRenderProfiles();
            document.getElementById('dualsub-profile-editor').style.display = 'none';
            _dualsubManageOpen = false;
            document.getElementById('dualsub-manage-btn').textContent = 'Manage \u25be';
            toast('Profile deleted');
        } catch (e) {
            const errMsg = (e.body && e.body.error) || e.message || 'Delete failed';
            toast(errMsg, { error: true });
        }
    };

    window.dualsubSubmit = async function () {
        const path = store.get('catalog.dualsub.path');
        const statusEl = document.getElementById('dualsub-status');
        if (!path) { statusEl.textContent = 'No file path available.'; return; }

        const profileName = document.getElementById('dualsub-profile-select').value;
        const pairCards = document.querySelectorAll('.dualsub-pair-card');
        const pairs = Array.from(pairCards).map(c => {
            const topVal = c._getTopFile();
            const botVal = c._getBotFile();
            const pair = {};
            if (topVal.startsWith('embedded:')) {
                pair.top_sub_index = parseInt(topVal.slice(9), 10);
            } else {
                pair.top_file = topVal;
            }
            if (botVal.startsWith('embedded:')) {
                pair.bottom_sub_index = parseInt(botVal.slice(9), 10);
            } else {
                pair.bottom_file = botVal;
            }
            return pair;
        }).filter(p => (p.top_file || p.top_sub_index >= 0) && (p.bottom_file || p.bottom_sub_index >= 0));

        if (!pairs.length) { statusEl.textContent = 'Add at least one track pair.'; return; }

        statusEl.textContent = 'Generating\u2026';
        try {
            const data = await post('/api/pelicula/actions?wait=10', {
                action: 'dualsub',
                target: { path },
                params: { profile: profileName, pairs },
            });
            const outputs = (data && data.result && data.result.outputs) || [];
            statusEl.textContent = outputs.length
                ? 'Generated: ' + outputs.map(o => o.split('/').pop()).join(', ')
                : 'No output produced.';
            if (outputs.length) setTimeout(window.dualsubClose, 2000);
        } catch (e) {
            statusEl.textContent = 'Request failed.';
        }
    };

    // ── Catalog controls ──────────────────────────────────────────────────────
    async function catResync(btn) {
        btn.disabled = true;
        try {
            await post('/api/pelicula/catalog/backfill', {});
            toast('Catalog resync started');
            store.set('catalog.loaded', false);
            setTimeout(loadCatalog, 3000);
        } catch (e) {
            const errMsg = (e.body && e.body.error) || e.message || 'unknown';
            toast('Resync failed: ' + errMsg, { error: true });
        } finally {
            btn.disabled = false;
        }
    }

    function catSearch(value) {
        store.set('catalog.query', (value || '').trim());
        store.set('catalog.loaded', false);
        loadCatalog();
    }

    function catSetType(btn, type) {
        store.set('catalog.type', type);
        store.set('catalog.loaded', false);
        document.querySelectorAll('.cat-chip').forEach(b => b.classList.toggle('cat-chip-active', b.dataset.type === type));
        loadCatalog();
    }

    // ── Public API (window.*) ─────────────────────────────────────────────────
    // These are called by the static dialog listeners at the bottom of this file.
    window.catCloseDetail = function () {
        closeDrawer(
            document.getElementById('cat-drawer'),
            document.getElementById('cat-drawer-backdrop')
        );
    };

    // ── Subscribe to store changes ────────────────────────────────────────────
    store.subscribe('catalog.items', renderCatalog);
    store.subscribe('catalog.flaggedRows', renderAttention);

    // ── Event delegation for catalog controls ─────────────────────────────────
    document.getElementById('cat-search').addEventListener('input', e => catSearch(e.target.value));

    document.querySelector('.cat-type-chips').addEventListener('click', e => {
        const chip = e.target.closest('.cat-chip');
        if (chip) catSetType(chip, chip.dataset.type);
    });

    document.getElementById('cat-resync-btn').addEventListener('click', e => catResync(e.currentTarget));

    // ── Replace drawer ────────────────────────────────────────────────────────

    let _replaceItem = null;
    let _replaceLevel = null;
    let _replaceFiles = []; // [{path, arr_id, episode_id, arr_type}]
    let _replaceGen = 0;

    async function openReplaceDrawer(item, level) {
        _replaceItem = item;
        _replaceLevel = level;
        _replaceFiles = [];

        const isTv = (level === 'episode' || level === 'season' || level === 'series');
        const path = level === 'movie'
            ? (item.movieFile ? item.movieFile.path : '')
            : (item.path || '');

        document.getElementById('replace-sub').textContent = item.title || item.seriesTitle || '';
        document.getElementById('replace-path').textContent = path || '';
        document.getElementById('replace-reason').value = '';
        document.getElementById('replace-status').textContent = '';
        document.getElementById('replace-confirm-btn').disabled = false;

        // Show/hide scope section
        const scopeSection = document.getElementById('replace-scope-section');
        scopeSection.style.display = isTv ? '' : 'none';

        if (isTv) {
            // Default to most specific scope
            const episodeRadio = document.getElementById('replace-scope-episode');
            episodeRadio.checked = true;
            const seasonLabel = document.getElementById('replace-scope-season-label');
            if (item.season) seasonLabel.textContent = 'Entire season (Season ' + item.season + ')';

            // Wire scope radio change to update file count preview
            document.querySelectorAll('input[name="replace-scope"]').forEach(r => {
                r.onchange = () => updateReplaceFileCount();
            });
        }

        await updateReplaceFileCount();
        openDrawer(document.getElementById('replace-dialog'), document.getElementById('replace-backdrop'));
    }

    async function updateReplaceFileCount() {
        const gen = ++_replaceGen;
        const countEl = document.getElementById('replace-file-count');
        const scope = document.querySelector('input[name="replace-scope"]:checked');
        const scopeVal = scope ? scope.value : 'episode';
        const level = _replaceLevel;
        const item = _replaceItem;

        if (level === 'movie') {
            _replaceFiles = item.movieFile ? [{
                path: item.movieFile.path,
                arr_id: item.id,
                episode_id: 0,
                arr_type: 'radarr',
            }] : [];
            countEl.textContent = _replaceFiles.length === 1 ? 'Will replace 1 file.' : '';
            return;
        }

        if (level === 'episode' || scopeVal === 'episode') {
            _replaceFiles = item.path ? [{
                path: item.path,
                arr_id: item.id,
                episode_id: item.episodeId || 0,
                arr_type: 'sonarr',
            }] : [];
            countEl.textContent = _replaceFiles.length === 1 ? 'Will replace 1 file.' : '';
            return;
        }

        countEl.textContent = 'Counting\u2026';
        try {
            let episodes = [];
            if (scopeVal === 'season') {
                const seasonNum = item.season || (item.seasons && item.seasons[0] && item.seasons[0].seasonNumber);
                if (seasonNum) {
                    const eps = await get('/api/pelicula/catalog/series/' + item.id + '/season/' + seasonNum);
                    if (eps) episodes = eps.filter(e => e.hasFile || (e.file && e.file.id));
                }
            } else if (scopeVal === 'series') {
                const detail = await get('/api/pelicula/catalog/series/' + item.id);
                if (detail) {
                    for (const s of (detail.seasons || []).filter(s => s.seasonNumber > 0)) {
                        try {
                            const eps = await get('/api/pelicula/catalog/series/' + item.id + '/season/' + s.seasonNumber);
                            if (eps) episodes.push(...eps.filter(e => e.hasFile || (e.file && e.file.id)));
                        } catch (e) { /* non-critical */ }
                    }
                }
            }
            if (gen !== _replaceGen) return;
            _replaceFiles = episodes
                .filter(e => e.file && e.file.path)
                .map(e => ({
                    path: e.file.path,
                    arr_id: item.id,
                    episode_id: e.id,
                    arr_type: 'sonarr',
                }));
            countEl.textContent = 'Will replace ' + _replaceFiles.length + ' file' + (_replaceFiles.length !== 1 ? 's' : '') + '.';
        } catch (e) {
            countEl.textContent = 'Could not count files.';
        }
    }

    window.replaceClose = function () {
        closeDrawer(document.getElementById('replace-dialog'), document.getElementById('replace-backdrop'));
    };

    window.replaceConfirm = async function () {
        if (!_replaceFiles.length) {
            document.getElementById('replace-status').textContent = 'No files to replace.';
            return;
        }
        const reason = document.getElementById('replace-reason').value.trim();
        const statusEl = document.getElementById('replace-status');
        const confirmBtn = document.getElementById('replace-confirm-btn');
        confirmBtn.disabled = true;
        statusEl.textContent = 'Replacing\u2026';

        let succeeded = 0;
        let failed = 0;
        for (const f of _replaceFiles) {
            try {
                const data = await post('/api/pelicula/actions?wait=10', {
                    action: 'replace',
                    target: { path: f.path, arr_type: f.arr_type, arr_id: f.arr_id, episode_id: f.episode_id },
                    params: { reason },
                });
                if (data && data.state === 'failed') { failed++; } else { succeeded++; }
            } catch (e) {
                failed++;
            }
        }

        if (failed === 0) {
            statusEl.textContent = '';
            window.replaceClose();
            toast(succeeded + ' file' + (succeeded !== 1 ? 's' : '') + ' replaced \u2014 Searching for new release\u2026');
        } else {
            statusEl.textContent = succeeded + ' replaced, ' + failed + ' failed.';
            confirmBtn.disabled = false;
        }
    };

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
onTab('catalog', function () {
    const el = document.getElementById('catalog-section');
    if (el && !el.dataset.mounted) {
        el.dataset.mounted = '1';
        mount('catalog', el);
    }
});

if (document.body && document.body.dataset.tab === 'catalog') {
    const el = document.getElementById('catalog-section');
    if (el && !el.dataset.mounted) {
        el.dataset.mounted = '1';
        mount('catalog', el);
    }
}

// ── Dialog static listeners ───────────────────────────────────────────────────
// Functions are set on window when the catalog component mounts; use arrow
// wrappers so the lookup is deferred to call time.

// Catalog detail drawer
document.getElementById('cat-drawer-backdrop').addEventListener('click', () => window.catCloseDetail?.());
document.getElementById('cat-drawer-close-btn').addEventListener('click', () => window.catCloseDetail?.());

// Subtitle search dialog
document.getElementById('sub-search-backdrop').addEventListener('click', () => window.subSearchClose?.());
document.getElementById('sub-search-close-btn').addEventListener('click', () => window.subSearchClose?.());
document.getElementById('sub-search-cancel-btn').addEventListener('click', () => window.subSearchClose?.());
document.getElementById('sub-search-submit-btn').addEventListener('click', () => window.subSearchSubmit?.());

// Dual subtitle dialog
document.getElementById('dualsub-backdrop').addEventListener('click', () => window.dualsubClose?.());
document.getElementById('dualsub-close-btn').addEventListener('click', () => window.dualsubClose?.());
document.getElementById('dualsub-profile-select').addEventListener('change', () => window.dualsubProfileChanged?.());
document.getElementById('dualsub-manage-btn').addEventListener('click', () => window.dualsubToggleManage?.());
document.getElementById('dsp-layout-btns').addEventListener('click', e => {
    const btn = e.target.closest('[data-layout]');
    if (btn) window.dualsubSetLayout?.(btn);
});
document.getElementById('dsp-save-new-btn').addEventListener('click', () => window.dualsubSaveAsNew?.());
document.getElementById('dsp-update-btn').addEventListener('click', () => window.dualsubUpdateProfile?.());
document.getElementById('dsp-delete-btn').addEventListener('click', () => window.dualsubDeleteProfile?.());
document.getElementById('dualsub-add-pair-btn').addEventListener('click', () => window.dualsubAddPair?.());
document.getElementById('dualsub-cancel-btn').addEventListener('click', () => window.dualsubClose?.());
document.getElementById('dualsub-submit-btn').addEventListener('click', () => window.dualsubSubmit?.());

// Replace drawer
document.getElementById('replace-backdrop').addEventListener('click', () => window.replaceClose?.());
document.getElementById('replace-close-btn').addEventListener('click', () => window.replaceClose?.());
document.getElementById('replace-cancel-btn').addEventListener('click', () => window.replaceClose?.());
document.getElementById('replace-confirm-btn').addEventListener('click', () => window.replaceConfirm?.());
