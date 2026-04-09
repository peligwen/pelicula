// ── Resilient fetch (auto-abort after ms) ──
function tfetch(url, opts, ms) {
    ms = ms || 4000;
    const ctrl = new AbortController();
    const t = setTimeout(() => ctrl.abort(), ms);
    return fetch(url, Object.assign({}, opts, {signal: ctrl.signal})).finally(() => clearTimeout(t));
}

// ── Auth ──────────────────────────────────
let currentRole = 'admin'; // default when auth is off

async function checkAuth() {
    try {
        const res = await tfetch('/api/pelicula/auth/check');
        const data = await res.json();
        if (!data.auth) {
            // Auth is off — no login needed, full access
            applyRole('admin', '');
            return;
        }
        if (!data.valid) {
            document.getElementById('login-username').classList.remove('hidden');
            document.getElementById('login-overlay').classList.remove('hidden');
        } else {
            applyRole(data.role || 'admin', data.username || '');
        }
    } catch {
        // Network error — default to locked state rather than granting admin
        document.getElementById('login-overlay').classList.remove('hidden');
    }
}

async function doLogin() {
    const username = document.getElementById('login-username').value;
    const pw = document.getElementById('login-password').value;
    const errEl = document.getElementById('login-error');
    try {
        const res = await fetch('/api/pelicula/auth/login', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({username, password: pw})
        });
        const data = await res.json();
        if (res.ok) {
            document.getElementById('login-overlay').classList.add('hidden');
            errEl.style.display = 'none';
            applyRole(data.role || 'admin', data.username || '');
            refresh();
        } else {
            errEl.style.display = 'block';
        }
    } catch { errEl.style.display = 'block'; }
}

// Apply role-based visibility to UI elements.
// viewer:  no search, no download actions
// manager: search + add, pause/resume; no cancel/blocklist
// admin:   everything
function applyRole(role, username) {
    currentRole = role;
    document.body.dataset.username = username || '';
    const isManager = role === 'manager' || role === 'admin';
    const isAdmin = role === 'admin';

    // Search section
    const searchSection = document.querySelector('.search-section');
    if (searchSection) searchSection.style.display = isManager ? '' : 'none';

    // Admin-only elements (e.g. settings gear icon)
    document.querySelectorAll('.admin-only').forEach(el => {
        el.style.display = isAdmin ? '' : 'none';
    });

    // Requests section: visible to all authenticated users
    const requestsSection = document.getElementById('requests-section');
    if (requestsSection) {
        requestsSection.classList.remove('hidden');
        if (!requestsLoaded) { loadRequests(); requestsLoaded = true; }
        if (isAdmin && !arrMetaLoaded) { loadArrMeta(); arrMetaLoaded = true; }
    }

    // Users section: visible to admins
    const usersSection = document.getElementById('users-section');
    if (usersSection) {
        if (isAdmin) {
            usersSection.classList.remove('hidden');
            if (!usersLoaded) { loadUsers(); loadInvites(); usersLoaded = true; }
        } else {
            usersSection.classList.add('hidden');
        }
    }

    // Download action buttons (rendered dynamically — use a data attribute approach)
    // Store role for use in renderDownloads
    document.body.dataset.role = role;
}

let usersLoaded = false;

document.getElementById('login-username').addEventListener('keydown', e => { if (e.key === 'Enter') document.getElementById('login-password').focus(); });
document.getElementById('login-password').addEventListener('keydown', e => { if (e.key === 'Enter') doLogin(); });

// ── Status + Indexer check ────────────────
async function checkStatus() {
    try {
        const res = await tfetch('/api/pelicula/status');
        if (!res.ok) return;
        const data = await res.json();
        const toast = document.getElementById('toast');
        const hint = document.getElementById('footer-hint');
        if (data.indexers === 0) {
            toast.classList.add('visible');
            if (hint) hint.textContent = 'Prowlarr needs an indexer';
        } else {
            toast.classList.remove('visible');
            if (hint) hint.textContent = '';
        }
    } catch (e) { console.warn('[pelicula] error:', e); }
}

// ── Search ────────────────────────────────
let searchTimeout;
let searchType = '';
let lastResults = [];
const searchInput = document.getElementById('search-input');

// Clear any stale localStorage added-cache from older versions
localStorage.removeItem('peliculaAdded');
const searchResults = document.getElementById('search-results');
const searchFilters = document.getElementById('search-filters');

function setFilter(btn) {
    document.querySelectorAll('.filter-btn').forEach(b => b.classList.remove('active'));
    btn.classList.add('active');
    searchType = btn.dataset.type;
    const q = searchInput.value.trim();
    if (q.length >= 2) doSearch(q);
}

searchInput.addEventListener('input', () => {
    clearTimeout(searchTimeout);
    const q = searchInput.value.trim();
    if (q.length < 2) {
        searchResults.className = 'search-results'; searchResults.innerHTML = '';
        searchFilters.classList.remove('visible');
        lastResults = [];
        return;
    }
    searchFilters.classList.add('visible');
    searchTimeout = setTimeout(() => doSearch(q), 400);
});
async function doSearch(q) {
    searchResults.innerHTML = '<div class="search-searching-msg">Searching</div>';
    searchResults.className = 'search-results searching';
    try {
        const typeParam = searchType ? '&type=' + searchType : '';
        const res = await fetch('/api/pelicula/search?q=' + encodeURIComponent(q) + typeParam);
        const data = await res.json();
        lastResults = data.results || [];
        renderResults(lastResults, false);
    } catch (e) {
        console.warn('[pelicula] search error:', e);
        searchResults.innerHTML = '<div class="no-items">Search unavailable</div>';
        searchResults.className = 'search-results visible';
    }
}
function renderResultCard(r) {
    const poster = r.poster ? `<img src="${r.poster}" alt="">` : '<div class="no-poster"></div>';
    const badge = r.type === 'movie' ? 'Movie' : 'Series';
    const tmdbId = r.tmdbId || 0;
    const tvdbId = r.tvdbId || 0;
    const id = r.type === 'movie' ? tmdbId : tvdbId;
    const idKey = r.type === 'movie' ? 'tmdb' : 'tvdb';
    const added = r.added;
    const isManager = currentRole === 'manager' || currentRole === 'admin';
    // Managers and admins get the direct Add button; viewers get a Request button.
    const actionBtn = isManager
        ? `<button class="${added ? 'search-add added' : 'search-add'}" ${added ? 'disabled' : ''} data-type="${esc(r.type)}" data-tmdb="${tmdbId}" data-tvdb="${tvdbId}" onclick="addMedia(this.dataset.type, this.dataset.type==='movie'?parseInt(this.dataset.tmdb):parseInt(this.dataset.tvdb), this)">${added ? 'Added' : 'Add'}</button>`
        : `<button class="search-request" data-type="${esc(r.type)}" data-tmdb="${tmdbId}" data-tvdb="${tvdbId}" data-title="${esc(r.title)}" data-year="${r.year||0}" data-poster="${esc(r.poster||'')}" onclick="submitRequest(this.dataset.type,parseInt(this.dataset.tmdb),parseInt(this.dataset.tvdb),this.dataset.title,parseInt(this.dataset.year),this.dataset.poster);this.textContent='Requested';this.disabled=true">Request</button>`;
    return `<div class="search-result">${poster}<div class="search-info"><div class="search-title">${esc(r.title)}</div><div class="search-meta">${r.year || ''} &middot; ${badge}</div><div class="search-overview">${esc(r.overview || '')}</div></div>${actionBtn}</div>`;
}
function renderResults(results, collapsed) {
    if (!results.length) {
        searchResults.innerHTML = '<div class="no-items">No results found</div>';
        searchResults.className = 'search-results visible';
        return;
    }
    const items = results.slice(0, 10);
    let html = items.map(r => renderResultCard(r)).join('');
    if (collapsed && items.length > 1) {
        html += `<div class="search-show-more" onclick="expandResults(); event.stopPropagation();">Show <span class="count">${items.length - 1}</span> more result${items.length > 2 ? 's' : ''}</div>`;
    }
    searchResults.innerHTML = html;
    searchResults.className = collapsed ? 'search-results collapsed' : 'search-results visible';
}
function expandResults() {
    searchResults.className = 'search-results visible';
    // Re-render without the "show more" bar
    if (lastResults.length) renderResults(lastResults, false);
    searchFilters.classList.add('visible');
}
async function addMedia(type, id, btn) {
    btn.disabled = true; btn.textContent = '…';
    try {
        const body = type === 'movie' ? {type, tmdbId: id} : {type, tvdbId: id};
        const res = await fetch('/api/pelicula/search/add', { method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body) });
        if (res.ok) {
            // Mark the matching result in lastResults so re-renders reflect the add immediately
            const idKey = type === 'movie' ? 'tmdbId' : 'tvdbId';
            const hit = lastResults.find(r => r[idKey] === id);
            if (hit) { hit.added = true; }
            btn.textContent = 'Added'; btn.classList.add('added');
        } else { btn.textContent = 'Error'; setTimeout(() => { btn.textContent = 'Add'; btn.disabled = false; }, 2000); }
    } catch { btn.textContent = 'Error'; setTimeout(() => { btn.textContent = 'Add'; btn.disabled = false; }, 2000); }
}
function esc(s) { const d = document.createElement('div'); d.textContent = s; return d.innerHTML; }

