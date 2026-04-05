// ── Auth ──────────────────────────────────
let currentRole = 'admin'; // default when auth is off

async function checkAuth() {
    try {
        const res = await fetch('/api/pelicula/auth/check');
        const data = await res.json();
        if (!data.auth) {
            // Auth is off — no login needed, full access
            applyRole('admin');
            return;
        }
        if (!data.valid) {
            // Show username field only in users mode
            if (data.mode === 'users') {
                document.getElementById('login-username').classList.remove('hidden');
            }
            document.getElementById('login-overlay').classList.remove('hidden');
        } else {
            applyRole(data.role || 'admin');
        }
    } catch {}
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
            applyRole(data.role || 'admin');
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
function applyRole(role) {
    currentRole = role;
    const isManager = role === 'manager' || role === 'admin';
    const isAdmin = role === 'admin';

    // Search section
    const searchSection = document.querySelector('.search-section');
    if (searchSection) searchSection.style.display = isManager ? '' : 'none';

    // Download action buttons (rendered dynamically — use a data attribute approach)
    // Store role for use in renderDownloads
    document.body.dataset.role = role;
}

document.getElementById('login-username').addEventListener('keydown', e => { if (e.key === 'Enter') document.getElementById('login-password').focus(); });
document.getElementById('login-password').addEventListener('keydown', e => { if (e.key === 'Enter') doLogin(); });

// ── Status + Indexer check ────────────────
async function checkStatus() {
    try {
        const res = await fetch('/api/pelicula/status', {});
        if (!res.ok) return;
        const data = await res.json();
        const toast = document.getElementById('toast');
        if (data.indexers === 0) {
            toast.classList.add('visible');
        } else {
            toast.classList.remove('visible');
        }
    } catch {}
}

// ── Search ────────────────────────────────
let searchTimeout;
let searchType = '';
let lastResults = [];
const searchInput = document.getElementById('search-input');
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
    try {
        const typeParam = searchType ? '&type=' + searchType : '';
        const res = await fetch('/api/pelicula/search?q=' + encodeURIComponent(q) + typeParam);
        const data = await res.json();
        lastResults = data.results || [];
        renderResults(lastResults, false);
    } catch {
        searchResults.innerHTML = '<div class="no-items">Search unavailable</div>';
        searchResults.className = 'search-results visible';
    }
}
function renderResultCard(r) {
    const poster = r.poster ? `<img src="${r.poster}" alt="">` : '<div class="no-poster"></div>';
    const badge = r.type === 'movie' ? 'Movie' : 'Series';
    const id = r.type === 'movie' ? r.tmdbId : r.tvdbId;
    const btnClass = r.added ? 'search-add added' : 'search-add';
    const btnText = r.added ? 'Added' : 'Add';
    const disabled = r.added ? 'disabled' : '';
    return `<div class="search-result">${poster}<div class="search-info"><div class="search-title">${esc(r.title)}</div><div class="search-meta">${r.year || ''} &middot; ${badge}</div><div class="search-overview">${esc(r.overview || '')}</div></div><button class="${btnClass}" ${disabled} onclick="addMedia('${r.type}', ${id}, this)">${btnText}</button></div>`;
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
    btn.disabled = true; btn.textContent = '...';
    try {
        const body = type === 'movie' ? {type, tmdbId: id} : {type, tvdbId: id};
        const res = await fetch('/api/pelicula/search/add', { method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body) });
        if (res.ok) { btn.textContent = 'Added'; btn.classList.add('added'); }
        else { btn.textContent = 'Error'; setTimeout(() => { btn.textContent = 'Add'; btn.disabled = false; }, 2000); }
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
        const res = await fetch('/api/pelicula/downloads', {});
        if (!res.ok) throw new Error();
        const data = await res.json();
        renderDownloads(data);
        // Update telemetry speed
        var s = data.stats || {};
        document.getElementById('t-dl').textContent = formatSpeed(s.dlspeed || 0);
        document.getElementById('t-dl').classList.remove('loading');
        document.getElementById('t-ul').textContent = formatSpeed(s.upspeed || 0);
        document.getElementById('t-ul').classList.remove('loading');
    } catch {}
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
        const barClass = isPaused ? 'paused' : isSeeding ? 'seeding' : 'active';
        const pauseBtn = !canPause ? '' : isPaused
            ? `<button class="dl-btn resume" title="Resume" onclick="dlPause('${t.hash}',false)">&#9654;</button>`
            : `<button class="dl-btn pause" title="Pause" onclick="dlPause('${t.hash}',true)">&#9646;&#9646;</button>`;
        const cancelBtn = canCancel ? `<button class="dl-btn cancel" title="Cancel download" onclick="dlCancel('${t.hash}','${t.category}','${esc(t.name)}',false)">&#10005;</button>` : '';
        const blocklistBtn = canCancel ? `<button class="dl-btn blocklist" title="Remove &amp; blocklist" onclick="openBlocklistModal('${t.hash}','${t.category}','${esc(t.name)}')">&#8856;</button>` : '';
        const statusText = isPaused ? '<span class="paused-label">paused</span>' : `${speed}${eta ? ' &middot; ' + eta : ''}`;
        return `<div class="download-item"><div class="download-header"><div class="download-name">${esc(t.name)}</div><div class="download-actions">${pauseBtn}${cancelBtn}${blocklistBtn}</div></div><div class="download-bar-bg"><div class="download-bar ${barClass}" style="width:${pct}%"></div></div><div class="download-meta"><span>${pct}% of ${formatSize(t.size)}</span><span>${statusText}</span></div></div>`;
    }).join('');
}

