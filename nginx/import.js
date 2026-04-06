// ── Import wizard state ─────────────────────────────────────────────────────

const state = {
    step: 'browse',
    selected: [],        // [{path, name, size}]
    scanResults: [],     // from /library/scan
    dismissed: new Set(),
};

// ── Helpers ─────────────────────────────────────────────────────────────────

function esc(s) {
    const d = document.createElement('div');
    d.textContent = s;
    return d.innerHTML;
}

function escAttr(s) {
    return String(s).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

async function apiFetch(url, opts) {
    const res = await fetch(url, opts);
    if (res.status === 401) {
        window.location.href = '/?login=1';
        throw new Error('Session expired');
    }
    return res;
}

function formatSize(bytes) {
    if (!bytes) return '';
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB';
    if (bytes < 1073741824) return (bytes / 1048576).toFixed(1) + ' MB';
    return (bytes / 1073741824).toFixed(2) + ' GB';
}

// ── Step navigation ─────────────────────────────────────────────────────────

function goToStep(step) {
    state.step = step;
    const steps = ['browse', 'match', 'configure', 'apply'];
    const idx = steps.indexOf(step);

    document.querySelectorAll('.import-panel').forEach(p => p.classList.add('hidden'));
    document.getElementById('step-' + step).classList.remove('hidden');

    document.querySelectorAll('.import-step').forEach((el, i) => {
        el.classList.remove('active', 'done');
        if (i < idx) el.classList.add('done');
        if (i === idx) el.classList.add('active');
    });
}

// ── Step 1: Browse ──────────────────────────────────────────────────────────

async function loadBrowseRoots() {
    try {
        const res = await apiFetch('/api/pelicula/browse');
        if (!res.ok) throw new Error('Failed to load directories');
        const data = await res.json();
        renderRoots(data.entries || []);
    } catch (e) {
        document.getElementById('browse-tree').innerHTML =
            '<div class="no-items">Failed to load directories: ' + esc(e.message) + '</div>';
    }
}

function renderRoots(entries) {
    const tree = document.getElementById('browse-tree');
    if (!entries.length) {
        tree.innerHTML = '<div class="no-items">No browsable directories found</div>';
        return;
    }
    tree.innerHTML = '';
    entries.forEach(e => tree.appendChild(createBrowseEntry(e, 0)));
}

function createBrowseEntry(entry, depth) {
    const row = document.createElement('div');
    row.className = 'browse-entry';
    row.style.paddingLeft = (0.75 + depth * 1.25) + 'rem';

    if (entry.isDir) {
        const expand = document.createElement('span');
        expand.className = 'browse-expand';
        expand.textContent = '\u25B6';
        row.appendChild(expand);

        const icon = document.createElement('span');
        icon.className = 'browse-icon dir';
        icon.textContent = '\uD83D\uDCC1';
        row.appendChild(icon);

        const name = document.createElement('span');
        name.className = 'browse-name';
        name.textContent = entry.name;
        row.appendChild(name);

        // Children container
        const children = document.createElement('div');
        children.className = 'browse-children';
        children.dataset.path = entry.path;
        children.dataset.loaded = 'false';

        row.addEventListener('click', (e) => {
            if (e.target.classList.contains('browse-checkbox')) return;
            toggleDir(expand, children, entry.path);
        });

        const wrapper = document.createElement('div');
        wrapper.appendChild(row);
        wrapper.appendChild(children);
        return wrapper;
    }

    // File entry
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.className = 'browse-checkbox';
    cb.addEventListener('change', () => toggleFileSelection(entry, cb.checked));
    row.appendChild(cb);

    const spacer = document.createElement('span');
    spacer.className = 'browse-expand';
    spacer.style.visibility = 'hidden';
    spacer.textContent = '\u25B6';
    row.appendChild(spacer);

    const icon = document.createElement('span');
    icon.className = 'browse-icon file';
    icon.textContent = '\uD83C\uDFA5';
    row.appendChild(icon);

    const name = document.createElement('span');
    name.className = 'browse-name';
    name.textContent = entry.name;
    row.appendChild(name);

    const size = document.createElement('span');
    size.className = 'browse-size';
    size.textContent = formatSize(entry.size);
    row.appendChild(size);

    return row;
}

async function toggleDir(expandEl, childrenEl, path) {
    const isOpen = childrenEl.classList.contains('open');
    if (isOpen) {
        childrenEl.classList.remove('open');
        expandEl.classList.remove('open');
        return;
    }

    expandEl.classList.add('open');
    childrenEl.classList.add('open');

    if (childrenEl.dataset.loaded === 'true') return;

    childrenEl.innerHTML = '<div class="browse-loading">Loading...</div>';
    try {
        const res = await apiFetch('/api/pelicula/browse?path=' + encodeURIComponent(path));
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const data = await res.json();
        childrenEl.innerHTML = '';
        const entries = data.entries || [];
        if (!entries.length) {
            childrenEl.innerHTML = '<div class="browse-loading">Empty directory</div>';
        } else {
            let depth = 0;
            let el = childrenEl;
            while ((el = el.parentElement)) {
                if (el.classList.contains('browse-children')) depth++;
            }
            entries.forEach(e => childrenEl.appendChild(createBrowseEntry(e, depth)));
            if (data.truncated) {
                const note = document.createElement('div');
                note.className = 'browse-truncated';
                note.textContent = 'Directory listing truncated (500 max)';
                childrenEl.appendChild(note);
            }
        }
        childrenEl.dataset.loaded = 'true';
    } catch (e) {
        childrenEl.innerHTML = '<div class="browse-loading">Error: ' + esc(e.message) + '</div>';
    }
}

function toggleFileSelection(entry, checked) {
    if (checked) {
        if (!state.selected.find(s => s.path === entry.path)) {
            state.selected.push({ path: entry.path, name: entry.name, size: entry.size });
        }
    } else {
        state.selected = state.selected.filter(s => s.path !== entry.path);
    }
    updateSelectedCount();
}

function updateSelectedCount() {
    const count = state.selected.length;
    const el = document.getElementById('selected-count');
    el.textContent = count ? count + ' file' + (count > 1 ? 's' : '') + ' selected' : '';
    document.getElementById('btn-scan').disabled = count === 0;
}

// ── Step 2: Scan / Match ────────────────────────────────────────────────────

async function doScan() {
    goToStep('match');
    const results = document.getElementById('match-results');
    results.innerHTML = '<div class="apply-progress"><div class="apply-spinner"></div><span>Scanning files...</span></div>';

    try {
        const files = state.selected.map(f => ({ path: f.path, size: f.size }));
        const res = await apiFetch('/api/pelicula/library/scan', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ files }),
        });
        if (!res.ok) {
            const err = await res.json().catch(() => ({ error: 'HTTP ' + res.status }));
            throw new Error(err.error || 'Scan failed');
        }
        state.scanResults = await res.json();
        state.dismissed = new Set();
        renderMatchResults();
    } catch (e) {
        results.innerHTML = '<div class="no-items">Scan failed: ' + esc(e.message) + '</div>';
    }
}