// Collapse search results on click-away or scroll (show top result + "show more")
document.addEventListener('click', e => {
    if (!e.target.closest('.search-box') && !e.target.closest('.search-results')) {
        if (searchResults.classList.contains('visible') && lastResults.length > 1) {
            renderResults(lastResults, true);
            searchFilters.classList.remove('visible');
        } else if (searchResults.classList.contains('visible')) {
            searchResults.className = 'search-results collapsed';
        }
    }
});
let scrollTick = false;
window.addEventListener('scroll', () => {
    if (scrollTick) return;
    scrollTick = true;
    requestAnimationFrame(() => {
        if (searchResults.classList.contains('visible')) {
            const box = document.querySelector('.search-box').getBoundingClientRect();
            if (box.bottom < 0) {
                if (lastResults.length > 1) {
                    renderResults(lastResults, true);
                } else {
                    searchResults.className = 'search-results collapsed';
                }
                searchFilters.classList.remove('visible');
            }
        }
        scrollTick = false;
    });
});
// Escape blurs the search input (hides results without clearing query)
searchInput.addEventListener('keydown', e => {
    if (e.key === 'Escape') searchInput.blur();
});

// Expand results when focusing search input
searchInput.addEventListener('focus', () => {
    if (searchInput.value.trim().length >= 2 && lastResults.length) {
        renderResults(lastResults, false);
        searchFilters.classList.add('visible');
    }
});

// ── Downloads ─────────────────────────────
async function checkDownloads() {
    try {
        const res = await tfetch('/api/pelicula/downloads');
        if (!res.ok) throw new Error();
        const data = await res.json();
        renderDownloads(data);
        // Update VPN card speeds
        var s = data.stats || {};
        document.getElementById('t-dl').textContent = formatSpeed(s.dlspeed || 0);
        document.getElementById('t-dl').classList.remove('loading');
        document.getElementById('t-ul').textContent = formatSpeed(s.upspeed || 0);
        document.getElementById('t-ul').classList.remove('loading');
        setText('s-dl', formatSpeed(s.dlspeed || 0));
        setText('s-ul', formatSpeed(s.upspeed || 0));
    } catch (e) { console.warn('[pelicula] error:', e); }
}
function renderDownloads(data) {
    const list = document.getElementById('downloads-list');
    const statsEl = document.getElementById('dl-stats');
    if (data.stats) { statsEl.textContent = `${data.stats.active} active / ${data.stats.queued} queued`; }
    const shown = (data.torrents || []).filter(t => ['downloading','stalledDL','forcedDL','queuedDL','uploading','stalledUP','pausedDL','pausedUP','stoppedDL','stoppedUP','forcedUP'].includes(t.state));
    if (!shown.length) { list.innerHTML = '<div class="no-items">No active downloads</div>'; return; }
    const role = document.body.dataset.role || currentRole;
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
            ? `<button class="dl-btn resume" title="Resume" data-hash="${esc(t.hash)}" onclick="dlPauseFromBtn(this,false)">&#9654;</button>`
            : `<button class="dl-btn pause" title="Pause" data-hash="${esc(t.hash)}" onclick="dlPauseFromBtn(this,true)">&#9646;&#9646;</button>`;
        const cancelBtn = canCancel ? `<button class="dl-btn cancel" title="Cancel download" data-hash="${esc(t.hash)}" data-category="${esc(t.category)}" data-name="${esc(t.name)}" onclick="dlCancelFromBtn(this,false)">&#10005;</button>` : '';
        const blocklistBtn = canCancel ? `<button class="dl-btn blocklist" title="Remove &amp; blocklist" data-hash="${esc(t.hash)}" data-category="${esc(t.category)}" data-name="${esc(t.name)}" onclick="openBlocklistFromBtn(this)">&#8856;</button>` : '';
        const isDone = pct >= 100 && isSeeding;
        const statusText = isPaused ? '<span class="paused-label">paused</span>'
            : isFetching ? '<span class="fetching-label">Fetching metadata\u2026</span>'
            : isDone ? '<span class="seeding-label">seeding</span>'
            : `${speed}${eta && t.eta < 8640000 ? ' \u00b7 ' + eta : ''}`;
        const sizeText = isFetching ? '\u2014' : `${pct}% of ${formatSize(t.size)}`;
        return `<div class="download-item"><div class="download-header"><div class="download-name" onclick="this.classList.toggle('expanded')" title="${esc(t.name)}">${esc(t.name)}</div><div class="download-actions">${pauseBtn}${cancelBtn}${blocklistBtn}</div></div><div class="download-bar-bg"><div class="download-bar ${barClass}" style="width:${pct}%"></div></div><div class="download-meta"><span>${sizeText}</span><span>${statusText}</span></div></div>`;
    }).join('');
}

// data-* bridge helpers — keep user-controlled strings out of JS string literals in onclick
function dlPauseFromBtn(btn, paused) { dlPause(btn.dataset.hash, paused); }
function dlCancelFromBtn(btn, blocklist) { dlCancel(btn.dataset.hash, btn.dataset.category, btn.dataset.name, blocklist); }
function openBlocklistFromBtn(btn) { openBlocklistModal(btn.dataset.hash, btn.dataset.category, btn.dataset.name); }
function retryFromBtn(btn) { retryJob(btn.dataset.jobId); }

// Download actions
async function dlPause(hash, paused) {
    try {
        await fetch('/api/pelicula/downloads/pause', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({hash, paused})
        });
        setTimeout(checkPipeline, 500);
    } catch (e) { console.warn('[pelicula] error:', e); }
}
async function dlCancel(hash, category, name, blocklist, reason) {
    if (!blocklist && !confirm('Cancel download and unmonitor?\n\n' + name)) return;
    try {
        await fetch('/api/pelicula/downloads/cancel', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({hash, category, blocklist, reason: reason || ''})
        });
        setTimeout(checkPipeline, 500);
    } catch (e) { console.warn('[pelicula] error:', e); }
}

// Blocklist modal
let blocklistState = {};
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
function formatSpeed(bps) { if (bps > 1048576) return (bps/1048576).toFixed(1)+' MB/s'; if (bps > 1024) return (bps/1024).toFixed(0)+' KB/s'; if (bps > 0) return bps+' B/s'; return 'idle'; }
function formatSize(b) { if (b > 1099511627776) return (b/1099511627776).toFixed(1)+' TB'; if (b > 1073741824) return (b/1073741824).toFixed(1)+' GB'; if (b > 1048576) return (b/1048576).toFixed(0)+' MB'; return (b/1024).toFixed(0)+' KB'; }
function formatETA(s) { if (s > 86400) return Math.floor(s/86400)+'d'; if (s > 3600) return Math.floor(s/3600)+'h '+Math.floor((s%3600)/60)+'m'; if (s > 60) return Math.floor(s/60)+'m'; return s+'s'; }

// ── Services ──────────────────────────────
async function checkServices() {
    const warn = document.getElementById('search-warning');
    try {
        const res = await tfetch('/api/pelicula/status');
        if (!res.ok) throw new Error();
        const data = await res.json();
        const svcMap = data.services || {};
        document.querySelectorAll('a.service').forEach(el => {
            const icon = el.querySelector('.service-status');
            let name = el.dataset.service || '';
            if (!name) {
                const href = el.getAttribute('href') || '';
                if (href.includes('sonarr')) name = 'sonarr';
                else if (href.includes('radarr')) name = 'radarr';
                else if (href.includes('prowlarr')) name = 'prowlarr';
                else if (href.includes('qbt')) name = 'qbittorrent';
                else if (href.includes('jellyfin')) name = 'jellyfin';
                else if (href.includes('procula')) name = 'procula';
                else if (href.includes('bazarr')) name = 'bazarr';
            }
            const up = svcMap[name] === 'up';
            icon.className = 'service-status ' + (up ? 'up' : 'down');
            icon.textContent = up ? '\u2713' : '!';
            el.classList.remove('svc-up', 'svc-down', 'svc-unknown');
            el.classList.add(up ? 'svc-up' : 'svc-down');
        });
        // Update sidebar pips
        Object.keys(svcMap).forEach(name => {
            const pip = document.getElementById('svc-pip-' + name);
            if (!pip) return;
            const up = svcMap[name] === 'up';
            pip.className = 'svc-pip ' + (up ? 'up' : 'down');
            const row = pip.closest('.svc-row');
            if (row) { row.classList.remove('svc-up', 'svc-down', 'svc-unknown'); row.classList.add(up ? 'svc-up' : 'svc-down'); }
        });
        updateSvcTotals();
        // Search depends on Radarr + Sonarr
        const radarrUp = svcMap['radarr'] === 'up';
        const sonarrUp = svcMap['sonarr'] === 'up';
        if (!radarrUp && !sonarrUp) {
            searchInput.disabled = true;
            searchInput.placeholder = 'Search unavailable';
            warn.textContent = 'Radarr and Sonarr are both down — search is disabled';
            warn.className = 'search-warning err';
        } else if (!radarrUp || !sonarrUp) {
            searchInput.disabled = false;
            searchInput.placeholder = 'Search for a title...';
            const down = !radarrUp ? 'Radarr (movies)' : 'Sonarr (TV shows)';
            warn.textContent = down + ' is down — some results may be missing';
            warn.className = 'search-warning warn';
        } else {
            searchInput.disabled = false;
            searchInput.placeholder = 'Search for a title...';
            warn.className = 'search-warning';
        }
    } catch (e) {
        console.warn('[pelicula] status check error:', e);
        document.querySelectorAll('a.service').forEach(el => {
            const d = el.querySelector('.service-status');
            if (d) { d.className = 'service-status unknown'; d.textContent = '\u26a0'; }
            el.classList.remove('svc-up', 'svc-down');
            el.classList.add('svc-unknown');
        });
        document.querySelectorAll('.svc-pip').forEach(pip => {
            pip.className = 'svc-pip unknown';
            const row = pip.closest('.svc-row');
            if (row) { row.classList.remove('svc-up', 'svc-down'); row.classList.add('svc-unknown'); }
        });
        updateSvcTotals();
        searchInput.disabled = true;
        searchInput.placeholder = 'Search unavailable';
        warn.textContent = 'Cannot reach services — search is disabled';
        warn.className = 'search-warning err';
    }
}

