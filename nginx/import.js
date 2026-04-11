// ── Media browser state ──────────────────────────────────────────────────────

const state = {
    selected: [],           // [{path, name, size, isDir}]
    scanResults: [],        // from /library/scan
    dismissed: new Set(),
    groupSelections: {},    // groupKey → chosen file path (for dup groups)
};

// ── Helpers ──────────────────────────────────────────────────────────────────

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
    const u = ['B','KB','MB','GB','TB'];
    let i = 0, n = bytes;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return n.toFixed(1) + ' ' + u[i];
}

// ── Import modal navigation ──────────────────────────────────────────────────

function importGoToStep(step) {
    const steps = ['match', 'configure', 'apply'];
    const idx = steps.indexOf(step);

    document.querySelectorAll('#import-modal .import-panel').forEach(p => p.classList.add('hidden'));
    document.getElementById('step-' + step).classList.remove('hidden');

    document.querySelectorAll('#import-modal .import-step').forEach((el, i) => {
        el.classList.remove('active', 'done');
        if (i < idx) el.classList.add('done');
        if (i === idx) el.classList.add('active');
    });

    if (step === 'configure') {
        updateStrategyExamples();
    }
}

// updateStrategyExamples replaces the generic placeholder text in the strategy
// cards with paths from the first matched scan result so the user sees their
// actual files instead of a made-up example.
function updateStrategyExamples() {
    const first = state.scanResults.find(r => r.status === 'new' && r.suggestedPath);
    if (!first) return;
    const src = first.file;
    const dst = first.suggestedPath;
    const fwd = src + ' \u2192 ' + dst;
    const bwd = dst + ' \u2192 ' + src;
    const el = id => document.getElementById(id);
    if (el('strategy-example-hardlink')) el('strategy-example-hardlink').textContent = fwd;
    if (el('strategy-example-migrate'))  el('strategy-example-migrate').textContent  = fwd;
    if (el('strategy-example-symlink'))  el('strategy-example-symlink').textContent  = bwd;
}

// ── Browse tree ──────────────────────────────────────────────────────────────

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
        // Directory checkbox — selecting a folder imports all video files inside it.
        const cb = document.createElement('input');
        cb.type = 'checkbox';
        cb.className = 'browse-checkbox';
        cb.title = 'Select entire folder';
        cb.addEventListener('change', () => toggleFolderSelection(entry, cb.checked));
        row.appendChild(cb);

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

        const hint = document.createElement('span');
        hint.className = 'browse-size';
        hint.textContent = 'folder';
        row.appendChild(hint);

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
    // Disable individual file selection if a parent folder is already selected.
    if (isPathCoveredByFolder(entry.path)) {
        cb.checked = true;
        cb.disabled = true;
        cb.title = 'Covered by parent folder selection';
    }
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

function isPathCoveredByFolder(filePath) {
    return state.selected.some(s => s.isDir && filePath.startsWith(s.path + '/'));
}

function toggleFolderSelection(entry, checked) {
    if (checked) {
        if (!state.selected.find(s => s.path === entry.path)) {
            state.selected.push({ path: entry.path, name: entry.name, isDir: true });
        }
    } else {
        state.selected = state.selected.filter(s => s.path !== entry.path);
    }
    updateActionBar();
}

function toggleFileSelection(entry, checked) {
    if (isPathCoveredByFolder(entry.path)) return; // parent folder takes precedence
    if (checked) {
        if (!state.selected.find(s => s.path === entry.path)) {
            state.selected.push({ path: entry.path, name: entry.name, size: entry.size });
        }
    } else {
        state.selected = state.selected.filter(s => s.path !== entry.path);
    }
    updateActionBar();
}

// ── Action bar ───────────────────────────────────────────────────────────────

function updateActionBar() {
    const n = state.selected.length;
    const bar = document.getElementById('action-bar');

    if (n === 0) {
        bar.classList.add('hidden');
        // Also keep the old #selected-count in sync for any code still reading it
        const sc = document.getElementById('selected-count');
        if (sc) sc.textContent = '';
        return;
    }
    bar.classList.remove('hidden');

    // Selection count label
    const folders = state.selected.filter(s => s.isDir).length;
    const files = state.selected.filter(s => !s.isDir).length;
    const parts = [];
    if (folders) parts.push(folders + ' folder' + (folders > 1 ? 's' : ''));
    if (files) parts.push(files + ' file' + (files > 1 ? 's' : ''));
    document.getElementById('action-bar-count').textContent = parts.join(', ') + ' selected';

    // Import: enabled whenever anything is selected
    const btnImport = document.getElementById('btn-import');
    btnImport.disabled = false;
    btnImport.title = '';

}

