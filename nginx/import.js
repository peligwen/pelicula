// ── Media browser state ──────────────────────────────────────────────────────

const state = {
    selected: [],           // [{path, name, size, isDir}]
    scanResults: [],        // from /library/scan
    dismissed: new Set(),
    groupSelections: {},    // groupKey → chosen file path (for dup groups)
    libraries: [],          // fetched from /api/pelicula/libraries
};

// ── Helpers ──────────────────────────────────────────────────────────────────

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
        updateHardlinkToggle();
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
    if (el('strategy-example-import')) el('strategy-example-import').textContent = fwd;
    if (el('strategy-example-link'))   el('strategy-example-link').textContent   = bwd;
}

// updateHardlinkToggle shows/hides the hardlink sub-option based on strategy.
function updateHardlinkToggle() {
    const strategyEl = document.querySelector('input[name="strategy"]:checked');
    const toggleGroup = document.getElementById('hardlink-toggle-group');
    if (!toggleGroup) return;
    toggleGroup.style.display = (strategyEl && strategyEl.value === 'link') ? '' : 'none';
}

// ── Browse tree ──────────────────────────────────────────────────────────────

function isMediaChild(path) {
    // /media/something with no further slashes
    return /^\/media\/[^/]+$/.test(path);
}

function libraryForPath(path) {
    return state.libraries.find(lib => '/media/' + lib.slug === path) || null;
}

async function loadBrowseRoots() {
    try {
        // Ensure libraries are loaded before rendering (needed for /media annotations)
        if (!state.libraries.length) await loadImportLibraries();
        const res = await apiFetch('/api/pelicula/browse');
        if (!res.ok) throw new Error('Failed to load directories');
        const data = await res.json();
        renderRoots(data.entries || []);
    } catch (e) {
        document.getElementById('browse-tree').innerHTML =
            '<div class="no-items">Failed to load directories: ' + PeliculaFW.esc(e.message) + '</div>';
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

        // Annotate /media immediate children with library status
        if (isMediaChild(entry.path)) {
            const lib = libraryForPath(entry.path);
            if (lib) {
                // Registered library — show a badge
                const badge = document.createElement('span');
                badge.className = 'browse-lib-badge';
                badge.textContent = lib.name;
                badge.title = lib.type + ' · ' + lib.arr;
                row.appendChild(badge);
            } else {
                // Unregistered — show "Add as Library" button (admin only)
                const addBtn = document.createElement('button');
                addBtn.className = 'browse-add-lib admin-only';
                addBtn.textContent = '+ Library';
                addBtn.title = 'Add as library';
                addBtn.addEventListener('click', (e) => {
                    e.stopPropagation(); // don't trigger dir expand
                    if (typeof addLibraryFromStorage === 'function') {
                        addLibraryFromStorage(entry.path);
                    }
                });
                row.appendChild(addBtn);
            }
        }

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
        childrenEl.innerHTML = '<div class="browse-loading">Error: ' + PeliculaFW.esc(e.message) + '</div>';
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
    results.innerHTML = '<div class="apply-progress"><div class="apply-spinner"></div><span>Scanning ' + PeliculaFW.esc(scanDesc) + '...</span></div>';

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
        results.innerHTML = '<div class="no-items">Scan failed: ' + PeliculaFW.esc(e.message) + '</div>';
    }
}

// ── Match results ────────────────────────────────────────────────────────────