// Download actions
async function dlPause(hash, paused) {
    try {
        await fetch('/api/pelicula/downloads/pause', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({hash, paused})
        });
        setTimeout(checkDownloads, 500);
    } catch {}
}
async function dlCancel(hash, category, name, blocklist, reason) {
    if (!blocklist && !confirm('Cancel download and unmonitor?\n\n' + name)) return;
    try {
        await fetch('/api/pelicula/downloads/cancel', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({hash, category, blocklist, reason: reason || ''})
        });
        setTimeout(checkDownloads, 500);
    } catch {}
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
    const reason = document.getElementById('blocklist-reason').value;
    closeBlocklistModal();
    dlCancel(blocklistState.hash, blocklistState.category, blocklistState.name, true, reason);
}
function formatSpeed(bps) { if (bps > 1048576) return (bps/1048576).toFixed(1)+' MB/s'; if (bps > 1024) return (bps/1024).toFixed(0)+' KB/s'; if (bps > 0) return bps+' B/s'; return 'idle'; }
function formatSize(b) { if (b > 1073741824) return (b/1073741824).toFixed(1)+' GB'; if (b > 1048576) return (b/1048576).toFixed(0)+' MB'; return (b/1024).toFixed(0)+' KB'; }
function formatETA(s) { if (s > 86400) return Math.floor(s/86400)+'d'; if (s > 3600) return Math.floor(s/3600)+'h '+Math.floor((s%3600)/60)+'m'; if (s > 60) return Math.floor(s/60)+'m'; return s+'s'; }

// ── Services ──────────────────────────────
async function checkServices() {
    const warn = document.getElementById('search-warning');
    try {
        const res = await fetch('/api/pelicula/status', {});
        if (!res.ok) throw new Error();
        const data = await res.json();
        const svcMap = data.services || {};
        document.querySelectorAll('.service').forEach(el => {
            const dot = el.querySelector('.status-dot');
            const href = el.getAttribute('href');
            let name = '';
            if (href.includes('sonarr')) name = 'sonarr';
            else if (href.includes('radarr')) name = 'radarr';
            else if (href.includes('prowlarr')) name = 'prowlarr';
            else if (href.includes('qbt')) name = 'qbittorrent';
            else if (href.includes('jellyfin')) name = 'jellyfin';
            else if (href.includes('procula')) name = 'procula';
            dot.className = 'status-dot ' + (svcMap[name] === 'up' ? 'up' : 'down');
        });
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
            searchInput.placeholder = 'Search movies & TV shows...';
            const down = !radarrUp ? 'Radarr (movies)' : 'Sonarr (TV shows)';
            warn.textContent = down + ' is down — some results may be missing';
            warn.className = 'search-warning warn';
        } else {
            searchInput.disabled = false;
            searchInput.placeholder = 'Search movies & TV shows...';
            warn.className = 'search-warning';
        }
    } catch {
        document.querySelectorAll('.status-dot').forEach(d => d.className = 'status-dot down');
        searchInput.disabled = true;
        searchInput.placeholder = 'Search unavailable';
        warn.textContent = 'Cannot reach services — search is disabled';
        warn.className = 'search-warning err';
    }
}

// ── VPN Telemetry ─────────────────────────
async function checkVPN() {
    const vpnEl = document.getElementById('t-vpn');
    const regionEl = document.getElementById('t-region');
    const portEl = document.getElementById('t-port');
    try {
        const [ipRes, portRes] = await Promise.all([
            fetch('/api/vpn/v1/publicip/ip', {}),
            fetch('/api/vpn/v1/portforward', {})
        ]);
        if (ipRes.ok) {
            const data = await ipRes.json();
            vpnEl.setAttribute('data-ip', data.public_ip || '?');
            vpnEl.textContent = '***.***';
            vpnEl.className = 'telem-value vpn-ok';
            regionEl.textContent = data.country || '?';
            regionEl.classList.remove('loading');
        }
        if (portRes.ok) {
            const pd = await portRes.json();
            portEl.textContent = pd.port || '?';
            portEl.classList.remove('loading');
        }
    } catch {
        vpnEl.textContent = 'DOWN';
        vpnEl.className = 'telem-value vpn-err';
        regionEl.textContent = '-'; regionEl.classList.remove('loading');
        portEl.textContent = '-'; portEl.classList.remove('loading');
    }
}

function updateTimestamp() { document.getElementById('footer').textContent = new Date().toLocaleTimeString(); }
async function refresh() {
    console.log('[pelicula] refresh start');
    try {
        await Promise.all([checkServices(), checkVPN(), checkDownloads(), checkStatus()]);
        console.log('[pelicula] refresh done');
    } catch(e) {
        console.error('[pelicula] refresh error:', e);
    }
    updateTimestamp();
}
document.getElementById('t-vpn').addEventListener('click', function() {
    const ip = this.getAttribute('data-ip');
    if (!ip || ip === '?') return;
    this.textContent = this.textContent === ip ? '***.***' : ip;
});
checkAuth();
setTimeout(refresh, 500);
setInterval(refresh, 15000);