// ── Services auto-refresh + totals ────────

let _svcRefreshTimer = null;
let _svcCountdown = 0;
const SVC_INTERVAL = 30; // seconds

function startServicesAutoRefresh() {
    _svcCountdown = SVC_INTERVAL;
    updateSvcCountdown();
    if (_svcRefreshTimer) clearInterval(_svcRefreshTimer);
    _svcRefreshTimer = setInterval(() => {
        if (document.hidden) return;
        _svcCountdown--;
        if (_svcCountdown <= 0) {
            _svcCountdown = SVC_INTERVAL;
            checkServices().then(updateSvcTotals);
        }
        updateSvcCountdown();
    }, 1000);
    document.addEventListener('visibilitychange', () => {
        if (!document.hidden) {
            _svcCountdown = SVC_INTERVAL;
            updateSvcCountdown();
        }
    }, { once: false });
}

function updateSvcCountdown() {
    const el = document.getElementById('svc-refresh-status');
    if (el) el.textContent = _svcCountdown > 0 ? 'next in ' + _svcCountdown + 's' : '';
}

function manualRefreshServices() {
    _svcCountdown = SVC_INTERVAL;
    updateSvcCountdown();
    checkServices().then(updateSvcTotals);
}

function updateSvcTotals() {
    const cards = document.querySelectorAll('.service');
    let up = 0, down = 0;
    cards.forEach(c => {
        if (c.classList.contains('hidden')) return;
        if (c.classList.contains('svc-up')) up++;
        else if (c.classList.contains('svc-down')) down++;
    });
    const el = document.getElementById('svc-totals');
    if (!el) return;
    if (down === 0 && up > 0) {
        el.textContent = 'All ' + up + ' up';
        el.style.color = '#7dda93';
    } else if (down > 0) {
        el.textContent = up + ' up \u00b7 ' + down + ' down';
        el.style.color = '#f87171';
    } else {
        el.textContent = '';
    }
}

// ── Stack actions ──────────────────────────

function toggleStackMenu() {
    const menu = document.getElementById('svc-stack-menu');
    if (menu) menu.classList.toggle('hidden');
}

document.addEventListener('click', (e) => {
    const menu = document.getElementById('svc-stack-menu');
    const wrap = document.querySelector('.svc-stack-menu-wrap');
    if (menu && wrap && !wrap.contains(e.target)) {
        menu.classList.add('hidden');
    }
});

async function stackRestart() {
    const btn = document.getElementById('svc-menu-btn');
    if (!confirm('Restart all stack services? The dashboard will reconnect automatically.')) return;
    toggleStackMenu();
    if (btn) btn.disabled = true;
    try {
        const res = await fetch('/api/pelicula/admin/stack/restart', { method: 'POST' });
        const data = await res.json().catch(() => ({}));
        if (!res.ok) { showAdminToast(data.error || 'Restart failed', true); return; }
        showAdminToast('Stack restarting\u2026');
        setTimeout(() => checkServices().then(updateSvcTotals), 5000);
    } catch (e) {
        // pelicula-api restarted — response was lost. That's fine.
        showAdminToast('Stack restarting\u2026');
        setTimeout(() => checkServices().then(updateSvcTotals), 5000);
    } finally {
        if (btn) btn.disabled = false;
    }
}

function showAdminToast(msg, isError) {
    // Reuse the existing index toast element for one-liners,
    // else fall back to alert for errors.
    if (isError) { alert(msg); return; }
    const el = document.getElementById('toast');
    if (!el) return;
    const prev = el.innerHTML;
    el.innerHTML = '<span class="toast-icon">&#10003;</span><span>' + escapeHtml(msg) + '</span>';
    el.classList.add('visible');
    setTimeout(() => { el.classList.remove('visible'); el.innerHTML = prev; }, 3000);
}

// ── Log viewer modal ────────────────────────

let _logCurrentSvc = '';

function showServiceLogs(e, svc) {
    e.stopPropagation();
    e.preventDefault();
    _logCurrentSvc = svc;
    const modal = document.getElementById('log-modal');
    const title = document.getElementById('log-modal-title');
    const pre = document.getElementById('log-modal-pre');
    if (!modal) return;
    title.textContent = svc + ' logs';
    pre.textContent = 'Loading\u2026';
    modal.classList.remove('hidden');
    fetchServiceLogs(svc);
}

function closeLogModal() {
    const modal = document.getElementById('log-modal');
    if (modal) modal.classList.add('hidden');
    _logCurrentSvc = '';
}

function refreshServiceLogs() {
    if (_logCurrentSvc) fetchServiceLogs(_logCurrentSvc);
}

function copyServiceLogs() {
    const pre = document.getElementById('log-modal-pre');
    const btn = document.getElementById('log-copy-btn');
    if (!pre || !btn) return;
    const text = pre.textContent || '';
    const flash = () => {
        const prev = btn.textContent;
        btn.textContent = 'Copied!';
        setTimeout(() => { btn.textContent = prev; }, 1500);
    };
    if (navigator.clipboard) {
        navigator.clipboard.writeText(text).then(flash).catch(() => {
            const r = document.createRange();
            r.selectNodeContents(pre);
            const sel = window.getSelection();
            sel.removeAllRanges(); sel.addRange(r);
        });
    }
}

async function fetchServiceLogs(svc) {
    const pre = document.getElementById('log-modal-pre');
    const btn = document.getElementById('log-refresh-btn');
    if (btn) btn.disabled = true;
    try {
        const res = await fetch('/api/pelicula/admin/logs?svc=' + encodeURIComponent(svc) + '&tail=200');
        if (!res.ok) {
            const d = await res.json().catch(() => ({}));
            pre.textContent = 'Error: ' + (d.error || res.status);
            return;
        }
        const text = await res.text();
        pre.textContent = text || '(no output)';
        pre.scrollTop = pre.scrollHeight;
    } catch (e) {
        pre.textContent = 'Network error: ' + e.message;
    } finally {
        if (btn) btn.disabled = false;
    }
}

// Close log modal on overlay click
document.getElementById('log-modal')?.addEventListener('click', (e) => {
    if (e.target === document.getElementById('log-modal')) closeLogModal();
});

// ── VPN Telemetry ─────────────────────────
async function checkVPN() {
    const vpnEl = document.getElementById('t-vpn');
    const regionEl = document.getElementById('t-region');
    const portEl = document.getElementById('t-port');
    const badge = document.getElementById('vpn-status-badge');
    const desc = document.getElementById('vpn-desc');
    const card = document.getElementById('vpn-card');
    try {
        const [ipResult, portResult] = await Promise.allSettled([
            tfetch('/api/vpn/v1/publicip/ip'),
            tfetch('/api/vpn/v1/portforward')
        ]);
        const ipRes = ipResult.status === 'fulfilled' ? ipResult.value : null;
        const portRes = portResult.status === 'fulfilled' ? portResult.value : null;
        if (ipRes && ipRes.ok) {
            const data = await ipRes.json();
            vpnEl.setAttribute('data-ip', data.public_ip || '?');
            vpnEl.textContent = '***.***';
            vpnEl.className = 'vpn-stat-val vpn-ok';
            regionEl.textContent = data.country || '\u2014';
            regionEl.classList.remove('loading');
            badge.className = 'service-status up';
            badge.textContent = '\u2713';
            desc.textContent = 'Connected';
            card.classList.remove('vpn-down', 'svc-down', 'svc-unknown');
            card.classList.add('svc-up');
            setText('s-region', data.country || '\u2014');
        } else if (!ipRes) {
            throw new Error('VPN timeout');
        }
        if (portRes && portRes.ok) {
            const pd = await portRes.json();
            portEl.textContent = pd.port || '?';
            portEl.classList.remove('loading');
            setText('s-port', pd.port || '\u2014');
        }
    } catch (e) {
        console.warn('[pelicula] VPN telemetry error:', e);
        vpnEl.textContent = '---';
        vpnEl.className = 'vpn-stat-val vpn-err';
        regionEl.textContent = '-'; regionEl.classList.remove('loading');
        portEl.textContent = '-'; portEl.classList.remove('loading');
        badge.className = 'service-status down';
        badge.textContent = '!';
        desc.textContent = 'Down — downloads paused';
        card.classList.remove('svc-up', 'svc-unknown');
        card.classList.add('vpn-down', 'svc-down');
        setText('s-region', '\u2014');
        setText('s-port', '\u2014');
    }
}

