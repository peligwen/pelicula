// nginx/downloads.js
// Downloads component — registered with PeliculaFW; mounted by dashboard.js.
// Depends on: framework.js (PeliculaFW), dashboard.js (tfetch, store, checkDownloads,
//             formatSpeed, formatSize, formatETA).

'use strict';

(function () {
    const { component, html, raw } = PeliculaFW;

    // ── Module-level state ────────────────────────────────────────────────────
    let blocklistState = {};

    // ── Downloads fetch + render ──────────────────────────────────────────────

    async function checkDownloads() {
        try {
            const res = await tfetch('/api/pelicula/downloads');
            if (!res.ok) throw new Error();
            const data = await res.json();
            renderDownloads(data);
            // Update VPN sidebar speeds
            var s = data.stats || {};
            setText('s-dl', formatSpeed(s.dlspeed || 0));
            setText('s-ul', formatSpeed(s.upspeed || 0));
        } catch (e) { console.warn('[pelicula] error:', e); }
    }

    function renderDownloads(data) {
        const list = document.getElementById('downloads-list');
        const statsEl = document.getElementById('dl-stats');
        if (data.stats && statsEl) { statsEl.textContent = data.stats.active + ' active / ' + data.stats.queued + ' queued'; }
        if (!list) return;
        const shown = (data.torrents || []).filter(t => ['downloading','stalledDL','forcedDL','queuedDL','uploading','stalledUP','pausedDL','pausedUP','stoppedDL','stoppedUP','forcedUP'].includes(t.state));
        if (!shown.length) { list.innerHTML = html`<div class="no-items">No active downloads</div>`.str; return; }
        const role = document.body.dataset.role || store.get('role');
        const canPause = role === 'manager' || role === 'admin';
        const canCancel = role === 'admin';
        list.innerHTML = shown.slice(0, 8).map(t => {
            const pct = Math.round(t.progress * 100);
            const speed = formatSpeed(t.dlspeed);
            const eta = t.eta > 0 ? formatETA(t.eta) : '';
            const isPaused = ['pausedDL','pausedUP','stoppedDL','stoppedUP'].includes(t.state);
            const isSeeding = ['uploading','stalledUP','forcedUP','pausedUP','stoppedUP'].includes(t.state);
            const isFetching = t.size === 0 && !isPaused;
            const barClass = isPaused ? 'paused' : isSeeding ? 'seeding' : 'active';
            const pauseBtn = !canPause ? '' : isPaused
                ? html`<button class="dl-btn resume" title="Resume" data-hash="${t.hash}" onclick="dlPauseFromBtn(this,false)">&#9654;</button>`.str
                : html`<button class="dl-btn pause" title="Pause" data-hash="${t.hash}" onclick="dlPauseFromBtn(this,true)">&#9646;&#9646;</button>`.str;
            const cancelBtn = canCancel ? html`<button class="dl-btn cancel" title="Cancel download" data-hash="${t.hash}" data-category="${t.category}" data-name="${t.name}" onclick="dlCancelFromBtn(this,false)">&#10005;</button>`.str : '';
            const blocklistBtn = canCancel ? html`<button class="dl-btn blocklist" title="Remove &amp; blocklist" data-hash="${t.hash}" data-category="${t.category}" data-name="${t.name}" onclick="openBlocklistFromBtn(this)">&#8856;</button>`.str : '';
            const isDone = pct >= 100 && isSeeding;
            const statusText = isPaused ? html`<span class="paused-label">paused</span>`.str
                : isFetching ? html`<span class="fetching-label">Fetching metadata\u2026</span>`.str
                : isDone ? html`<span class="seeding-label">seeding</span>`.str
                : speed + (eta && t.eta < 8640000 ? ' \u00b7 ' + eta : '');
            const sizeText = isFetching ? '\u2014' : pct + '% of ' + formatSize(t.size);
            return html`<div class="download-item"><div class="download-header"><div class="download-name" onclick="this.classList.toggle('expanded')" title="${t.name}">${t.name}</div><div class="download-actions">${raw(pauseBtn)}${raw(cancelBtn)}${raw(blocklistBtn)}</div></div><div class="download-bar-bg"><div class="download-bar ${barClass}" style="width:${pct}%"></div></div><div class="download-meta"><span>${sizeText}</span><span>${raw(statusText)}</span></div></div>`.str;
        }).join('');
    }

    // ── data-* bridge helpers — keep user-controlled strings out of JS string
    // literals in onclick. Also used by pipeline cards in dashboard.js. ────────

    function dlPauseFromBtn(btn, paused) { dlPause(btn.dataset.hash, paused); }
    function dlCancelFromBtn(btn, blocklist) { dlCancel(btn.dataset.hash, btn.dataset.category, btn.dataset.name, blocklist); }
    function openBlocklistFromBtn(btn) { openBlocklistModal(btn.dataset.hash, btn.dataset.category, btn.dataset.name); }

    // ── Download actions ──────────────────────────────────────────────────────

    async function dlPause(hash, paused) {
        try {
            await fetch('/api/pelicula/downloads/pause', {
                method: 'POST', headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({hash, paused})
            });
            setTimeout(checkDownloads, 500);
        } catch (e) { console.warn('[pelicula] error:', e); }
    }

    async function dlCancel(hash, category, name, blocklist, reason) {
        if (!blocklist && !confirm('Cancel download and unmonitor?\n\n' + name)) return;
        try {
            await fetch('/api/pelicula/downloads/cancel', {
                method: 'POST', headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({hash, category, blocklist, reason: reason || ''})
            });
            setTimeout(checkDownloads, 500);
        } catch (e) { console.warn('[pelicula] error:', e); }
    }

    // ── Blocklist modal ───────────────────────────────────────────────────────

    function openBlocklistModal(hash, category, name) {
        blocklistState = {hash, category, name};
        document.getElementById('blocklist-name').textContent = name;
        document.getElementById('blocklist-reason').value = 'quality';
        document.getElementById('blocklist-modal').classList.remove('hidden');
    }

    function closeBlocklistModal() {
        document.getElementById('blocklist-modal').classList.add('hidden');
        blocklistState = {};
    }

    function confirmBlocklist() {
        const {hash, category, name} = blocklistState;
        const reason = document.getElementById('blocklist-reason').value;
        closeBlocklistModal();
        dlCancel(hash, category, name, true, reason);
    }

    // ── Component registration ────────────────────────────────────────────────

    component('downloads', function (el, storeProxy) {
        return {
            render: function () {},   // no template rendering — operates on existing DOM
        };
    });

    // ── Window exports (for onclick handlers in index.html and pipeline cards) ─
    window.checkDownloads       = checkDownloads;
    window.dlPauseFromBtn       = dlPauseFromBtn;
    window.dlCancelFromBtn      = dlCancelFromBtn;
    window.openBlocklistFromBtn = openBlocklistFromBtn;
    window.dlPause              = dlPause;
    window.dlCancel             = dlCancel;
    window.openBlocklistModal   = openBlocklistModal;
    window.closeBlocklistModal  = closeBlocklistModal;
    window.confirmBlocklist     = confirmBlocklist;
}());