function clearSelection() {
    state.selected = [];
    document.querySelectorAll('#browse-tree .browse-checkbox').forEach(cb => {
        cb.checked = false;
        cb.disabled = false;
    });
    updateActionBar();
}

// ── Import action ────────────────────────────────────────────────────────────

function openImportModal() {
    document.getElementById('import-modal').classList.remove('hidden');
    importGoToStep('match');
}

function closeImportModal() {
    document.getElementById('import-modal').classList.add('hidden');
    // Reset modal state for re-use
    state.scanResults = [];
    state.dismissed = new Set();
    state.groupSelections = {};
    document.getElementById('apply-content').innerHTML =
        '<div class="apply-progress"><div class="apply-spinner"></div><span>Applying import...</span></div>';
    document.getElementById('apply-nav').classList.add('hidden');
    clearSelection();
}

async function onImportClick() {
    openImportModal();

    const results = document.getElementById('match-results');
    const folderCount = state.selected.filter(s => s.isDir).length;
    const fileCount = state.selected.filter(s => !s.isDir).length;
    const scanDesc = [
        folderCount ? folderCount + ' folder' + (folderCount > 1 ? 's' : '') : '',
        fileCount ? fileCount + ' file' + (fileCount > 1 ? 's' : '') : '',
    ].filter(Boolean).join(', ');
    results.innerHTML = '<div class="apply-progress"><div class="apply-spinner"></div><span>Scanning ' + esc(scanDesc) + '...</span></div>';

    try {
        const files = state.selected.filter(s => !s.isDir).map(f => ({ path: f.path, size: f.size }));
        const folders = state.selected.filter(s => s.isDir).map(s => s.path);
        const res = await apiFetch('/api/pelicula/library/scan', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ files, folders }),
        });
        if (!res.ok) {
            const err = await res.json().catch(() => ({ error: 'HTTP ' + res.status }));
            throw new Error(err.error || 'Scan failed');
        }
        state.scanResults = await res.json();
        state.dismissed = new Set();
        state.groupSelections = {};
        renderMatchResults();
    } catch (e) {
        results.innerHTML = '<div class="no-items">Scan failed: ' + esc(e.message) + '</div>';
    }
}

// ── Match results ────────────────────────────────────────────────────────────

function renderMatchResults() {
    const container = document.getElementById('match-results');
    container.innerHTML = '';

    const buckets = { new: [], exists: [], unmatched: [] };
    state.scanResults.forEach((item, i) => {
        const status = item.status || 'unmatched';
        if (buckets[status]) buckets[status].push({ ...item, idx: i });
        else buckets.unmatched.push({ ...item, idx: i });
    });

    document.getElementById('match-stats').textContent =
        buckets.new.length + ' new, ' + buckets.exists.length + ' existing, ' + buckets.unmatched.length + ' unmatched';

    // Group "new" items by groupKey so duplicates get a picker card.
    const newByKey = new Map(); // groupKey → [item, ...]
    buckets.new.forEach(item => {
        const key = item.groupKey || ('singleton:' + item.idx);
        if (!newByKey.has(key)) newByKey.set(key, []);
        newByKey.get(key).push(item);
    });

    // Count unresolved dup groups (>1 candidate, none dismissed, no selection).
    let unresolvedDups = 0;
    newByKey.forEach((items, key) => {
        if (items.length > 1) {
            const allDismissed = items.every(it => state.dismissed.has(it.idx));
            const hasPick = key in state.groupSelections;
            if (!allDismissed && !hasPick) unresolvedDups++;
        }
    });

    if (newByKey.size) {
        container.appendChild(groupHeader('New (' + buckets.new.length + ')'));

        if (unresolvedDups > 0) {
            const banner = document.createElement('div');
            banner.className = 'dup-banner';
            banner.id = 'dup-banner';
            banner.textContent = unresolvedDups + ' duplicate group' + (unresolvedDups > 1 ? 's' : '') +
                ' need a selection before applying';
            container.appendChild(banner);
        }

        newByKey.forEach((items, key) => {
            if (items.length === 1) {
                container.appendChild(createMatchItem(items[0]));
            } else {
                container.appendChild(createDupGroup(items, key));
            }
        });
    }
    if (buckets.exists.length) {
        container.appendChild(groupHeader('Already in library (' + buckets.exists.length + ')'));
        buckets.exists.forEach(item => container.appendChild(createMatchItem(item)));
    }
    if (buckets.unmatched.length) {
        container.appendChild(groupHeader('Unmatched (' + buckets.unmatched.length + ')'));
        buckets.unmatched.forEach(item => container.appendChild(createMatchItem(item)));
    }

    // Enable "Next" only if there are active new items and no unresolved dup groups.
    const activeNewCount = buckets.new.filter(item => !state.dismissed.has(item.idx)).length;
    document.getElementById('btn-configure').disabled = activeNewCount === 0 || unresolvedDups > 0;
}