function setText(id, val) { const el = document.getElementById(id); if (el) el.textContent = val; }

function fmtUptime(secs) {
    const s = Math.floor(secs);
    const d = Math.floor(s / 86400);
    const h = Math.floor((s % 86400) / 3600);
    const m = Math.floor((s % 3600) / 60);
    return d > 0 ? `${d}d ${h}h` : `${h}h ${m}m`;
}

function fmtBytes(b) {
    if (b >= 1e12) return (b / 1e12).toFixed(1) + '\u202fTB';
    if (b >= 1e9)  return (b / 1e9).toFixed(0)  + '\u202fGB';
    return (b / 1e6).toFixed(0) + '\u202fMB';
}

async function checkHost() {
    try {
        const res = await tfetch('/api/pelicula/host');
        if (!res.ok) throw new Error();
        const d = await res.json();
        setText('s-uptime', fmtUptime(d.uptime_seconds || 0));
        if (d.disk && d.disk.total > 0) {
            setText('s-space', `${fmtBytes(d.disk.free)} free / ${fmtBytes(d.disk.total)}`);
            const bar = document.getElementById('s-space-bar');
            if (bar) bar.style.width = Math.round(d.disk.used_pct) + '%';
        }
        if (d.library) {
            const parts = [];
            if (d.library.movies) parts.push(`${d.library.movies} movies`);
            if (d.library.series) parts.push(`${d.library.series} series`);
            setText('s-library', parts.join(' \u00b7 ') || '\u2014');
        }
    } catch (e) { console.warn('[pelicula] host error:', e); }
}

function updateTimestamp() { document.getElementById('footer-time').textContent = new Date().toLocaleTimeString(); }
function toggleVpnFlip(e) { e.stopPropagation(); document.getElementById('vpn-card').classList.toggle('flipped'); }
document.getElementById('t-vpn').addEventListener('click', function() {
    const ip = this.getAttribute('data-ip');
    if (!ip || ip === '?') return;
    this.textContent = this.textContent === ip ? '***.***' : ip;
});
// ── Notifications bell ────────────────────
let lastSeenTs = localStorage.getItem('peliculaLastSeen') || '1970-01-01T00:00:00Z';

async function checkNotifications() {
    try {
        const res = await tfetch('/api/pelicula/notifications');
        if (!res.ok) return;
        const events = await res.json();
        renderNotifications(events);
        renderActivity(events);
    } catch (e) { console.warn('[pelicula] error:', e); }
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
        dropdown.innerHTML = '<div class="notif-empty">No notifications</div>';
        return;
    }

    dropdown.innerHTML = events.slice(0, 20).map(e => {
        const isUnread = e.timestamp > lastSeenTs;
        const typeClass = notifClass(e.type);
        const icon = notifIcon(e.type);
        const time = formatNotifTime(e.timestamp);
        return `<div class="notif-item ${isUnread ? 'unread' : ''} ${typeClass}">
            <span class="notif-icon">${icon}</span>
            <div class="notif-body">
                <div class="notif-msg">${esc(e.message)}</div>
                <div class="notif-time">${time}</div>
            </div>
        </div>`;
    }).join('');
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

// Close notification dropdown on click outside
document.addEventListener('click', e => {
    if (!e.target.closest('#bell-wrap')) {
        document.getElementById('notif-dropdown').classList.add('hidden');
    }
});

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

// ── Activity feed ─────────────────────────
function renderActivity(events) {
    const section = document.getElementById('activity-section');
    const list = document.getElementById('activity-list');
    if (!section || !list) return;
    if (!Array.isArray(events) || !events.length) {
        list.innerHTML = '<div class="activity-empty">No recent activity yet.</div>';
        return;
    }
    list.innerHTML = events.slice(0, 15).map(e => {
        const icon = notifIcon(e.type);
        const cls = notifClass(e.type);
        const time = formatNotifTime(e.timestamp);
        return `<div class="activity-item ${cls}">
            <span class="activity-icon">${icon}</span>
            <span class="activity-msg">${esc(e.message)}</span>
            <span class="activity-time">${time}</span>
        </div>`;
    }).join('');
}

// ── Storage Management ────────────────────
async function checkStorage() {
    try {
        const res = await tfetch('/api/pelicula/storage');
        if (!res.ok) return;
        const data = await res.json();
        const filesystems = Array.isArray(data.filesystems) ? data.filesystems : [];
        if (!filesystems.length) return;
        document.getElementById('storage-section').classList.remove('hidden');
        renderStorage(data);
        renderStorageMetrics(data);
        renderStorageFolders(data);
        renderStorageTimestamp(data.timestamp);
    } catch (e) { console.warn('[pelicula] storage error:', e); }
}

// Load threshold settings into the Settings lane (admin only, best-effort)
async function loadStorageSettings() {
    try {
        const res = await tfetch('/api/pelicula/procula-settings');
        if (!res.ok) return;
        const cfg = await res.json();
        const warnEl = document.getElementById('sm-warn-pct');
        const critEl = document.getElementById('sm-crit-pct');
        if (warnEl && cfg.storage_warning_pct) warnEl.value = cfg.storage_warning_pct;
        if (critEl && cfg.storage_critical_pct) critEl.value = cfg.storage_critical_pct;
        if (warnEl) warnEl.addEventListener('change', saveStorageThreshold);
        if (critEl) critEl.addEventListener('change', saveStorageThreshold);
    } catch (e) { /* non-admin: silently skip */ }
}

async function saveStorageThreshold() {
    const warn = parseInt(document.getElementById('sm-warn-pct')?.value, 10);
    const crit = parseInt(document.getElementById('sm-crit-pct')?.value, 10);
    if (isNaN(warn) || isNaN(crit)) return;
    try {
        const res = await tfetch('/api/pelicula/procula-settings', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ storage_warning_pct: warn, storage_critical_pct: crit })
        });
        if (res.ok) {
            ['sm-warn-pct', 'sm-crit-pct'].forEach(id => {
                const el = document.getElementById(id);
                if (!el) return;
                el.classList.add('saved');
                setTimeout(() => el.classList.remove('saved'), 1200);
            });
        }
    } catch (e) { console.warn('[pelicula] save storage threshold error:', e); }
}

async function scanStorageNow() {
    const btn = document.getElementById('storage-scan-btn');
    if (btn) { btn.disabled = true; btn.textContent = 'Scanning\u2026'; }
    try {
        await tfetch('/api/pelicula/storage/scan', { method: 'POST' });
        await checkStorage();
    } catch (e) { console.warn('[pelicula] scan error:', e); }
    if (btn) { btn.disabled = false; btn.textContent = 'Scan now'; }
}

function folderColor(label) {
    const map = { downloads: '#7dda93', movies: '#7080e8', tv: '#40c8a8', processing: '#f0c060' };
    return map[(label || '').toLowerCase()] || '#888';
}

function renderStorageMetrics(data) {
    const filesystems = data.filesystems || [];
    let free = 0, pelicula = 0, allKnown = true;
    let hasCrit = false, hasWarn = false;
    for (const fs of filesystems) {
        free += fs.available || 0;
        for (const f of (fs.folders || [])) {
            if (f.size < 0) allKnown = false;
            else pelicula += f.size;
        }
        if (fs.status === 'critical') hasCrit = true;
        else if (fs.status === 'warning') hasWarn = true;
    }
    const freeEl = document.getElementById('sm-metric-free');
    const pelEl = document.getElementById('sm-metric-pelicula');
    const statEl = document.getElementById('sm-metric-status');
    if (freeEl) freeEl.textContent = formatSize(free);
    if (pelEl) pelEl.textContent = allKnown ? formatSize(pelicula) : 'Calculating\u2026';
    if (statEl) {
        if (hasCrit) { statEl.textContent = 'Critical'; statEl.className = 'um-metric-num sm-status-critical'; }
        else if (hasWarn) { statEl.textContent = 'Warning'; statEl.className = 'um-metric-num sm-status-warning'; }
        else { statEl.textContent = 'Healthy'; statEl.className = 'um-metric-num sm-status-ok'; }
    }
}

function renderStorageFolders(data) {
    const el = document.getElementById('sm-folder-list');
    if (!el) return;
    const filesystems = data.filesystems || [];
    // Aggregate across filesystems by label
    const totals = {};
    let grandTotal = 0;
    for (const fs of filesystems) {
        for (const f of (fs.folders || [])) {
            if (f.size < 0) continue;
            totals[f.label] = (totals[f.label] || 0) + f.size;
            grandTotal += f.size;
        }
    }
    if (!Object.keys(totals).length) { el.innerHTML = '<div class="sm-last-scan">No data yet</div>'; return; }
    el.innerHTML = Object.entries(totals).sort((a,b) => b[1]-a[1]).map(([label, size]) => {
        const pct = grandTotal > 0 ? (size / grandTotal * 100).toFixed(0) : 0;
        const color = folderColor(label);
        return `<div class="sm-folder-row">
            <div class="sm-folder-dot" style="background:${color}"></div>
            <div class="sm-folder-label">${esc(label)}</div>
            <div class="sm-folder-size">${formatSize(size)}</div>
            <div class="sm-folder-pct">${pct}%</div>
        </div>`;
    }).join('');
}

