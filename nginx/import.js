import { escHtml } from './framework.js';
import { get, post, APIError } from './api.js';

// ── Media browser state ──────────────────────────────────────────────────────

const state = {
    selected: [],           // [{path, name, size, isDir}]
    scanResults: [],        // from /library/scan
    dismissed: new Set(),
    confirmed: new Set(),   // idx of items confirmed via "Use anyway"
    groupSelections: {},    // groupKey → chosen file path (TV dup groups, radio)
    groupChecked: {},       // "groupKey:idx" → bool (movie dup groups, checkbox; default true)
    groupEditions: {},      // "groupKey:idx" → edition string (user-edited label)
    libraries: [],          // fetched from /api/pelicula/libraries
};

// ── Helpers ──────────────────────────────────────────────────────────────────

// apiOrRedirect calls fn() (which wraps a get/post from api.js) and redirects
// to the login page if the session has expired (null return = 401).
async function apiOrRedirect(fn) {
    const data = await fn();
    if (data === null) {
        window.location.href = '/?login=1';
        throw new Error('Session expired');
    }
    return data;
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
        const data = await apiOrRedirect(() => get('/api/pelicula/browse'));
        renderRoots(data.entries || []);
    } catch (e) {
        document.getElementById('browse-tree').innerHTML =
            '<div class="no-items">Failed to load directories: ' + escHtml(e.message) + '</div>';
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
        const data = await apiOrRedirect(() => get('/api/pelicula/browse?path=' + encodeURIComponent(path)));
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
        childrenEl.innerHTML = '<div class="browse-loading">Error: ' + escHtml(e.message) + '</div>';
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
    state.confirmed = new Set();
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
    results.innerHTML = '<div class="apply-progress"><div class="apply-spinner"></div><span>Scanning ' + escHtml(scanDesc) + '...</span></div>';

    try {
        const files = state.selected.filter(s => !s.isDir).map(f => ({ path: f.path, size: f.size }));
        const folders = state.selected.filter(s => s.isDir).map(s => s.path);
        state.scanResults = await apiOrRedirect(() => post('/api/pelicula/library/scan', { files, folders }));
        state.dismissed = new Set();
        state.confirmed = new Set();
        state.groupSelections = {};
        renderMatchResults();
    } catch (e) {
        results.innerHTML = '<div class="no-items">Scan failed: ' + escHtml(e.message) + '</div>';
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
    const dupGroups = new Map(); // groupKey → [item, ...]
    buckets.new.forEach(item => {
        const key = item.groupKey || ('singleton:' + item.idx);
        if (!dupGroups.has(key)) dupGroups.set(key, []);
        dupGroups.get(key).push(item);
    });

    // Classify dup groups: unresolved (attention) vs resolved/dismissed (confident).
    const attentionDups = new Map();
    const confidentDupGroups = new Map();
    dupGroups.forEach((items, key) => {
        if (items.length > 1) {
            const allDismissed = items.every(it => state.dismissed.has(it.idx));
            const isMovie = items[0].match && items[0].match.type === 'movie';
            let resolved;
            if (isMovie) {
                resolved = allDismissed || isMovieGroupResolved(items, key);
            } else {
                resolved = allDismissed || (key in state.groupSelections);
            }
            if (!resolved) {
                attentionDups.set(key, items);
            } else {
                confidentDupGroups.set(key, items);
            }
        }
    });

    // Classify singleton new items by confidence level.
    const attentionLow = [];
    const attentionMedium = [];
    const confidentSingletons = [];
    dupGroups.forEach((items, key) => {
        if (items.length === 1) {
            const item = items[0];
            // Confirmed ("use anyway") or overridden items always go to confident bucket.
            if (state.confirmed.has(item.idx) || item.overridden) {
                confidentSingletons.push(item);
                return;
            }
            const conf = item.match && item.match.confidence;
            if (conf === 'low') attentionLow.push(item);
            else if (conf === 'medium') attentionMedium.push(item);
            else confidentSingletons.push(item); // high or no match → confident bucket
        }
    });

    // Unmatched items always need attention, unless overridden (got a match via popover).
    const attentionUnmatched = [];
    buckets.unmatched.forEach(item => {
        if (item.overridden) {
            confidentSingletons.push(item);
        } else {
            attentionUnmatched.push(item);
        }
    });

    // Count unresolved dup groups — gates btn-configure.
    const unresolvedDups = attentionDups.size;

    // ── Section 1: Attention (always expanded, lemon-tint wrapper) ─────────────
    const hasAttention = attentionDups.size > 0 || attentionUnmatched.length > 0 ||
                         attentionLow.length > 0 || attentionMedium.length > 0;

    if (hasAttention) {
        const attentionWrap = document.createElement('div');
        attentionWrap.className = 'attention-section';

        const subtitle = document.createElement('div');
        subtitle.className = 'attention-section-subtitle';
        subtitle.textContent = 'Title-match confidence \u2014 review these before importing';
        attentionWrap.appendChild(subtitle);

        // Dup groups first (no sub-header — dup cards are self-explanatory).
        attentionDups.forEach((items, key) => {
            attentionWrap.appendChild(createDupGroup(items, key));
        });

        // Unmatched items.
        if (attentionUnmatched.length > 0) {
            attentionWrap.appendChild(groupHeader('Unmatched (' + attentionUnmatched.length + ')'));
            attentionUnmatched.forEach(item => attentionWrap.appendChild(createMatchItem(item)));
        }

        // Low-confidence items.
        if (attentionLow.length > 0) {
            attentionWrap.appendChild(groupHeader('Low confidence (' + attentionLow.length + ')'));
            attentionLow.forEach(item => {
                const node = createMatchItem(item);
                node.classList.add('conf-low');
                attentionWrap.appendChild(node);
            });
        }

        // Medium-confidence items.
        if (attentionMedium.length > 0) {
            attentionWrap.appendChild(groupHeader('Medium confidence (' + attentionMedium.length + ')'));
            attentionMedium.forEach(item => {
                const node = createMatchItem(item);
                node.classList.add('conf-medium');
                attentionWrap.appendChild(node);
            });
        }

        container.appendChild(attentionWrap);
    }

    // ── Section 2: Confident (collapsed <details>) ─────────────────────────────
    const hasConfident = confidentSingletons.length > 0 || confidentDupGroups.size > 0;

    if (hasConfident) {
        const totalConfident = confidentSingletons.length + confidentDupGroups.size;
        const details = document.createElement('details');
        details.className = 'confident-section';

        const summary = document.createElement('summary');
        summary.className = 'confident-summary';
        const chevron = document.createElement('span');
        chevron.className = 'confident-chevron';
        chevron.textContent = '\u25B6';
        summary.appendChild(chevron);
        const label = document.createElement('span');
        label.className = 'confident-label';
        label.textContent = 'Ready to import (' + totalConfident + ')';
        summary.appendChild(label);
        const hint = document.createElement('span');
        hint.className = 'confident-hint';
        hint.textContent = 'High confidence \u2014 expand to review';
        summary.appendChild(hint);
        details.appendChild(summary);

        const body = document.createElement('div');
        body.className = 'confident-section-body';

        confidentDupGroups.forEach((items, key) => {
            body.appendChild(createDupGroup(items, key));
        });

        confidentSingletons.forEach(item => {
            body.appendChild(createMatchItem(item));
        });

        details.appendChild(body);
        container.appendChild(details);
    }

    // ── Section 3: In-place and existing (unchanged collapsibles) ──────────────
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

// ── Multi-version (edition) helpers ─────────────────────────────────────────

function dupItemKey(groupKey, idx) { return groupKey + ':' + idx; }

function getEffectiveEdition(item, groupKey) {
    const k = dupItemKey(groupKey, item.idx);
    return k in state.groupEditions ? state.groupEditions[k] : (item.edition || '');
}

function isDupItemChecked(item, groupKey) {
    return state.groupChecked[dupItemKey(groupKey, item.idx)] !== false;
}

// Returns true when all checked items in a movie dup group can proceed:
// single selection always OK; multiple require distinct non-empty labels.
function isMovieGroupResolved(items, groupKey) {
    const checked = items.filter(it => !state.dismissed.has(it.idx) && isDupItemChecked(it, groupKey));
    if (checked.length === 0) return false;
    if (checked.length === 1) return true;
    const labels = checked.map(it => getEffectiveEdition(it, groupKey).trim());
    if (labels.some(l => l === '')) return false;
    return new Set(labels.map(l => l.toLowerCase())).size === labels.length;
}

// createDupGroup renders a single card for a duplicate group (multiple files
// that match the same title/episode). For movies the user selects which cuts
// to import and labels each edition; for TV the user picks exactly one file.
function createDupGroup(items, groupKey) {
    const card = document.createElement('div');
    card.className = 'dup-group';
    card.id = 'dup-group-' + groupKey.replace(/[^a-z0-9]/gi, '_');

    const allDismissed = items.every(it => state.dismissed.has(it.idx));
    if (allDismissed) card.classList.add('dismissed');

    const isMovie = items[0].match && items[0].match.type === 'movie';

    const hdr = document.createElement('div');
    hdr.className = 'dup-group-header';
    const firstMatch = items[0].match;
    let titleText = firstMatch ? firstMatch.title : items[0].file.split('/').pop();
    if (firstMatch && firstMatch.year) titleText += ' (' + firstMatch.year + ')';
    if (firstMatch && firstMatch.type === 'series' && firstMatch.season) {
        titleText += '  S' + String(firstMatch.season).padStart(2, '0') +
                     'E' + String(firstMatch.episode || 0).padStart(2, '0');
    }
    const titleSpan = document.createElement('span');
    titleSpan.className = 'dup-group-title';
    titleSpan.textContent = titleText;
    const hintSpan = document.createElement('span');
    hintSpan.className = 'dup-group-hint';
    hintSpan.textContent = isMovie
        ? 'Multiple cuts found \u2014 select versions to import'
        : 'Pick one file to import';
    hdr.appendChild(titleSpan);
    hdr.appendChild(hintSpan);
    card.appendChild(hdr);

    if (isMovie) {
        // Checkboxes + editable edition labels for multi-version movie import.
        items.forEach(item => {
            const row = document.createElement('div');
            row.className = 'dup-candidate dup-candidate-movie' +
                (state.dismissed.has(item.idx) ? ' dismissed' : '');

            const cb = document.createElement('input');
            cb.type = 'checkbox';
            cb.checked = isDupItemChecked(item, groupKey);
            cb.addEventListener('change', () => {
                state.groupChecked[dupItemKey(groupKey, item.idx)] = cb.checked;
                renderMatchResults();
            });
            row.appendChild(cb);

            const fileSpan = document.createElement('span');
            fileSpan.className = 'dup-candidate-info';

            const nameSpan = document.createElement('span');
            nameSpan.className = 'dup-candidate-filename';
            nameSpan.title = item.file;
            nameSpan.textContent = item.file.split('/').pop();
            fileSpan.appendChild(nameSpan);

            if (item.size) {
                const sizeSpan = document.createElement('span');
                sizeSpan.className = 'browse-size';
                sizeSpan.textContent = ' ' + formatSize(item.size);
                fileSpan.appendChild(sizeSpan);
            }
            if (item.match && item.match.confidence) {
                const badge = document.createElement('span');
                badge.className = 'match-badge badge-' + item.match.confidence;
                badge.textContent = item.match.confidence;
                fileSpan.appendChild(badge);
            }
            if (item.aliases && item.aliases.length) {
                const aliasSpan = document.createElement('span');
                aliasSpan.className = 'dup-alias-hint';
                aliasSpan.textContent = '+ ' + item.aliases.length + ' hardlink' +
                    (item.aliases.length > 1 ? 's' : '');
                fileSpan.appendChild(aliasSpan);
            }
            row.appendChild(fileSpan);

            const editionInput = document.createElement('input');
            editionInput.type = 'text';
            editionInput.className = 'dup-edition-input';
            editionInput.placeholder = 'Version label (e.g. Theatrical Cut)';
            editionInput.value = getEffectiveEdition(item, groupKey);
            editionInput.addEventListener('input', () => {
                state.groupEditions[dupItemKey(groupKey, item.idx)] = editionInput.value;
                renderMatchResults();
            });
            row.appendChild(editionInput);

            card.appendChild(row);
        });

        // Inline validation hint when multiple cuts are selected.
        const checkedItems = items.filter(
            it => !state.dismissed.has(it.idx) && isDupItemChecked(it, groupKey));
        if (checkedItems.length > 1) {
            const labels = checkedItems.map(it => getEffectiveEdition(it, groupKey).trim());
            const hasEmpty = labels.some(l => l === '');
            const hasDups = new Set(labels.map(l => l.toLowerCase())).size < labels.length;
            if (hasEmpty || hasDups) {
                const hint = document.createElement('div');
                hint.className = 'dup-edition-error';
                hint.textContent = hasEmpty
                    ? 'Each selected version needs a label before importing.'
                    : 'Version labels must be unique.';
                card.appendChild(hint);
            }
        }
    } else {
        // Radio buttons: pick exactly one file (TV series behaviour).
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
            let infoHtml = '<span class="dup-candidate-filename" title="' + escHtml(item.file) + '">' + escHtml(filename) + '</span>';
            if (item.size) infoHtml += ' <span class="browse-size">' + formatSize(item.size) + '</span>';
            if (item.match && item.match.confidence) {
                infoHtml += ' <span class="match-badge badge-' + escHtml(item.match.confidence) + '">' + escHtml(item.match.confidence) + '</span>';
            }
            if (item.aliases && item.aliases.length) {
                infoHtml += ' <span class="dup-alias-hint">+ ' + item.aliases.length + ' hardlink' + (item.aliases.length > 1 ? 's' : '') + '</span>';
            }
            if (item.suggestedPath) {
                infoHtml += '<div class="match-dest" title="' + escHtml(item.suggestedPath) + '">' +
                            '<span class="match-dest-arrow">&rarr;</span>' + escHtml(item.suggestedPath) + '</div>';
            }
            info.innerHTML = infoHtml;
            row.appendChild(info);

            card.appendChild(row);
        });
    }

    const dismiss = document.createElement('button');
    dismiss.className = 'match-dismiss';
    dismiss.textContent = allDismissed ? 'Restore group' : 'Dismiss all';
    dismiss.addEventListener('click', () => {
        if (allDismissed) {
            items.forEach(it => state.dismissed.delete(it.idx));
        } else {
            items.forEach(it => state.dismissed.add(it.idx));
            if (!isMovie) delete state.groupSelections[groupKey];
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
            ? '<div class="match-dest" title="' + escHtml(item.suggestedPath) + '">' +
              '<span class="match-dest-arrow">&rarr;</span>' + escHtml(item.suggestedPath) + '</div>'
            : '';
        info.innerHTML =
            '<div class="match-title">' + escHtml(item.match.title) +
            (item.match.year ? ' <span style="color:#666">(' + item.match.year + ')</span>' : '') +
            '</div>' +
            '<div class="match-meta">' + escHtml(item.match.type) + '</div>' +
            '<div class="match-file" title="' + escHtml(item.file) + '">' + escHtml(item.file) + '</div>' +
            destHtml;
    } else {
        info.innerHTML =
            '<div class="match-title" style="color:#666">' + escHtml(item.file.split('/').pop()) + '</div>' +
            '<div class="match-file" title="' + escHtml(item.file) + '">' + escHtml(item.file) + '</div>';
    }
    row.appendChild(info);

    if (item.size) {
        const size = document.createElement('span');
        size.className = 'browse-size';
        size.textContent = formatSize(item.size);
        row.appendChild(size);
    }

    if (item.match) {
        const conf = item.overridden ? 'overridden' : (state.confirmed.has(item.idx) ? 'confirmed' : item.match.confidence);
        const badge = document.createElement('span');
        badge.className = 'match-badge badge-' + conf;
        badge.textContent = conf;
        row.appendChild(badge);
    }

    const statusBadge = document.createElement('span');
    statusBadge.className = 'match-badge badge-' + item.status;
    statusBadge.textContent = item.status;
    row.appendChild(statusBadge);

    // Determine if this item is in the attention section (medium/low confidence or unmatched).
    const conf = item.match && item.match.confidence;
    const isAttention = !item.overridden && !state.confirmed.has(item.idx) &&
                        (conf === 'medium' || conf === 'low' || !item.match);

    if (isAttention) {
        // Action cluster: Change match / Use anyway / Dismiss
        const cluster = document.createElement('div');
        cluster.className = 'action-cluster';

        if (item.match) {
            // Medium or low confidence — has a candidate match
            const changeBtn = document.createElement('button');
            changeBtn.className = 'action-btn primary-action';
            changeBtn.textContent = 'Change match';
            changeBtn.addEventListener('click', () => openChangeMatchPopover(item, row));
            cluster.appendChild(changeBtn);

            const useBtn = document.createElement('button');
            useBtn.className = 'action-btn';
            useBtn.textContent = 'Use anyway';
            useBtn.addEventListener('click', () => confirmItem(item));
            cluster.appendChild(useBtn);
        } else {
            // Unmatched — no candidate yet
            const findBtn = document.createElement('button');
            findBtn.className = 'action-btn primary-action';
            findBtn.textContent = 'Find match';
            findBtn.addEventListener('click', () => openChangeMatchPopover(item, row));
            cluster.appendChild(findBtn);
        }

        const dismiss = document.createElement('button');
        dismiss.className = 'action-btn match-dismiss';
        dismiss.textContent = state.dismissed.has(item.idx) ? 'Restore' : 'Dismiss';
        dismiss.addEventListener('click', () => {
            if (state.dismissed.has(item.idx)) {
                state.dismissed.delete(item.idx);
            } else {
                state.dismissed.add(item.idx);
            }
            renderMatchResults();
        });
        cluster.appendChild(dismiss);

        row.appendChild(cluster);
    } else {
        // Non-attention items (high confidence, confirmed, overridden): just a dismiss button for new items
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

// confirmItem marks an item as "use anyway" regardless of its confidence score,
// routing it to the confident section on next render.
function confirmItem(item) {
    state.confirmed.add(item.idx);
    renderMatchResults();
}

// openChangeMatchPopover opens the change-match search popover anchored to rowEl.
let _activePopover = null;

function openChangeMatchPopover(item, rowEl) {
    // Close any existing popover first
    if (_activePopover) {
        _activePopover.remove();
        _activePopover = null;
    }

    const popover = document.createElement('div');
    popover.className = 'match-popover';
    _activePopover = popover;

    // Header
    const header = document.createElement('div');
    header.className = 'match-popover-header';
    header.textContent = 'Search for a different match';
    popover.appendChild(header);

    // Search input
    const input = document.createElement('input');
    input.type = 'text';
    input.placeholder = 'Title\u2026';
    // Pre-populate with parsed title from match or filename
    const defaultTitle = (item.match && item.match.title) ||
        item.file.split('/').pop().replace(/\.\w+$/, '').replace(/[._]/g, ' ').trim();
    input.value = defaultTitle;
    popover.appendChild(input);

    // Results container
    const resultsEl = document.createElement('div');
    resultsEl.className = 'match-popover-results';
    popover.appendChild(resultsEl);

    // Determine media type filter from existing match or default to both
    const typeFilter = (item.match && item.match.type) || '';

    // Debounced search
    let debounceTimer = null;
    function doSearch(q) {
        if (!q.trim()) {
            resultsEl.replaceChildren();
            return;
        }
        let url = '/api/pelicula/search?q=' + encodeURIComponent(q.trim());
        if (typeFilter) url += '&type=' + encodeURIComponent(typeFilter);
        get(url).then(data => {
            if (data === null) { window.location.href = '/?login=1'; return; }
            const results = (data && data.results) || [];
            resultsEl.replaceChildren();
            if (!results.length) {
                const empty = document.createElement('div');
                empty.className = 'match-popover-empty';
                empty.textContent = 'No results found';
                resultsEl.appendChild(empty);
                return;
            }
            results.forEach(result => {
                const row = document.createElement('div');
                row.className = 'match-popover-result';

                const titleEl = document.createElement('div');
                titleEl.className = 'match-popover-result-title';
                titleEl.textContent = result.title + (result.year ? ' (' + result.year + ')' : '');
                row.appendChild(titleEl);

                const metaEl = document.createElement('div');
                metaEl.className = 'match-popover-result-meta';
                const parts = [result.type];
                if (result.network) parts.push(result.network);
                if (result.seasonCount) parts.push(result.seasonCount + ' season' + (result.seasonCount !== 1 ? 's' : ''));
                metaEl.textContent = parts.join(' \u00B7 ');
                row.appendChild(metaEl);

                if (result.overview) {
                    const overviewEl = document.createElement('div');
                    overviewEl.className = 'match-popover-result-overview';
                    overviewEl.textContent = result.overview;
                    row.appendChild(overviewEl);
                }

                row.addEventListener('click', () => {
                    applyOverride(item, result);
                    closePopover();
                });
                resultsEl.appendChild(row);
            });
        }).catch(() => {
            resultsEl.replaceChildren();
            const empty = document.createElement('div');
            empty.className = 'match-popover-empty';
            empty.textContent = 'Search failed';
            resultsEl.appendChild(empty);
        });
    }

    input.addEventListener('input', () => {
        clearTimeout(debounceTimer);
        debounceTimer = setTimeout(() => doSearch(input.value), 300);
    });

    // Position popover anchored to rowEl
    document.body.appendChild(popover);
    const rect = rowEl.getBoundingClientRect();
    const popW = Math.min(340, window.innerWidth * 0.9);
    let left = rect.left;
    if (left + popW > window.innerWidth - 8) left = window.innerWidth - popW - 8;
    if (left < 8) left = 8;
    let top = rect.bottom + 4;
    if (top + 400 > window.innerHeight - 8) top = rect.top - 404;
    if (top < 8) top = 8;
    popover.style.left = left + 'px';
    popover.style.top = top + 'px';

    // Focus input and trigger initial search
    input.focus();
    input.select();
    doSearch(input.value);

    // Close helpers
    function closePopover() {
        if (_activePopover === popover) {
            popover.remove();
            _activePopover = null;
        }
        document.removeEventListener('keydown', onKey);
        document.removeEventListener('mousedown', onOutsideClick);
    }

    function onKey(e) {
        if (e.key === 'Escape') closePopover();
    }
    function onOutsideClick(e) {
        if (!popover.contains(e.target)) closePopover();
    }

    // Delay outside-click listener so it doesn't fire on the button click that opened it
    setTimeout(() => {
        document.addEventListener('keydown', onKey);
        document.addEventListener('mousedown', onOutsideClick);
    }, 0);
}

// computeGroupKey mirrors the server-side matchItemGroupKey so the wizard can
// regroup items after the user overrides a match. Keeping the formats in sync
// with middleware/internal/app/library/match.go is required: the apply handler
// rejects batches whose client-side group keys disagree with its own.
function computeGroupKey(scanItem) {
    const m = scanItem.match;
    if (!m) return 'unmatched:' + scanItem.file;
    if (m.type === 'movie' && m.tmdbId > 0) return 'movie:' + m.tmdbId;
    if (m.type === 'series' && m.tvdbId > 0) {
        return 'series:' + m.tvdbId + ':s' + (m.season || 0) + 'e' + (m.episode || 0);
    }
    return 'unmatched:' + scanItem.file;
}

// extractSeasonEpisode mirrors server-side extractSeason/extractEpisode in
// match.go. Used when overriding a series match — the new match returned by
// /search has no per-episode info, so we re-derive S/E from the filename.
function extractSeasonEpisode(filename) {
    const m = filename.match(/\bS(\d{1,2})E(\d{1,2})\b/i);
    if (!m) return { season: 0, episode: 0 };
    return { season: parseInt(m[1], 10), episode: parseInt(m[2], 10) };
}

// applyOverride updates the match for an item to the operator-selected result.
async function applyOverride(item, searchResult) {
    const scanItem = state.scanResults[item.idx];
    // For series, re-derive season/episode from the filename — the search
    // result is series-level and carries no per-episode info, but the original
    // file's S/E is what actually goes to Sonarr.
    let season = 0, episode = 0;
    if (searchResult.type === 'series') {
        const ext = extractSeasonEpisode(scanItem.file.split('/').pop());
        season = ext.season;
        episode = ext.episode;
    }
    scanItem.match = {
        tmdbId: searchResult.tmdbId || 0,
        tvdbId: searchResult.tvdbId || 0,
        title: searchResult.title,
        year: searchResult.year || 0,
        type: searchResult.type,
        season,
        episode,
        confidence: 'overridden',
    };
    scanItem.overridden = true;
    // Recompute groupKey from the new match so two items overridden to the same
    // title collapse into one duplicate group instead of slipping through as
    // confident singletons and getting rejected by the apply handler's
    // duplicate-key guard.
    scanItem.groupKey = computeGroupKey(scanItem);
    // If the item was unmatched, give it status 'new' so doApply picks it up
    if (scanItem.status === 'unmatched') {
        scanItem.status = 'new';
    }

    // Fetch updated destination path; clear stale path on any failure so
    // doApply sends an empty destPath and the backend recomputes it.
    scanItem.suggestedPath = '';
    try {
        const params = new URLSearchParams({
            type: searchResult.type,
            title: searchResult.title,
            year: String(searchResult.year || 0),
        });
        const data = await get('/api/pelicula/library/suggest-path?' + params.toString());
        if (data && data.path) scanItem.suggestedPath = data.path;
    } catch (e) { /* non-fatal */ }

    renderMatchResults();
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

    // Pre-build a map of which group keys are movie dup groups (>1 item, movie type).
    const allDupGroups = new Map();
    state.scanResults.forEach((r, i) => {
        if (r.groupKey) {
            if (!allDupGroups.has(r.groupKey)) allDupGroups.set(r.groupKey, []);
            allDupGroups.get(r.groupKey).push({ ...r, idx: i });
        }
    });
    const movieDupKeys = new Set();
    allDupGroups.forEach((grpItems, key) => {
        if (grpItems.length > 1 && grpItems[0].match && grpItems[0].match.type === 'movie') {
            movieDupKeys.add(key);
        }
    });

    // Build the item list, respecting group selections for duplicate groups.
    const newItems = [];
    state.scanResults.forEach((r, i) => {
        if (r.status !== 'new' || !r.match || state.dismissed.has(i)) return;
        const key = r.groupKey;
        if (key) {
            if (movieDupKeys.has(key)) {
                if (!isDupItemChecked({ idx: i }, key)) return;
            } else if (key in state.groupSelections) {
                if (state.groupSelections[key] !== r.file) return;
            }
        }
        const isMultiEdition = key && movieDupKeys.has(key);
        const edition = isMultiEdition
            ? getEffectiveEdition({ idx: i, edition: r.edition }, key).trim()
            : '';
        newItems.push({
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
            // For edition items let the backend compute the edition-aware filename.
            destPath: edition ? '' : (r.suggestedPath || ''),
            edition,
        });
    });

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
        const result = await apiOrRedirect(() => post('/api/pelicula/library/apply', { items, strategy: newItems.length === 0 ? 'register' : strategy, validate }));
        renderApplyResult(result, validate);
    } catch (e) {
        content.innerHTML = '<div class="no-items">Import failed: ' + escHtml(e.message) + '</div>';
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
            html += '<div class="apply-error-item">' + escHtml(e) + '</div>';
        });
        html += '</div>';
    }

    if (result.items && result.items.length) {
        html += '<div class="apply-items">';
        result.items.forEach(item => {
            const opClass = item.fsOp === 'failed' ? 'apply-item-failed' : 'apply-item-ok';
            html += '<div class="apply-item-row ' + opClass + '">';
            html += '<span class="apply-item-op badge-' + escHtml(item.fsOp || 'kept') + '">' + escHtml(item.fsOp || 'kept') + '</span>';
            html += '<span class="apply-item-title">' + escHtml(item.title) + '</span>';
            if (item.src && item.dest && item.src !== item.dest) {
                html += '<div class="apply-item-paths"><span class="apply-item-src">' + escHtml(item.src) + '</span>' +
                        '<span class="match-dest-arrow">&rarr;</span>' +
                        '<span class="apply-item-dest">' + escHtml(item.dest) + '</span></div>';
            } else if (item.dest) {
                html += '<div class="apply-item-paths"><span class="apply-item-dest">' + escHtml(item.dest) + '</span></div>';
            }
            if (item.error) {
                html += '<div class="apply-item-error">' + escHtml(item.error) + '</div>';
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
        const data = await get('/api/pelicula/libraries');
        if (data === null) return; // 401 handled silently on init
        state.libraries = data || [];
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
        opt.textContent = lib.name;
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

document.getElementById('import-clear-btn').addEventListener('click', clearSelection);
document.getElementById('btn-import').addEventListener('click', onImportClick);
document.getElementById('import-modal-close-btn').addEventListener('click', closeImportModal);
document.getElementById('import-cancel-btn').addEventListener('click', closeImportModal);
document.getElementById('import-back-btn').addEventListener('click', () => importGoToStep('match'));
document.getElementById('import-apply-btn').addEventListener('click', doApply);
document.getElementById('import-done-btn').addEventListener('click', closeImportModal);

// ── Init ─────────────────────────────────────────────────────────────────────

// Wire strategy radio changes to show/hide the hardlink toggle.
document.querySelectorAll('input[name="strategy"]').forEach(function(radio) {
    radio.addEventListener('change', updateHardlinkToggle);
});

// On the dashboard this script is loaded on demand by openStorageExplorer()
// in dashboard.js. Auto-init the browse tree immediately.
// Load libraries first so /media directory annotations are ready when the tree renders.
loadImportLibraries().then(loadBrowseRoots);