function renderMatchResults() {
    const container = document.getElementById('match-results');
    container.innerHTML = '';

    const buckets = { new: [], in_place: [], exists: [], unmatched: [] };
    state.scanResults.forEach((item, i) => {
        const status = item.status || 'unmatched';
        if (buckets[status]) buckets[status].push({ ...item, idx: i });
        else buckets.unmatched.push({ ...item, idx: i });
    });

    const statParts = [];
    if (buckets.new.length) statParts.push(buckets.new.length + ' new');
    if (buckets.in_place.length) statParts.push(buckets.in_place.length + ' in place');
    if (buckets.exists.length) statParts.push(buckets.exists.length + ' existing');
    if (buckets.unmatched.length) statParts.push(buckets.unmatched.length + ' unmatched');
    document.getElementById('match-stats').textContent = statParts.join(', ');

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
    if (buckets.in_place.length) {
        container.appendChild(createCollapsibleSummary(
            'In place (' + buckets.in_place.length + ')',
            'Already in correct location \u2014 will be registered automatically',
            buckets.in_place
        ));
    }
    if (buckets.exists.length) {
        container.appendChild(createCollapsibleSummary(
            'Already in library (' + buckets.exists.length + ')',
            'Already tracked by Sonarr/Radarr \u2014 no action needed',
            buckets.exists
        ));
    }
    if (buckets.unmatched.length) {
        container.appendChild(groupHeader('Unmatched (' + buckets.unmatched.length + ')'));
        buckets.unmatched.forEach(item => container.appendChild(createMatchItem(item)));
    }

    // Enable "Next" when there are actionable items and no unresolved dup groups.
    const activeNewCount = buckets.new.filter(item => !state.dismissed.has(item.idx)).length;
    const hasInPlace = buckets.in_place.length > 0;
    const btn = document.getElementById('btn-configure');

    if (activeNewCount === 0 && hasInPlace) {
        // Only in-place items — skip Configure, go straight to Apply.
        btn.disabled = false;
        btn.textContent = 'Register in Library';
        btn.onclick = function () { doApply(); };
    } else {
        btn.disabled = (activeNewCount === 0 && !hasInPlace) || unresolvedDups > 0;
        btn.textContent = 'Next: Configure';
        btn.onclick = function () { importGoToStep('configure'); };
    }
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
        '<span class="dup-group-title">' + PeliculaFW.esc(titleText) + '</span>' +
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
        let infoHtml = '<span class="dup-candidate-filename" title="' + PeliculaFW.esc(item.file) + '">' + PeliculaFW.esc(filename) + '</span>';
        if (item.size) infoHtml += ' <span class="browse-size">' + formatSize(item.size) + '</span>';
        if (item.match && item.match.confidence) {
            infoHtml += ' <span class="match-badge badge-' + PeliculaFW.esc(item.match.confidence) + '">' + PeliculaFW.esc(item.match.confidence) + '</span>';
        }
        if (item.aliases && item.aliases.length) {
            infoHtml += ' <span class="dup-alias-hint">+ ' + item.aliases.length + ' hardlink' + (item.aliases.length > 1 ? 's' : '') + '</span>';
        }
        if (item.suggestedPath) {
            infoHtml += '<div class="match-dest" title="' + PeliculaFW.esc(item.suggestedPath) + '">' +
                        '<span class="match-dest-arrow">&rarr;</span>' + PeliculaFW.esc(item.suggestedPath) + '</div>';
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

function createCollapsibleSummary(headerText, subtitle, items) {
    const wrap = document.createElement('div');
    wrap.className = 'collapsible-summary';

    const header = document.createElement('div');
    header.className = 'collapsible-summary-header';

    const chevron = document.createElement('span');
    chevron.className = 'collapsible-summary-chevron';
    chevron.textContent = '\u25B6';
    header.appendChild(chevron);

    const label = document.createElement('span');
    label.className = 'collapsible-summary-label';
    label.textContent = headerText;
    header.appendChild(label);

    const sub = document.createElement('span');
    sub.className = 'collapsible-summary-subtitle';
    sub.textContent = subtitle;
    header.appendChild(sub);

    header.addEventListener('click', () => wrap.classList.toggle('open'));
    wrap.appendChild(header);

    const body = document.createElement('div');
    body.className = 'collapsible-summary-body';
    items.forEach(item => {
        const row = document.createElement('div');
        row.className = 'collapsible-summary-item';
        if (item.match) {
            row.textContent = item.match.title;
            if (item.match.year) {
                const yr = document.createElement('span');
                yr.className = 'collapsible-summary-item-year';
                yr.textContent = ' (' + item.match.year + ')';
                row.appendChild(yr);
            }
        } else {
            row.textContent = item.file.split('/').pop();
        }
        body.appendChild(row);
    });
    wrap.appendChild(body);

    return wrap;
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
            ? '<div class="match-dest" title="' + PeliculaFW.esc(item.suggestedPath) + '">' +
              '<span class="match-dest-arrow">&rarr;</span>' + PeliculaFW.esc(item.suggestedPath) + '</div>'
            : '';
        info.innerHTML =
            '<div class="match-title">' + PeliculaFW.esc(item.match.title) +
            (item.match.year ? ' <span style="color:#666">(' + item.match.year + ')</span>' : '') +
            '</div>' +
            '<div class="match-meta">' + PeliculaFW.esc(item.match.type) + '</div>' +
            '<div class="match-file" title="' + PeliculaFW.esc(item.file) + '">' + PeliculaFW.esc(item.file) + '</div>' +
            destHtml;
    } else {
        info.innerHTML =
            '<div class="match-title" style="color:#666">' + PeliculaFW.esc(item.file.split('/').pop()) + '</div>' +
            '<div class="match-file" title="' + PeliculaFW.esc(item.file) + '">' + PeliculaFW.esc(item.file) + '</div>';
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

    const strategyEl = document.querySelector('input[name="strategy"]:checked');
    const strategyBase = strategyEl ? strategyEl.value : 'register';
    // For "link", check whether the user wants hardlinks.
    const hardlinkEl = document.getElementById('hardlink-toggle');
    const strategy = (strategyBase === 'link' && hardlinkEl && hardlinkEl.checked) ? 'hardlink' : strategyBase;
    const validateEl = document.getElementById('validate-toggle');
    const validate = validateEl ? validateEl.checked : false;

    // Build the item list, respecting group selections for duplicate groups.
    const newItems = state.scanResults
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
            rootFolderPath: getLibraryPathForType(r.match.type),
            monitored: strategy !== 'register',
            sourcePath: r.file,
            destPath: r.suggestedPath || '',
        }));

    // In-place items need registration but no FS operation — always use "register".
    const inPlaceItems = state.scanResults
        .filter(r => r.status === 'in_place' && r.match)
        .map(r => ({
            type: r.match.type === 'series' ? 'series' : 'movie',
            tmdbId: r.match.tmdbId || 0,
            tvdbId: r.match.tvdbId || 0,
            title: r.match.title,
            year: r.match.year || 0,
            season: r.match.season || 0,
            episode: r.match.episode || 0,
            rootFolderPath: getLibraryPathForType(r.match.type),
            monitored: false,
            sourcePath: r.file,
            destPath: r.suggestedPath || '',
        }));

    const items = newItems.concat(inPlaceItems);

    if (!items.length) {
        content.innerHTML = '<div class="no-items">No items to import</div>';
        document.getElementById('apply-nav').classList.remove('hidden');
        return;
    }

    try {
        const res = await apiFetch('/api/pelicula/library/apply', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ items, strategy: newItems.length === 0 ? 'register' : strategy, validate }),
        });
        if (!res.ok) {
            const err = await res.json().catch(() => ({ error: 'HTTP ' + res.status }));
            throw new Error(err.error || 'Apply failed');
        }
        const result = await res.json();
        renderApplyResult(result, validate);
    } catch (e) {
        content.innerHTML = '<div class="no-items">Import failed: ' + PeliculaFW.esc(e.message) + '</div>';
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
            html += '<div class="apply-error-item">' + PeliculaFW.esc(e) + '</div>';
        });
        html += '</div>';
    }

    if (result.items && result.items.length) {
        html += '<div class="apply-items">';
        result.items.forEach(item => {
            const opClass = item.fsOp === 'failed' ? 'apply-item-failed' : 'apply-item-ok';
            html += '<div class="apply-item-row ' + opClass + '">';
            html += '<span class="apply-item-op badge-' + PeliculaFW.esc(item.fsOp || 'kept') + '">' + PeliculaFW.esc(item.fsOp || 'kept') + '</span>';
            html += '<span class="apply-item-title">' + PeliculaFW.esc(item.title) + '</span>';
            if (item.src && item.dest && item.src !== item.dest) {
                html += '<div class="apply-item-paths"><span class="apply-item-src">' + PeliculaFW.esc(item.src) + '</span>' +
                        '<span class="match-dest-arrow">&rarr;</span>' +
                        '<span class="apply-item-dest">' + PeliculaFW.esc(item.dest) + '</span></div>';
            } else if (item.dest) {
                html += '<div class="apply-item-paths"><span class="apply-item-dest">' + PeliculaFW.esc(item.dest) + '</span></div>';
            }
            if (item.error) {
                html += '<div class="apply-item-error">' + PeliculaFW.esc(item.error) + '</div>';
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

// ── Library helpers ──────────────────────────────────────────────────────────

async function loadImportLibraries() {
    try {
        const res = await apiFetch('/api/pelicula/libraries');
        if (!res.ok) return;
        state.libraries = await res.json() || [];
        populateLibrarySelect();
    } catch (e) { /* non-fatal */ }
}

function populateLibrarySelect() {
    const sel = document.getElementById('import-library-select');
    if (!sel || !state.libraries.length) return;
    sel.replaceChildren();
    // Prepend the "Auto" option so mixed batches route per item type by default.
    const autoOpt = document.createElement('option');
    autoOpt.value = '';
    autoOpt.textContent = 'Auto (per type)';
    sel.appendChild(autoOpt);
    state.libraries.forEach(function(lib) {
        const opt = document.createElement('option');
        opt.value = '/media/' + lib.slug;
        opt.textContent = lib.name + ' (/media/' + lib.slug + ')';
        opt.dataset.arr = lib.arr;
        sel.appendChild(opt);
    });
    sel.value = ''; // ensure Auto is selected
}

// getLibraryPathForType returns the ContainerPath for the best library match
// given a match type ('movie' or 'series'). When the user has picked a specific
// library override, that path is used for all items. In Auto mode (sel.value === ""),
// items are routed to the first library whose arr integration matches the type.
function getLibraryPathForType(type) {
    const sel = document.getElementById('import-library-select');
    if (!sel) return type === 'movie' ? '/media/movies' : '/media/tv';
    // User picked a specific library — apply it to all items in the batch.
    if (sel.value !== '') return sel.value;
    // Auto mode: find the first library whose arr integration matches the type.
    const arr = type === 'movie' ? 'radarr' : 'sonarr';
    for (let i = 0; i < sel.options.length; i++) {
        if (sel.options[i].dataset.arr === arr) {
            return sel.options[i].value;
        }
    }
    // Final fallback if no matching library is configured.
    return type === 'movie' ? '/media/movies' : '/media/tv';
}

// ── Init ─────────────────────────────────────────────────────────────────────

// Wire strategy radio changes to show/hide the hardlink toggle.
document.querySelectorAll('input[name="strategy"]').forEach(function(radio) {
    radio.addEventListener('change', updateHardlinkToggle);
});

// On the dashboard this script is loaded on demand by openStorageExplorer()
// in dashboard.js. Auto-init the browse tree immediately.
loadBrowseRoots();
loadImportLibraries();