function renderStorageTimestamp(ts) {
    const el = document.getElementById('sm-last-scan');
    if (!el || !ts) return;
    const d = new Date(ts);
    const diffMin = Math.round((Date.now() - d.getTime()) / 60000);
    el.textContent = diffMin < 2 ? 'just now' : diffMin < 60 ? diffMin + ' min ago' : d.toLocaleTimeString();
}

function renderStorage(data) {
    const list = document.getElementById('storage-list');
    if (!list) return;
    const filesystems = Array.isArray(data.filesystems) ? data.filesystems : [];

    list.innerHTML = filesystems.map(fs => {
        const pct = Math.round(fs.used_pct || 0);
        const folders = Array.isArray(fs.folders) ? fs.folders : [];
        const diskLabel = folders.map(f => esc(f.label)).join(', ') || esc(fs.fs_id);

        let oursTotal = 0, allKnown = true;
        for (const f of folders) {
            if (f.size < 0) allKnown = false;
            else oursTotal += f.size;
        }
        const otherUsed = Math.max(0, fs.used - oursTotal);

        const folderSegs = fs.total > 0 ? folders.map(f => {
            if (f.size < 0) return '';
            const w = (f.size / fs.total * 100).toFixed(2);
            return `<div class="storage-seg" style="width:${w}%;background:${folderColor(f.label)}"></div>`;
        }).join('') : '';
        const otherW = fs.total > 0 ? Math.max(0, otherUsed / fs.total * 100).toFixed(2) : 0;
        const otherSeg = otherW > 0
            ? `<div class="storage-seg storage-seg-other" style="width:${otherW}%"></div>` : '';

        const showFolders = folders.length > 1;
        const folderRows = folders.map(f => {
            const folderPct = (fs.total > 0 && f.size >= 0)
                ? (f.size / fs.total * 100).toFixed(2) : 0;
            const sizeText = f.size < 0 ? 'Calculating\u2026' : formatSize(f.size);
            const color = folderColor(f.label);
            return `<div class="storage-folder">
                <div class="storage-folder-header">
                    <span class="storage-folder-label" style="color:${color}">${esc(f.label)}</span>
                    <span class="storage-folder-size">${sizeText}</span>
                </div>
                <div class="download-bar-bg"><div class="download-bar storage-bar-folder" style="width:${folderPct}%;background:${color}"></div></div>
            </div>`;
        }).join('');

        const expandable = showFolders
            ? `<div class="storage-folders collapsed">${folderRows}</div>` : '';
        const chevron = showFolders
            ? `<span class="storage-chevron">&#9660;</span>` : '';
        const headerClick = showFolders ? ' onclick="toggleStorageDisk(this.parentElement)"' : '';
        const oursTotalText = allKnown ? formatSize(oursTotal) : 'Calculating\u2026';

        return `<div class="download-item storage-disk">
            <div class="download-header"${headerClick}>
                <div class="download-name">${diskLabel}</div>
                <div class="download-actions">
                    <span class="dl-size">${formatSize(fs.used)} / ${formatSize(fs.total)}</span>
                    ${chevron}
                </div>
            </div>
            <div class="storage-stacked-bar">${folderSegs}${otherSeg}</div>
            <div class="download-meta">
                <span>Pelicula: ${oursTotalText}</span>
                <span>${formatSize(fs.available)} free · ${pct}%</span>
            </div>
            ${expandable}
        </div>`;
    }).join('');
}

function toggleStorageDisk(el) {
    const folders = el.querySelector('.storage-folders');
    const chevron = el.querySelector('.storage-chevron');
    if (!folders) return;
    const collapsed = folders.classList.toggle('collapsed');
    if (chevron) chevron.innerHTML = collapsed ? '&#9660;' : '&#9650;';
}

// ── Update checker ────────────────────────
async function checkUpdates() {
    try {
        const res = await fetch('/api/pelicula/updates');
        if (!res.ok) return;
        const data = await res.json();
        if (!data || typeof data !== 'object') return;
        const el = document.getElementById('footer-update');
        if (data.update_available && data.latest_version) {
            el.innerHTML = `&#8593; Update available: <a href="https://github.com/peligwen/pelicula/releases" target="_blank" rel="noopener">${esc(data.latest_version)}</a> &nbsp;&bull;&nbsp;`;
        }
    } catch (e) { console.warn('[pelicula] updates error:', e); }
}

// ── Processing section ────────────────────
async function checkProcessing() {
    try {
        const res = await tfetch('/api/pelicula/processing');
        if (!res.ok) return;
        const data = await res.json();
        renderProcessing(data);
    } catch (e) { console.warn('[pelicula] error:', e); }
}

function renderProcessing(data) {
    const section = document.getElementById('processing-section');
    const activeEl = document.getElementById('processing-active');
    const failedSection = document.getElementById('processing-failed');
    const failedList = document.getElementById('processing-failed-list');
    const statsEl = document.getElementById('processing-stats');

    const jobs = Array.isArray(data.jobs) ? data.jobs : [];
    const activeJobs = jobs.filter(j => j.state === 'queued' || j.state === 'processing');
    const failedJobs = jobs.filter(j => j.state === 'failed');

    if (!activeJobs.length && !failedJobs.length) {
        section.style.display = 'none';
        return;
    }
    section.style.display = '';

    const status = data.status && data.status.queue ? data.status.queue : {};
    const processing = status.processing || 0;
    const pending = status.queued || status.pending || 0;
    const completed = status.completed || 0;
    statsEl.textContent = `${pending} queued / ${processing} active / ${completed} done`;

    activeEl.innerHTML = activeJobs.map(j => renderJobCard(j)).join('');

    if (failedJobs.length) {
        failedSection.style.display = '';
        failedList.innerHTML = failedJobs.map(j => renderJobCard(j)).join('');
    } else {
        failedSection.style.display = 'none';
    }
}

function renderJobCard(j) {
    const pct = Math.round((j.progress || 0) * 100);
    const stageName = {validate:'Validating', process:'Processing', catalog:'Cataloging', done:'Done'}[j.stage] || j.stage;
    const stateClass = j.state === 'failed' ? 'proc-failed' : 'proc-active';
    const barClass = j.state === 'failed' ? 'proc-bar-failed' : 'proc-bar-active';
    const title = j.source ? j.source.title : j.id;

    const retryBtn = j.state === 'failed'
        ? `<button class="dl-btn resume" title="Retry" data-job-id="${esc(j.id)}" onclick="retryFromBtn(this)">&#8635;</button>`
        : '';
    const cancelBtn = (j.state === 'queued' || j.state === 'processing' || j.state === 'failed')
        ? `<button class="dl-btn cancel" title="Cancel" data-job-id="${esc(j.id)}" onclick="cancelJobFromBtn(this)">&#x2715;</button>`
        : '';
    const viewLogLink = `<a class="dl-btn" href="/procula/#job=${esc(j.id)}" target="_blank" title="View in Procula" style="font-size:0.7rem;padding:0.2rem 0.4rem;text-decoration:none">&#9654;</a>`;

    const missingSubsBadge = (j.missing_subs && j.missing_subs.length)
        ? `<span class="proc-badge proc-warn" title="Bazarr will fetch these">Missing subs: ${j.missing_subs.map(esc).join(', ')}</span>`
        : '';

    let checksHTML = '';
    if (j.state === 'failed' && j.validation) {
        const checks = j.validation.checks || {};
        const checkOrder = ['integrity', 'duration', 'sample'];
        checksHTML = `<div class="proc-check-list">${checkOrder.map(k => {
            const v = checks[k] || 'skip';
            const cls = ['pass', 'fail', 'warn'].includes(v) ? v : 'skip';
            return `<span class="proc-check proc-check-${cls}">${k}: ${v}</span>`;
        }).join('')}</div>`;
    }

    let metaRight = '';
    if (j.transcode_profile) {
        metaRight = esc(j.transcode_profile) + (j.transcode_decision ? ' · ' + esc(j.transcode_decision) : '');
    } else if (j.transcode_eta > 0) {
        metaRight = `ETA ${Math.round(j.transcode_eta)}s`;
    }

    return `<div class="download-item">
        <div class="download-header">
            <div class="download-name">${esc(title)}</div>
            <div class="download-actions">
                <span class="proc-badge ${stateClass}">${stageName}</span>
                ${missingSubsBadge}
                ${retryBtn}${cancelBtn}${viewLogLink}
            </div>
        </div>
        <div class="download-bar-bg"><div class="download-bar ${barClass}" style="width:${pct}%"></div></div>
        <div class="download-meta">
            <span>${pct}%${j.error ? ' — ' + esc(j.error) : ''}</span>
            <span>${metaRight}</span>
        </div>
        ${checksHTML}
    </div>`;
}

async function retryJob(id) {
    try {
        await fetch(`/api/procula/jobs/${id}/retry`, {method: 'POST'});
        setTimeout(checkPipeline, 500);
    } catch (e) { console.warn('[pelicula] retry error:', e); }
}