// createDupGroup renders a single card for a duplicate group (multiple files
// that match the same title/episode). The user must pick one before applying.
function createDupGroup(items, groupKey) {
    const card = document.createElement('div');
    card.className = 'dup-group';
    card.id = 'dup-group-' + groupKey.replace(/[^a-z0-9]/gi, '_');

    const allDismissed = items.every(it => state.dismissed.has(it.idx));
    if (allDismissed) card.classList.add('dismissed');

    const hdr = document.createElement('div');
    hdr.className = 'dup-group-header';
    const firstMatch = items[0].match;
    let titleText = firstMatch ? firstMatch.title : items[0].file.split('/').pop();
    if (firstMatch && firstMatch.year) titleText += ' (' + firstMatch.year + ')';
    if (firstMatch && firstMatch.type === 'series' && firstMatch.season) {
        titleText += '  S' + String(firstMatch.season).padStart(2, '0') +
                     'E' + String(firstMatch.episode || 0).padStart(2, '0');
    }
    hdr.innerHTML =
        '<span class="dup-group-title">' + esc(titleText) + '</span>' +
        '<span class="dup-group-hint">Pick one file to import</span>';
    card.appendChild(hdr);

    const currentPick = state.groupSelections[groupKey] || null;
    items.forEach(item => {
        const row = document.createElement('label');
        row.className = 'dup-candidate' + (state.dismissed.has(item.idx) ? ' dismissed' : '');

        const radio = document.createElement('input');
        radio.type = 'radio';
        radio.name = 'dup-' + groupKey.replace(/[^a-z0-9]/gi, '_');
        radio.value = item.file;
        radio.checked = currentPick === item.file;
        radio.addEventListener('change', () => {
            state.groupSelections[groupKey] = item.file;
            renderMatchResults();
        });
        row.appendChild(radio);

        const info = document.createElement('span');
        info.className = 'dup-candidate-info';
        const filename = item.file.split('/').pop();
        let infoHtml = '<span class="dup-candidate-filename" title="' + escAttr(item.file) + '">' + esc(filename) + '</span>';
        if (item.size) infoHtml += ' <span class="browse-size">' + formatSize(item.size) + '</span>';
        if (item.match && item.match.confidence) {
            infoHtml += ' <span class="match-badge badge-' + esc(item.match.confidence) + '">' + esc(item.match.confidence) + '</span>';
        }
        if (item.aliases && item.aliases.length) {
            infoHtml += ' <span class="dup-alias-hint">+ ' + item.aliases.length + ' hardlink' + (item.aliases.length > 1 ? 's' : '') + '</span>';
        }
        if (item.suggestedPath) {
            infoHtml += '<div class="match-dest" title="' + escAttr(item.suggestedPath) + '">' +
                        '<span class="match-dest-arrow">&rarr;</span>' + esc(item.suggestedPath) + '</div>';
        }
        info.innerHTML = infoHtml;
        row.appendChild(info);

        card.appendChild(row);
    });

    const dismiss = document.createElement('button');
    dismiss.className = 'match-dismiss';
    dismiss.textContent = allDismissed ? 'Restore group' : 'Dismiss all';
    dismiss.addEventListener('click', () => {
        if (allDismissed) {
            items.forEach(it => state.dismissed.delete(it.idx));
        } else {
            items.forEach(it => state.dismissed.add(it.idx));
            delete state.groupSelections[groupKey];
        }
        renderMatchResults();
    });
    card.appendChild(dismiss);

    return card;
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
        const destHtml = item.suggestedPath
            ? '<div class="match-dest" title="' + escAttr(item.suggestedPath) + '">' +
              '<span class="match-dest-arrow">&rarr;</span>' + esc(item.suggestedPath) + '</div>'
            : '';
        info.innerHTML =
            '<div class="match-title">' + esc(item.match.title) +
            (item.match.year ? ' <span style="color:#666">(' + item.match.year + ')</span>' : '') +
            '</div>' +
            '<div class="match-meta">' + esc(item.match.type) + '</div>' +
            '<div class="match-file" title="' + escAttr(item.file) + '">' + esc(item.file) + '</div>' +
            destHtml;
    } else {
        info.innerHTML =
            '<div class="match-title" style="color:#666">' + esc(item.file.split('/').pop()) + '</div>' +
            '<div class="match-file" title="' + escAttr(item.file) + '">' + esc(item.file) + '</div>';
    }
    row.appendChild(info);

    if (item.size) {
        const size = document.createElement('span');
        size.className = 'browse-size';
        size.textContent = formatSize(item.size);
        row.appendChild(size);
    }

    if (item.match) {
        const badge = document.createElement('span');
        badge.className = 'match-badge badge-' + item.match.confidence;
        badge.textContent = item.match.confidence;
        row.appendChild(badge);
    }

    const statusBadge = document.createElement('span');
    statusBadge.className = 'match-badge badge-' + item.status;
    statusBadge.textContent = item.status;
    row.appendChild(statusBadge);

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