function renderMatchResults() {
    const container = document.getElementById('match-results');
    container.innerHTML = '';

    const groups = { new: [], exists: [], unmatched: [] };
    state.scanResults.forEach((item, i) => {
        const status = item.status || 'unmatched';
        if (groups[status]) groups[status].push({ ...item, idx: i });
        else groups.unmatched.push({ ...item, idx: i });
    });

    let newCount = groups.new.length;
    let existsCount = groups.exists.length;
    let unmatchedCount = groups.unmatched.length;

    document.getElementById('match-stats').textContent =
        newCount + ' new, ' + existsCount + ' existing, ' + unmatchedCount + ' unmatched';

    if (groups.new.length) {
        container.appendChild(groupHeader('New (' + groups.new.length + ')'));
        groups.new.forEach(item => container.appendChild(createMatchItem(item)));
    }
    if (groups.exists.length) {
        container.appendChild(groupHeader('Already in library (' + groups.exists.length + ')'));
        groups.exists.forEach(item => container.appendChild(createMatchItem(item)));
    }
    if (groups.unmatched.length) {
        container.appendChild(groupHeader('Unmatched (' + groups.unmatched.length + ')'));
        groups.unmatched.forEach(item => container.appendChild(createMatchItem(item)));
    }

    const activeNewCount = groups.new.filter(item => !state.dismissed.has(item.idx)).length;
    document.getElementById('btn-configure').disabled = activeNewCount === 0;
}

function groupHeader(text) {
    const el = document.createElement('div');
    el.className = 'match-group-header';
    el.textContent = text;
    return el;
}