async function cancelJob(id) {
    try {
        await fetch(`/api/procula/jobs/${id}/cancel`, {method: 'POST'});
        setTimeout(checkPipeline, 500);
    } catch (e) { console.warn('[pelicula] cancel error:', e); }
}

function cancelJobFromBtn(btn) { cancelJob(btn.dataset.jobId); }

// ── Pipeline board ────────────────────────
const LANE_BADGE = {
    downloading:    '',
    imported:       '<span class="proc-badge proc-active">Imported</span>',
    validating:     '<span class="proc-badge proc-active">Validating</span>',
    processing:     '<span class="proc-badge proc-active">Processing</span>',
    cataloging:     '<span class="proc-badge proc-active">Cataloging</span>',
    completed:      '<span class="proc-badge proc-done">Done</span>',
    needs_attention:'<span class="proc-badge proc-failed">Failed</span>',
};
const ACTIVE_LANES = ['downloading', 'imported', 'validating', 'processing', 'cataloging'];

async function checkPipeline() {
    try {
        const res = await tfetch('/api/pelicula/pipeline');
        if (!res.ok) throw new Error();
        const data = await res.json();
        renderPipeline(data);
        // Update VPN card speed stats (replaces checkDownloads)
        const s = data.stats || {};
        document.getElementById('t-dl').textContent = formatSpeed(s.dl_speed || 0);
        document.getElementById('t-dl').classList.remove('loading');
        document.getElementById('t-ul').textContent = formatSpeed(s.up_speed || 0);
        document.getElementById('t-ul').classList.remove('loading');
    } catch (e) { console.warn('[pelicula] pipeline error:', e); }
}

function renderPipeline(data) {
    const section = document.getElementById('pipeline-section');
    const statsEl = document.getElementById('pipeline-stats');
    const attentionEl = document.getElementById('pipeline-attention');
    const attentionList = document.getElementById('pipeline-attention-list');
    const completedWrap = document.getElementById('pipeline-completed-wrap');
    if (!section) return;

    const lanes = data.lanes || {};
    const stats = data.stats || {};

    // ── FLIP First: snapshot card positions before DOM changes ────────────────
    const firstRects = {};
    section.querySelectorAll('[data-key]').forEach(function(el) {
        firstRects[el.dataset.key] = el.getBoundingClientRect();
    });

    // Stats summary in header
    const parts = [];
    if (stats.active > 0) parts.push(stats.active + ' active');
    if (stats.failed > 0) parts.push(stats.failed + ' failed');
    if (statsEl) statsEl.textContent = parts.join(' / ');

    // Footer pipeline count
    const footerCount = document.getElementById('footer-pipeline-count');
    if (footerCount) {
        if (stats.active > 0) footerCount.textContent = stats.active + ' on the way';
        else if (stats.failed > 0) footerCount.textContent = stats.failed + ' needs attention';
        else footerCount.textContent = '';
    }

    // Needs attention
    const failedItems = lanes['needs_attention'] || [];
    if (failedItems.length && attentionEl && attentionList) {
        attentionEl.style.display = '';
        attentionList.innerHTML = failedItems.map(function(item) { return renderPipelineCard(item); }).join('');
    } else if (attentionEl) {
        attentionEl.style.display = 'none';
    }

    // Active lanes — always visible; empty lanes show a dash placeholder
    for (const laneKey of ACTIVE_LANES) {
        const items = lanes[laneKey] || [];
        const laneEl = document.getElementById('pipeline-lane-' + laneKey);
        const cardsEl = document.getElementById('pipeline-cards-' + laneKey);
        if (!laneEl || !cardsEl) continue;
        if (!items.length) {
            cardsEl.innerHTML = '<div class="pl-empty">—</div>';
        } else {
            cardsEl.innerHTML = items.map(function(item) { return renderPipelineCard(item); }).join('');
        }
    }

    // Completed tail
    const completedItems = lanes['completed'] || [];
    if (completedItems.length && completedWrap) {
        completedWrap.style.display = '';
        const el = document.getElementById('pipeline-cards-completed');
        if (el) el.innerHTML = completedItems.map(function(item) { return renderPipelineCard(item); }).join('');
    } else if (completedWrap) {
        completedWrap.style.display = 'none';
    }

    section.style.display = '';

    // ── FLIP Last+Invert+Play: animate cards that moved ───────────────────────
    section.querySelectorAll('[data-key]').forEach(function(el) {
        const key = el.dataset.key;
        const first = firstRects[key];
        if (!first) {
            // New card: fade in
            el.style.opacity = '0';
            requestAnimationFrame(function() {
                el.style.transition = 'opacity 0.25s';
                el.style.opacity = '';
                var cleanup = function() { el.style.transition = ''; el.removeEventListener('transitionend', cleanup); };
                el.addEventListener('transitionend', cleanup);
            });
            return;
        }
        var last = el.getBoundingClientRect();
        var dx = first.left - last.left;
        var dy = first.top - last.top;
        if (Math.abs(dx) < 1 && Math.abs(dy) < 1) return; // no visible movement
        // Invert
        el.style.transform = 'translate(' + dx + 'px,' + dy + 'px)';
        el.style.transition = 'none';
        // Play (two rAFs ensure the browser commits the inverted position first)
        requestAnimationFrame(function() {
            requestAnimationFrame(function() {
                el.style.transform = '';
                el.style.transition = 'transform 0.35s cubic-bezier(0.2,0,0.2,1)';
                var cleanup = function() { el.style.transition = ''; el.removeEventListener('transitionend', cleanup); };
                el.addEventListener('transitionend', cleanup);
            });
        });
    });
}

function renderPipelineCard(item) {
    const pct = Math.round((item.progress || 0) * 100);
    const isFailed = item.state === 'failed';
    const isDone = item.state === 'done';
    const isPaused = item.state === 'paused';
    const title = item.title || (item.source && item.source.qbt_hash) || item.key || '?';
    const year = item.year ? ' (' + item.year + ')' : '';
    const fullTitle = title + year;

    const barClass = isFailed ? 'proc-bar-failed'
        : isDone ? 'proc-bar-done'
        : isPaused ? 'paused'
        : item.lane === 'imported' ? 'seeding'
        : item.lane === 'processing' ? 'proc-bar-active'
        : 'active';

    // Right-side meta: speed, ETA, or detail
    let speedText = '';
    if (item.lane === 'downloading' && item.speed_down > 0) {
        speedText = formatSpeed(item.speed_down);
        if (item.eta_seconds > 0 && item.eta_seconds < 8640000) {
            speedText += ' \u00b7 ' + formatETA(item.eta_seconds);
        }
    } else if (item.lane === 'imported' && item.speed_up > 0) {
        speedText = '\u2191 ' + formatSpeed(item.speed_up);
    } else if (item.lane === 'processing' && item.eta_seconds > 0) {
        speedText = 'ETA ' + formatETA(item.eta_seconds);
    } else if (item.detail) {
        speedText = esc(item.detail);
    }

    // Left-side meta: pct + error snippet
    const metaLeft = pct + '%' + (item.error ? ' \u2014 ' + esc(item.error.substring(0, 80)) : '');

    const badge = LANE_BADGE[item.lane] || '';
    const missingSubsBadge = (item.missing_subs && item.missing_subs.length)
        ? '<span class="proc-badge proc-warn" title="Bazarr will fetch these">Missing subs: ' + item.missing_subs.map(esc).join(', ') + '</span>'
        : '';

    const role = document.body.dataset.role || currentRole;
    const canAdmin = role === 'admin';
    const canManage = role === 'manager' || role === 'admin';
    const actions = item.actions || [];
    const src = item.source || {};
    const qbtHash = esc(src.qbt_hash || '');
    const arrType = esc(src.arr_type || '');
    const jobId = esc(src.job_id || '');
    const safeTitle = esc(fullTitle);

    let actionBtns = '';
    if (actions.includes('pause') && canManage) {
        actionBtns += isPaused
            ? '<button class="dl-btn resume" title="Resume" data-hash="' + qbtHash + '" onclick="dlPauseFromBtn(this,false)">&#9654;</button>'
            : '<button class="dl-btn pause" title="Pause" data-hash="' + qbtHash + '" onclick="dlPauseFromBtn(this,true)">&#9646;&#9646;</button>';
    }
    if (actions.includes('cancel') && canAdmin) {
        actionBtns += '<button class="dl-btn cancel" title="Cancel" data-hash="' + qbtHash + '" data-category="' + arrType + '" data-name="' + safeTitle + '" onclick="dlCancelFromBtn(this,false)">&#10005;</button>';
    }
    if (actions.includes('blocklist') && canAdmin) {
        actionBtns += '<button class="dl-btn blocklist" title="Remove &amp; blocklist" data-hash="' + qbtHash + '" data-category="' + arrType + '" data-name="' + safeTitle + '" onclick="openBlocklistFromBtn(this)">&#8856;</button>';
    }
    if (actions.includes('retry') && canAdmin) {
        actionBtns += '<button class="dl-btn resume" title="Retry" data-job-id="' + jobId + '" onclick="retryFromBtn(this)">&#8635;</button>';
    }
    if (actions.includes('cancel_job') && canAdmin) {
        actionBtns += '<button class="dl-btn cancel" title="Cancel job" data-job-id="' + jobId + '" onclick="cancelJobFromBtn(this)">&#10005;</button>';
    }
    if (actions.includes('view_log') && src.job_id) {
        actionBtns += '<a class="dl-btn" href="/procula/#job=' + jobId + '" target="_blank" title="View log" style="font-size:0.7rem;padding:0.2rem 0.4rem;text-decoration:none">&#9654;</a>';
    }
    if (actions.includes('dismiss') && canAdmin) {
        actionBtns += '<button class="dl-btn" title="Dismiss" data-job-id="' + jobId + '" onclick="dismissJobFromBtn(this)" style="color:#555">&#10006;</button>';
    }

    // Validation checks for failed items
    let checksHTML = '';
    if (isFailed && item.checks) {
        const c = item.checks;
        checksHTML = '<div class="proc-check-list">' +
            [['integrity', c.integrity], ['duration', c.duration], ['sample', c.sample]].map(function(pair) {
                const v = pair[1]; if (!v) return '';
                const cls = ['pass', 'fail', 'warn'].includes(v) ? v : 'skip';
                return '<span class="proc-check proc-check-' + cls + '">' + pair[0] + ': ' + v + '</span>';
            }).join('') + '</div>';
    }

    const cardClass = 'download-item' + (isFailed ? ' pl-card-failed' : isDone ? ' pl-card-done' : '');
    const yearSpan = year ? '<span class="pl-year">' + esc(year) + '</span>' : '';

    return '<div class="' + cardClass + '" data-key="' + esc(item.key) + '" data-lane="' + esc(item.lane) + '">' +
        '<div class="download-header">' +
        '<div class="download-name" onclick="this.classList.toggle(\'expanded\')" title="' + safeTitle + '">' + esc(title) + yearSpan + '</div>' +
        '<div class="download-actions">' + badge + missingSubsBadge + actionBtns + '</div>' +
        '</div>' +
        '<div class="download-bar-bg"><div class="download-bar ' + barClass + '" style="width:' + pct + '%"></div></div>' +
        '<div class="download-meta"><span>' + metaLeft + '</span><span>' + speedText + '</span></div>' +
        checksHTML +
        '</div>';
}