// ── Import apply ─────────────────────────────────────────────────────────────

async function doApply() {
    importGoToStep('apply');
    const content = document.getElementById('apply-content');
    content.innerHTML = '<div class="apply-progress"><div class="apply-spinner"></div><span>Applying import...</span></div>';

    const strategy = document.querySelector('input[name="strategy"]:checked').value;
    const validate = document.getElementById('validate-toggle').checked;

    // Build the item list, respecting group selections for duplicate groups.
    const items = state.scanResults
        .filter((r, i) => {
            if (r.status !== 'new' || !r.match || state.dismissed.has(i)) return false;
            const key = r.groupKey;
            if (!key) return true;
            if (key in state.groupSelections) {
                return state.groupSelections[key] === r.file;
            }
            return true;
        })
        .map(r => ({
            type: r.match.type === 'series' ? 'series' : 'movie',
            tmdbId: r.match.tmdbId || 0,
            tvdbId: r.match.tvdbId || 0,
            title: r.match.title,
            year: r.match.year || 0,
            season: r.match.season || 0,
            episode: r.match.episode || 0,
            rootFolderPath: r.match.type === 'movie' ? '/movies' : '/tv',
            monitored: strategy !== 'keep',
            sourcePath: r.file,
            destPath: r.suggestedPath || '',
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

    if (result.items && result.items.length) {
        html += '<div class="apply-items">';
        result.items.forEach(item => {
            const opClass = item.fsOp === 'failed' ? 'apply-item-failed' : 'apply-item-ok';
            html += '<div class="apply-item-row ' + opClass + '">';
            html += '<span class="apply-item-op badge-' + esc(item.fsOp || 'kept') + '">' + esc(item.fsOp || 'kept') + '</span>';
            html += '<span class="apply-item-title">' + esc(item.title) + '</span>';
            if (item.src && item.dest && item.src !== item.dest) {
                html += '<div class="apply-item-paths"><span class="apply-item-src">' + esc(item.src) + '</span>' +
                        '<span class="match-dest-arrow">&rarr;</span>' +
                        '<span class="apply-item-dest">' + esc(item.dest) + '</span></div>';
            } else if (item.dest) {
                html += '<div class="apply-item-paths"><span class="apply-item-dest">' + esc(item.dest) + '</span></div>';
            }
            if (item.error) {
                html += '<div class="apply-item-error">' + esc(item.error) + '</div>';
            }
            html += '</div>';
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

// ── Init ─────────────────────────────────────────────────────────────────────

// On the dashboard this script is loaded on demand by openStorageExplorer()
// in dashboard.js. Auto-init the browse tree immediately.
loadBrowseRoots();