function createMatchItem(item) {
    const row = document.createElement('div');
    row.className = 'match-item';
    row.id = 'match-' + item.idx;
    if (state.dismissed.has(item.idx)) row.classList.add('dismissed');

    const info = document.createElement('div');
    info.className = 'match-info';

    if (item.match) {
        info.innerHTML =
            '<div class="match-title">' + esc(item.match.title) +
            (item.match.year ? ' <span style="color:#666">(' + item.match.year + ')</span>' : '') +
            '</div>' +
            '<div class="match-meta">' + esc(item.match.type) + '</div>' +
            '<div class="match-file" title="' + escAttr(item.file) + '">' + esc(item.file) + '</div>';
    } else {
        info.innerHTML =
            '<div class="match-title" style="color:#666">' + esc(item.file.split('/').pop()) + '</div>' +
            '<div class="match-file" title="' + escAttr(item.file) + '">' + esc(item.file) + '</div>';
    }
    row.appendChild(info);

    // Size
    if (item.size) {
        const size = document.createElement('span');
        size.className = 'browse-size';
        size.textContent = formatSize(item.size);
        row.appendChild(size);
    }

    // Confidence badge
    if (item.match) {
        const badge = document.createElement('span');
        badge.className = 'match-badge badge-' + item.match.confidence;
        badge.textContent = item.match.confidence;
        row.appendChild(badge);
    }

    // Status badge
    const statusBadge = document.createElement('span');
    statusBadge.className = 'match-badge badge-' + item.status;
    statusBadge.textContent = item.status;
    row.appendChild(statusBadge);

    // Dismiss button (only for "new" items)
    if (item.status === 'new') {
        const dismiss = document.createElement('button');
        dismiss.className = 'match-dismiss';
        dismiss.textContent = state.dismissed.has(item.idx) ? 'Restore' : 'Dismiss';
        dismiss.addEventListener('click', () => {
            if (state.dismissed.has(item.idx)) {
                state.dismissed.delete(item.idx);
            } else {
                state.dismissed.add(item.idx);
            }
            renderMatchResults();
        });
        row.appendChild(dismiss);
    }

    return row;
}

// ── Step 4: Apply ───────────────────────────────────────────────────────────

async function doApply() {
    goToStep('apply');
    const content = document.getElementById('apply-content');
    content.innerHTML = '<div class="apply-progress"><div class="apply-spinner"></div><span>Applying import...</span></div>';

    const strategy = document.querySelector('input[name="strategy"]:checked').value;
    const validate = document.getElementById('validate-toggle').checked;

    const items = state.scanResults
        .filter((r, i) => r.status === 'new' && r.match && !state.dismissed.has(i))
        .map(r => ({
            type: r.match.type === 'series' ? 'series' : 'movie',
            tmdbId: r.match.tmdbId || 0,
            tvdbId: r.match.tvdbId || 0,
            title: r.match.title,
            year: r.match.year || 0,
            rootFolderPath: r.match.type === 'movie' ? '/movies' : '/tv',
            monitored: strategy !== 'keep',
            sourcePath: r.file,
        }));

    if (!items.length) {
        content.innerHTML = '<div class="no-items">No items to import</div>';
        document.getElementById('apply-nav').classList.remove('hidden');
        return;
    }

    try {
        const res = await apiFetch('/api/pelicula/library/apply', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ items, strategy, validate }),
        });
        if (!res.ok) {
            const err = await res.json().catch(() => ({ error: 'HTTP ' + res.status }));
            throw new Error(err.error || 'Apply failed');
        }
        const result = await res.json();
        renderApplyResult(result, validate);
    } catch (e) {
        content.innerHTML = '<div class="no-items">Import failed: ' + esc(e.message) + '</div>';
    }

    document.getElementById('apply-nav').classList.remove('hidden');
}

function renderApplyResult(result, validate) {
    const content = document.getElementById('apply-content');
    let html = '<div class="apply-result">';
    html += '<div class="apply-stat"><span class="apply-stat-label">Added</span><span class="apply-stat-value added">' + (result.added || 0) + '</span></div>';
    html += '<div class="apply-stat"><span class="apply-stat-label">Skipped</span><span class="apply-stat-value skipped">' + (result.skipped || 0) + '</span></div>';
    html += '<div class="apply-stat"><span class="apply-stat-label">Failed</span><span class="apply-stat-value failed">' + (result.failed || 0) + '</span></div>';
    html += '</div>';

    if (result.errors && result.errors.length) {
        html += '<div class="apply-errors">';
        result.errors.forEach(e => {
            html += '<div class="apply-error-item">' + esc(e) + '</div>';
        });
        html += '</div>';
    }

    if (result.added > 0) {
        if (validate) {
            html += '<div class="apply-success">Import complete. Files queued for Procula validation &mdash; check the <a href="/" style="color:#7dda93">dashboard</a> Processing section for progress.</div>';
        } else {
            html += '<div class="apply-success">Import complete. Items registered in your library.</div>';
        }
    }

    content.innerHTML = html;
}

// ── Init ────────────────────────────────────────────────────────────────────

loadBrowseRoots();