function dismissJobFromBtn(btn) { dismissJob(btn.dataset.jobId); }
async function dismissJob(id) {
    try {
        await fetch('/api/pelicula/pipeline/dismiss', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({job_id: id})
        });
        setTimeout(checkPipeline, 300);
    } catch (e) { console.warn('[pelicula] dismiss error:', e); }
}

let lastRefreshAt = 0;

async function refresh() {
    console.log('[pelicula] refresh start');
    const results = await Promise.allSettled([
        checkServices(), checkVPN(), checkPipeline(), checkStatus(),
        checkNotifications(), checkStorage(), loadSessions(), checkHost()
    ]);
    const failed = results.filter(r => r.status === 'rejected').length;
    console.log('[pelicula] refresh done' + (failed ? ' (' + failed + ' failed)' : ''));
    lastRefreshAt = Date.now();
    updateTimestamp();
    updateStaleBanner();
}

function updateStaleBanner() {
    if (!lastRefreshAt) return;
    const age = Date.now() - lastRefreshAt;
    const stale = age > 30000;
    document.body.classList.toggle('stale', stale);
    const el = document.getElementById('footer-update');
    if (el && stale && !el.querySelector('a')) {
        el.textContent = 'stale \u2014 last updated ' + Math.round(age / 1000) + 's ago \u00b7 ';
    } else if (el && !stale && el.textContent.startsWith('stale')) {
        el.textContent = '';
    }
}

// ── Storage Explorer ──────────────────────────────────────────────────────────

function openStorageExplorer() {
    if (window.switchTab) switchTab('storage');
    const section = document.getElementById('storage-explorer-section');
    if (section) {
        section.classList.remove('hidden');
        section.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
    if (!window._seLoaded) {
        window._seLoaded = true;
        const s = document.createElement('script');
        s.src = '/import.js';
        document.head.appendChild(s);
    }
}

function closeStorageExplorer() {
    const section = document.getElementById('storage-explorer-section');
    if (section) section.classList.add('hidden');
}

async function checkVPNStatus() {
    try {
        const res = await tfetch('/api/pelicula/status');
        if (!res.ok) return;
        const data = await res.json();
        if (data.vpn_configured === false) {
            const banner = document.createElement('div');
            banner.className = 'vpn-banner';
            banner.innerHTML = '⚡ VPN not configured — downloading is disabled. <a href="/settings">Set up VPN →</a>';
            const main = document.querySelector('.main-content') || document.body;
            main.prepend(banner);
        }
    } catch(e) { /* non-critical */ }
}

checkAuth();
checkVPNStatus();
if (window.location.hash === '#storage-explorer') {
    setTimeout(openStorageExplorer, 0);
}
setTimeout(refresh, 500);
setTimeout(loadStorageSettings, 600);
// Update check runs once on load — backend caches for 24h so no need to poll.
setTimeout(checkUpdates, 1000);
// Services auto-refresh starts after the first refresh fires.
setTimeout(startServicesAutoRefresh, 1000);
setInterval(refresh, 15000);
setInterval(updateStaleBanner, 5000);
// Pipeline polls faster than the main cycle so cards update as items progress.
setInterval(function() { if (!document.hidden) checkPipeline(); }, 3000);

// ── Users ─────────────────────────────────
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
        if (countEl) countEl.textContent = ` (${users.length})`;
        if (metricEl) metricEl.textContent = users.length;
        list.innerHTML = users.map(u => {
            const lastSeen = u.lastLoginDate
                ? new Date(u.lastLoginDate).toLocaleDateString()
                : 'never';
            const adminBadge = u.isAdmin ? '<span class="user-admin-badge">admin</span>' : '';
            return `<li data-user-id="${escapeHtml(u.id)}" data-user-name="${escapeHtml(u.name)}">` +
                `<div class="user-info"><span class="user-name">${escapeHtml(u.name)}</span>${adminBadge}<span class="user-meta">last login: ${lastSeen}</span></div>` +
                `<div class="user-actions">` +
                `<button class="user-action-btn" onclick="startResetPassword(this)" title="Reset password">Reset</button>` +
                `<button class="user-action-btn user-action-delete" onclick="startDeleteUser(this)" title="Delete user">Delete</button>` +
                `</div>` +
                `<div class="user-reset-form hidden">` +
                `<input type="password" class="user-reset-input" placeholder="New password">` +
                `<button class="user-action-btn" onclick="submitResetPassword(this)">Set</button>` +
                `<button class="user-action-btn" onclick="cancelResetPassword(this)">Cancel</button>` +
                `</div>` +
                `</li>`;
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

async function submitResetPassword(btn) {
    const li = btn.closest('li');
    const id = li.dataset.userId;
    const input = li.querySelector('.user-reset-input');
    const password = input.value;
    if (!password) { input.focus(); return; }
    btn.disabled = true;
    try {
        const resp = await fetch(`/api/pelicula/users/${encodeURIComponent(id)}/password`, {
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
        const resp = await fetch(`/api/pelicula/users/${encodeURIComponent(id)}`, {
            method: 'DELETE',
        });
        if (!resp.ok) {
            const data = await resp.json().catch(() => ({}));
            alert(data.error || `Failed to delete ${name}.`);
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

// ── Sessions / Now Playing ─────────────────
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
            const what = s.nowPlayingType === 'Episode' ? `episode of ${escapeHtml(s.nowPlayingTitle)}` : escapeHtml(s.nowPlayingTitle);
            return `<li class="session-item"><span class="session-user">${escapeHtml(s.userName)}</span>` +
                `<span class="session-sep">·</span><span class="session-title">${what}</span>` +
                `<span class="session-sep">·</span><span class="session-device">${escapeHtml(s.client || s.deviceName)}</span></li>`;
        }).join('');
    } catch (e) {
        section.classList.add('hidden');
        console.warn('[pelicula] loadSessions error:', e);
    }
}

document.getElementById('add-user-form')?.addEventListener('submit', async (e) => {
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
            successEl.innerHTML = `User <strong>${escapeHtml(createdUsername)}</strong> created. <a href="/jellyfin/" target="_blank" style="color:#7dda93">Open Jellyfin &rarr;</a>`;
            successEl.classList.remove('hidden');
            setTimeout(() => successEl.classList.add('hidden'), 8000);
        }
        loadUsers();
    } catch (e) {
        errEl.textContent = 'Network error.';
        errEl.classList.remove('hidden');
    }
});


function escapeHtml(str) {
    return str.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

// ── Requests ──────────────────────────────
let requestsLoaded = false;
let arrMetaLoaded = false;
let _arrMeta = null;

async function loadRequests() {
    try {
        const resp = await fetch('/api/pelicula/requests');
        if (!resp.ok) return;
        const requests = await resp.json();
        renderRequests(requests || []);
    } catch (e) { console.warn('[pelicula] loadRequests error', e); }
}

function renderRequests(requests) {
    const isAdmin = currentRole === 'admin';
    const username = document.body.dataset.username || '';

    const pendingList = document.getElementById('requests-pending-list');
    const pendingEmpty = document.getElementById('requests-pending-empty');
    const mineList = document.getElementById('requests-mine-list');
    const mineEmpty = document.getElementById('requests-mine-empty');

    const pending = requests.filter(r => r.state === 'pending' && isAdmin);
    const mine = requests.filter(r => r.requested_by === username || (!username && !isAdmin));

    if (pendingList) {
        pendingList.innerHTML = pending.map(r => `
            <li class="request-item" data-id="${escapeHtml(r.id)}">
                ${r.poster ? `<img class="request-poster" src="${escapeHtml(r.poster)}" alt="">` : '<div class="request-poster request-poster-placeholder"></div>'}
                <div class="request-info">
                    <div class="request-title">${escapeHtml(r.title)}${r.year ? ` <span class="request-year">(${r.year})</span>` : ''}</div>
                    <div class="request-meta">${escapeHtml(r.type)} · requested by ${escapeHtml(r.requested_by)}</div>
                </div>
                <div class="request-actions">
                    <button class="request-btn request-btn-approve" onclick="approveRequest('${escapeHtml(r.id)}')">Approve</button>
                    <button class="request-btn request-btn-deny" onclick="denyRequest('${escapeHtml(r.id)}')">Deny</button>
                </div>
            </li>`).join('');
        if (pendingEmpty) pendingEmpty.classList.toggle('hidden', pending.length > 0);
    }

    if (mineList) {
        mineList.innerHTML = mine.map(r => `
            <li class="request-item request-item-${escapeHtml(r.state)}" data-id="${escapeHtml(r.id)}">
                ${r.poster ? `<img class="request-poster" src="${escapeHtml(r.poster)}" alt="">` : '<div class="request-poster request-poster-placeholder"></div>'}
                <div class="request-info">
                    <div class="request-title">${escapeHtml(r.title)}${r.year ? ` <span class="request-year">(${r.year})</span>` : ''}</div>
                    <div class="request-meta">${escapeHtml(r.type)}</div>
                    ${r.reason ? `<div class="request-reason">${escapeHtml(r.reason)}</div>` : ''}
                </div>
                <span class="request-state request-state-${escapeHtml(r.state)}">${escapeHtml(r.state)}</span>
            </li>`).join('');
        if (mineEmpty) mineEmpty.classList.toggle('hidden', mine.length > 0);
    }
}

async function approveRequest(id) {
    const btn = document.querySelector(`.request-item[data-id="${id}"] .request-btn-approve`);
    if (btn) { btn.disabled = true; btn.textContent = 'Approving…'; }
    try {
        const resp = await fetch(`/api/pelicula/requests/${id}/approve`, {method: 'POST'});
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
        const resp = await fetch(`/api/pelicula/requests/${id}/deny`, {
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

async function submitRequest(type, tmdbId, tvdbId, title, year, poster) {
    try {
        const body = {type, title, year, poster};
        if (type === 'movie') body.tmdb_id = tmdbId;
        else body.tvdb_id = tvdbId;
        const resp = await fetch('/api/pelicula/requests', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(body)
        });
        const data = await resp.json();
        if (!resp.ok) {
            alert('Request failed: ' + (data.error || resp.status));
            return;
        }
        requestsLoaded = false; // force reload
        await loadRequests();
        const requestsSection = document.getElementById('requests-section');
        if (requestsSection) requestsSection.scrollIntoView({behavior: 'smooth'});
    } catch (e) { alert('Network error'); }
}

// arr-meta for admin request settings dropdowns
async function loadArrMeta() {
    try {
        const resp = await fetch('/api/pelicula/arr-meta');
        if (!resp.ok) return;
        _arrMeta = await resp.json();
        populateRequestsSettings(_arrMeta);
    } catch (e) { console.warn('[pelicula] loadArrMeta error', e); }
}

function toggleRequestsSettings() {
    const panel = document.getElementById('requests-settings-panel');
    if (panel) panel.classList.toggle('hidden');
}

function populateRequestsSettings(meta) {
    const fillProfiles = (selectId, profiles) => {
        const el = document.getElementById(selectId);
        if (!el || !profiles) return;
        el.innerHTML = '<option value="">— use default —</option>' +
            profiles.map(p => `<option value="${p.id}">${escapeHtml(p.name)}</option>`).join('');
    };
    const fillRoots = (selectId, roots) => {
        const el = document.getElementById(selectId);
        if (!el || !roots) return;
        el.innerHTML = '<option value="">— use default —</option>' +
            roots.map(r => `<option value="${escapeHtml(r.path)}">${escapeHtml(r.path)}</option>`).join('');
    };
    fillProfiles('req-radarr-profile', meta?.radarr?.qualityProfiles);
    fillRoots('req-radarr-root', meta?.radarr?.rootFolders);
    fillProfiles('req-sonarr-profile', meta?.sonarr?.qualityProfiles);
    fillRoots('req-sonarr-root', meta?.sonarr?.rootFolders);
}

async function saveRequestsSettings() {
    const profileEl = id => document.getElementById(id);
    const body = {};
    const radarrProfile = profileEl('req-radarr-profile')?.value;
    const radarrRoot = profileEl('req-radarr-root')?.value;
    const sonarrProfile = profileEl('req-sonarr-profile')?.value;
    const sonarrRoot = profileEl('req-sonarr-root')?.value;
    if (radarrProfile) body.requests_radarr_profile_id = radarrProfile;
    if (radarrRoot) body.requests_radarr_root = radarrRoot;
    if (sonarrProfile) body.requests_sonarr_profile_id = sonarrProfile;
    if (sonarrRoot) body.requests_sonarr_root = sonarrRoot;
    try {
        const resp = await fetch('/api/pelicula/settings', {
            method: 'POST',
            headers: {'Content-Type': 'application/json', 'Origin': window.location.origin},
            body: JSON.stringify(body)
        });
        const data = await resp.json();
        if (!resp.ok) { alert('Save failed: ' + (data.error || resp.status)); return; }
        const btn = document.getElementById('requests-settings-save-btn');
        if (btn) { const prev = btn.textContent; btn.textContent = 'Saved!'; setTimeout(() => btn.textContent = prev, 2000); }
    } catch (e) { alert('Network error'); }
}

// ── Invites ────────────────────────────────
async function loadInvites() {
    const list = document.getElementById('invites-list');
    if (!list) return;
    try {
        const resp = await fetch('/api/pelicula/invites');
        if (!resp.ok) { list.innerHTML = ''; return; }
        const invites = await resp.json();
        const invMetric = document.getElementById('um-metric-invites');
        if (!invites || invites.length === 0) {
            list.innerHTML = '<li class="invite-empty">No invite links yet.</li>';
            if (invMetric) invMetric.textContent = '0';
            return;
        }
        if (invMetric) invMetric.textContent = invites.filter(i => i.state === 'active').length;
        list.innerHTML = invites.map(inv => {
            const stateClass = {active:'invite-active', expired:'invite-dead', exhausted:'invite-dead', revoked:'invite-dead'}[inv.state] || 'invite-dead';
            const stateLabel = {active:'active', expired:'expired', exhausted:'used up', revoked:'revoked'}[inv.state] || inv.state;
            const label = inv.label ? escapeHtml(inv.label) : '—';
            const uses = inv.max_uses != null ? `${inv.uses}/${inv.max_uses}` : `${inv.uses}/∞`;
            const expiry = inv.expires_at ? `expires ${new Date(inv.expires_at).toLocaleDateString()}` : 'no expiry';
            const link = `${window.location.origin}/register?t=${encodeURIComponent(inv.token)}`;
            const isActive = inv.state === 'active';
            return `<li class="invite-item" data-token="${escapeHtml(inv.token)}">` +
                `<div class="invite-row">` +
                `<span class="invite-badge ${stateClass}">${stateLabel}</span>` +
                `<span class="invite-meta">${uses} use${inv.uses !== 1 ? 's' : ''} · ${expiry}</span>` +
                (inv.label ? `<span class="invite-label-text">${label}</span>` : '') +
                `</div>` +
                `<div class="invite-actions">` +
                (isActive ? `<button class="user-action-btn" onclick="copyInviteItemLink(this, '${escapeHtml(link)}')" title="Copy invite link">Copy link</button>` : '') +
                (isActive ? `<button class="user-action-btn" onclick="revokeInvite(this)" title="Deactivate this invite">Revoke</button>` : '') +
                `<button class="user-action-btn user-action-delete" onclick="deleteInvite(this)" title="Delete record">Delete</button>` +
                `</div>` +
                `</li>`;
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
        const resp = await fetch(`/api/pelicula/invites/${encodeURIComponent(token)}/revoke`, { method: 'POST' });
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
        const resp = await fetch(`/api/pelicula/invites/${encodeURIComponent(token)}`, { method: 'DELETE' });
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

// ── Invite modal ────────────────────────────
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
    btn.textContent = 'Creating…';
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
    const link = `${window.location.origin}/register?t=${encodeURIComponent(invite.token)}`;
    document.getElementById('invite-link-val').value = link;
    document.getElementById('invite-step-create').style.display = 'none';
    document.getElementById('invite-step-share').style.display = '';
    document.getElementById('invite-modal-title').textContent = 'Share invite link';

    // QR code
    if (typeof qrSVG === 'function') {
        const svg = qrSVG(link, 4);
        if (svg) {
            document.getElementById('invite-qr-svg').innerHTML = svg;
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
