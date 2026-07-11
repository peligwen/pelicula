// nginx/search.js
// Search component — registered with framework.js; mounted by dashboard.js.

'use strict';

import { component, html, raw, toast } from '/framework.js';
import { get, post } from '/api.js';

// ── Module-level state ────────────────────────────────────────────────────
let searchTimeout;
let searchType = '';
let lastResults = [];
let lockedResult = null;

// Cached DOM refs — set during init(), after the DOM is available.
let searchInput, searchResults, searchFilters;

// ── Helpers ───────────────────────────────────────────────────────────────
function buildDetailChips(r) {
    const chips = [];
    if (r.rating > 0) chips.push(html`<span class="search-detail-chip search-detail-rating">&#9733; ${r.rating.toFixed(1)}</span>`);
    if (r.certification) chips.push(html`<span class="search-detail-chip">${r.certification}</span>`);
    if (r.runtime > 0) {
        const label = r.type === 'series' ? `${r.runtime} min/ep` : `${r.runtime} min`;
        chips.push(html`<span class="search-detail-chip">${label}</span>`);
    }
    if (r.network) {
        const networkLabel = r.seasonCount > 0
            ? html`${r.network} &middot; ${r.seasonCount} season${r.seasonCount !== 1 ? 's' : ''}`
            : html`${r.network}`;
        chips.push(html`<span class="search-detail-chip">${networkLabel}</span>`);
    } else if (r.seasonCount > 0) {
        chips.push(html`<span class="search-detail-chip">${r.seasonCount} season${r.seasonCount !== 1 ? 's' : ''}</span>`);
    }
    if (r.genres && r.genres.length) {
        const genreText = r.genres.slice(0, 3).map(g => html`${g}`.str).join(' &middot; ');
        chips.push(html`<span class="search-detail-chip">${raw(genreText)}</span>`);
    }
    return chips.map(c => c.str).join('');
}

function renderResultCard(r) {
    const poster = r.poster
        ? html`<img src="${r.poster}" alt="">`
        : raw('<div class="no-poster"></div>');
    const badge = r.type === 'movie' ? 'Movie' : 'Series';
    const tmdbId = r.tmdbId || 0;
    const tvdbId = r.tvdbId || 0;
    const added = r.added;
    const role = document.body.dataset.role;
    const isManager = role === 'manager' || role === 'admin';
    const addBtn = html`<button
            class="${added ? 'search-add added' : 'search-add'}"
            ${added ? raw('disabled') : raw('')}
            data-action="add-media"
            data-type="${r.type}"
            data-tmdb="${tmdbId}"
            data-tvdb="${tvdbId}"
            data-testid="search-add-btn"
          >${added ? 'Added' : 'Add'}</button>`;
    // Secondary affordance next to Add — Manager+ only, never shown for
    // viewers (submit-request is their only action) and never once the item
    // is already added. Opens #add-options-modal (index.html) to override
    // quality profile / target library before POSTing search/add.
    const addOptionsBtn = (isManager && !added)
        ? html`<button
                class="search-add-options"
                data-action="add-media-options"
                data-type="${r.type}"
                data-tmdb="${tmdbId}"
                data-tvdb="${tvdbId}"
                data-testid="search-add-options-btn"
              >Add with options…</button>`
        : raw('');
    const requestBtn = html`<button
                class="search-request"
                data-action="submit-request"
                data-type="${r.type}"
                data-tmdb="${tmdbId}"
                data-tvdb="${tvdbId}"
                data-title="${r.title}"
                data-year="${r.year || 0}"
                data-poster="${r.poster || ''}"
                data-testid="search-request-btn"
              >Request</button>`;
    // Secondary affordance next to Request — viewers only, series only (season
    // scope has no meaning for movies), never once already added. Opens the
    // SAME #add-options-modal as Manager+'s "Add with options…" in 'request'
    // mode: arr-meta is never fetched (viewers get 403 on it) and confirming
    // calls submitRequest() with the chosen seasons instead of addMedia().
    const requestOptionsBtn = (!isManager && !added && r.type === 'series')
        ? html`<button
                class="search-add-options"
                data-action="request-options"
                data-type="${r.type}"
                data-tmdb="${tmdbId}"
                data-tvdb="${tvdbId}"
                data-testid="search-request-options-btn"
              >Request seasons…</button>`
        : raw('');
    const actionBtn = isManager
        ? raw(addBtn.str + addOptionsBtn.str)
        : raw(requestBtn.str + requestOptionsBtn.str);
    const detailChips = buildDetailChips(r);
    const overview = r.overview ? html`<div class="search-overview">${r.overview}</div>` : raw('');
    return html`
        <div class="search-card" data-testid="search-result-card" data-action="show-detail" data-tmdb="${tmdbId}" data-tvdb="${tvdbId}" data-type="${r.type}">
            <div class="search-poster">${poster}</div>
            <div class="search-info">
                <div class="search-title">${r.title}</div>
                <div class="search-meta">
                    <span class="search-year">${r.year || ''}</span>
                    <span class="search-badge">${badge}</span>
                </div>
                ${overview}
                <div class="search-detail">${raw(detailChips)}</div>
            </div>
            <div class="search-actions">${actionBtn}</div>
        </div>`.str;
}

function renderResults(results, collapsed) {
    if (!results.length) {
        searchResults.innerHTML = '<div class="no-items">No results found</div>';
        searchResults.className = 'search-results visible';
        return;
    }
    let items = results.slice(0, 10);
    // When collapsing, move the locked result to the front so it's the visible card.
    if (collapsed && lockedResult) {
        const lockedIdx = items.findIndex(r => r.tmdbId === lockedResult.tmdbId && r.tvdbId === lockedResult.tvdbId && r.type === lockedResult.type);
        if (lockedIdx > 0) {
            const [locked] = items.splice(lockedIdx, 1);
            items.unshift(locked);
        }
    }
    let markup = items.map(r => renderResultCard(r)).join('');
    if (collapsed && items.length > 1) {
        markup += '<div class="search-show-more" data-action="expand-results">Show <span class="count">' + (items.length - 1) + '</span> more result' + (items.length > 2 ? 's' : '') + '</div>';
    }
    searchResults.innerHTML = markup;
    searchResults.className = collapsed ? 'search-results collapsed' : 'search-results visible';
    // Re-apply expanded class to the locked result after re-render.
    if (!collapsed && lockedResult) {
        for (const card of searchResults.querySelectorAll('.search-card')) {
            if (parseInt(card.dataset.tmdb, 10) === lockedResult.tmdbId && parseInt(card.dataset.tvdb, 10) === lockedResult.tvdbId) {
                card.classList.add('expanded');
                break;
            }
        }
    }
}

// ── Public functions ──────────────────────────────────────────────────────

async function doSearch(q) {
    lockedResult = null;
    searchResults.innerHTML = '<div class="search-searching-msg">Searching</div>';
    searchResults.className = 'search-results searching';
    const typeParam = searchType ? '&type=' + searchType : '';
    try {
        const data = await get('/api/pelicula/search?q=' + encodeURIComponent(q) + typeParam);
        lastResults = (data && data.results) || [];
        renderResults(lastResults, false);
    } catch(e) {
        console.warn('[pelicula] search error:', e);
        searchResults.innerHTML = '<div class="no-items">Search unavailable</div>';
        searchResults.className = 'search-results visible';
    }
}

function setFilter(btn) {
    document.querySelectorAll('.filter-btn').forEach(function(b) { b.classList.remove('active'); });
    btn.classList.add('active');
    searchType = btn.dataset.type;
    const q = searchInput.value.trim();
    if (q.length >= 2) doSearch(q);
}

function expandResults() {
    searchResults.className = 'search-results visible';
    if (lastResults.length) renderResults(lastResults, false);
    searchFilters.classList.add('visible');
}

function showMediaDetail(tmdbId, tvdbId, type) {
    const hit = lastResults.find(function(r) { return type === 'movie' ? r.tmdbId === tmdbId : r.tvdbId === tvdbId; });
    if (!hit) return;
    let targetCard = null;
    for (const card of searchResults.querySelectorAll('.search-card')) {
        if (parseInt(card.dataset.tmdb, 10) === tmdbId && parseInt(card.dataset.tvdb, 10) === tvdbId) {
            targetCard = card;
            break;
        }
    }
    if (!targetCard) return;
    const isExpanded = targetCard.classList.contains('expanded');
    searchResults.querySelectorAll('.search-card.expanded').forEach(function(c) { c.classList.remove('expanded'); });
    if (isExpanded) {
        lockedResult = null;
    } else {
        targetCard.classList.add('expanded');
        lockedResult = hit;
    }
}

// addMedia posts the add request and drives btn's loading/success/failure
// states. overrides is optional: {profileId, rootPath} from the "Add with
// options..." modal, plus {seasons} (series only, Phase 2.2) — a non-empty
// int array when present; confirmAddOptions never passes an empty array,
// since the backend rejects [] with 400 and the modal blocks that selection
// client-side first. Omitted/falsy fields preserve the plain-Add behavior
// exactly. Returns true on success, false on failure, so callers
// (confirmAddOptions) can decide what to do with a second, related button.
async function addMedia(type, id, btn, overrides) {
    btn.disabled = true; btn.textContent = '\u2026';
    try {
        const idKey = type === 'movie' ? 'tmdbId' : 'tvdbId';
        const hit = lastResults.find(function(r) { return r[idKey] === id; });
        const body = type === 'movie' ? {type: type, tmdbId: id} : {type: type, tvdbId: id};
        if (hit) { body.title = hit.title; body.year = hit.year || 0; body.poster = hit.poster || ''; }
        if (overrides && overrides.profileId) body.profileId = overrides.profileId;
        if (overrides && overrides.rootPath) body.rootPath = overrides.rootPath;
        if (overrides && overrides.seasons && overrides.seasons.length) body.seasons = overrides.seasons;
        const data = await post('/api/pelicula/search/add', body);
        if (data !== null) {
            if (hit) { hit.added = true; }
            btn.textContent = 'Added'; btn.classList.add('added');
            return true;
        } else {
            // post() resolves to null only on a 401 from /api/pelicula/* \u2014 session expired/not logged in.
            toast('Add failed: not authorized', {error: true});
            btn.textContent = 'Add'; btn.disabled = false;
            return false;
        }
    } catch(e) {
        const msg = (e.body && e.body.error) || e.message || 'Add failed';
        toast(msg, {error: true});
        btn.textContent = 'Add'; btn.disabled = false;
        return false;
    }
}

// ── Season picker ─────────────────────────────────────────────────────────
// Mounts into #add-options-extra for series cards, in both the Manager+
// 'add' mode and the viewer 'request' mode (see the Add-with-options modal
// section below). Rendered fresh from the card's lastResults seasons
// metadata every time the modal opens; a hit with no seasons metadata (or a
// movie) leaves the slot empty — no picker present means "all seasons",
// matching the backend's absent/null default exactly.
const SEASON_CHECKBOX_CLASS = 'season-picker-checkbox';

function renderSeasonPicker(hit) {
    if (!hit || !hit.seasons || !hit.seasons.length) return '';
    const items = hit.seasons.map(function(s) {
        const label = s.seasonNumber === 0 ? 'Specials' : ('Season ' + s.seasonNumber);
        // episodeCount is additive metadata the backend only sends when
        // Sonarr's lookup actually reported it — never fabricated here.
        const epSuffix = s.episodeCount
            ? html` · ${s.episodeCount} episode${s.episodeCount !== 1 ? 's' : ''}`.str
            : '';
        // Default: regular seasons checked, Specials (season 0) unchecked —
        // mirrors Sonarr's own default monitoring behavior.
        const checkedAttr = s.seasonNumber === 0 ? raw('') : raw('checked');
        return html`<label class="season-picker-item">
                <input type="checkbox" class="${SEASON_CHECKBOX_CLASS}" value="${s.seasonNumber}" ${checkedAttr}>
                <span>${label}${raw(epSuffix)}</span>
            </label>`.str;
    }).join('');
    return html`
        <div class="season-picker" data-testid="season-picker">
            <div class="season-picker-label">Seasons</div>
            <div class="season-picker-quickselect">
                <button type="button" class="season-quickselect-btn" data-action="season-select-all" data-testid="season-select-all-btn">All seasons</button>
                <button type="button" class="season-quickselect-btn" data-action="season-select-latest" data-testid="season-select-latest-btn">Latest season</button>
            </div>
            <div class="season-picker-list">${raw(items)}</div>
            <div class="season-picker-hint hidden" data-testid="season-picker-hint">Select at least one season to continue.</div>
        </div>`.str;
}

// readSeasonSelection inspects whatever is currently rendered inside
// #add-options-extra. present=false means no picker was rendered at all (a
// movie, or a series hit with no seasons metadata) — the caller should omit
// the seasons key entirely, same as an unmodified default selection.
// isDefault mirrors renderSeasonPicker's own default (regular seasons
// checked, Specials unchecked) so confirming without touching anything also
// omits the key, keeping that path byte-identical to the pre-season payload.
function readSeasonSelection() {
    const extra = document.getElementById('add-options-extra');
    const checkboxes = extra ? extra.querySelectorAll('.' + SEASON_CHECKBOX_CLASS) : [];
    if (!checkboxes.length) return {present: false, checked: [], isDefault: true};
    const checked = [];
    let isDefault = true;
    checkboxes.forEach(function(cb) {
        const n = parseInt(cb.value, 10);
        if (cb.checked) checked.push(n);
        if (n === 0 ? cb.checked : !cb.checked) isDefault = false;
    });
    checked.sort(function(a, b) { return a - b; });
    return {present: true, checked: checked, isDefault: isDefault};
}

function showSeasonHint() {
    const hint = document.querySelector('#add-options-extra .season-picker-hint');
    if (hint) hint.classList.remove('hidden');
}

function hideSeasonHint() {
    const hint = document.querySelector('#add-options-extra .season-picker-hint');
    if (hint) hint.classList.add('hidden');
}

// ── Add-with-options modal ──────────────────────────────────────────────
// Manager+ affordance next to Add ('add' mode, addOptionsState.mode below):
// override quality profile / target library via GET /api/pelicula/arr-meta
// (radarr or sonarr branch, by card type) and POST /api/pelicula/search/add's
// optional profileId/rootPath fields. The SAME modal is reused by viewers'
// "Request seasons…" affordance ('request' mode, Phase 2.2): the
// #add-options-arr-fields wrapper (index.html) is hidden and arr-meta is
// NEVER fetched in this mode (viewers get 403 on it), and confirming calls
// submitRequest() instead of addMedia(). Both modes share the season picker
// above, mounted into #add-options-extra from the card's lastResults seasons
// metadata. Reuses addMedia's/submitRequest's toast-on-failure and
// button-state handling — see both.
let addOptionsState = null; // {type, id, addBtn, optionsBtn, mode} while the modal is open

function fillOptionsSelect(select, items, valueKey, labelKey) {
    const defaultOpt = html`<option value="">Default</option>`.str;
    if (!select) return;
    if (!items || !items.length) { select.innerHTML = defaultOpt; return; }
    const opts = items.map(function(item) {
        return html`<option value="${item[valueKey]}">${item[labelKey]}</option>`.str;
    }).join('');
    select.innerHTML = defaultOpt + opts;
}

async function openAddOptionsModal(type, id, addBtn, optionsBtn, mode) {
    mode = mode || 'add';
    const idKey = type === 'movie' ? 'tmdbId' : 'tvdbId';
    const hit = lastResults.find(function(r) { return r[idKey] === id; });
    addOptionsState = {type: type, id: id, addBtn: addBtn, optionsBtn: optionsBtn, mode: mode};

    const nameEl = document.getElementById('add-options-name');
    if (nameEl) nameEl.textContent = hit ? (hit.title + (hit.year ? ' (' + hit.year + ')' : '')) : '';

    const confirmBtn = document.getElementById('add-options-confirm-btn');
    if (confirmBtn) confirmBtn.textContent = mode === 'request' ? 'Request' : 'Add';

    const arrFields = document.getElementById('add-options-arr-fields');
    if (arrFields) arrFields.classList.toggle('hidden', mode === 'request');

    // Season picker: series only, rendered fresh from lastResults metadata.
    // Always clear the slot first — state must never leak between cards.
    const extra = document.getElementById('add-options-extra');
    if (extra) extra.innerHTML = type === 'series' ? renderSeasonPicker(hit) : '';

    document.getElementById('add-options-modal').classList.remove('hidden');

    if (mode === 'request') {
        // Viewers get 403 on /api/pelicula/arr-meta — never issue the call.
        return;
    }

    const profileSelect = document.getElementById('add-options-profile');
    const rootSelect = document.getElementById('add-options-root');
    if (profileSelect) profileSelect.innerHTML = html`<option value="">Loading…</option>`.str;
    if (rootSelect) rootSelect.innerHTML = html`<option value="">Loading…</option>`.str;

    try {
        const meta = await get('/api/pelicula/arr-meta');
        const arrMeta = (meta && (type === 'movie' ? meta.radarr : meta.sonarr)) || {};
        fillOptionsSelect(profileSelect, arrMeta.qualityProfiles, 'id', 'name');
        // Target Library options come from arrMeta.libraries (registered Pelicula
        // libraries for this arr), NOT rootFolders (the *arr's own root-folder
        // list) — the backend validates rootPath against registered libraries
        // (search/handler.go's rootPathValid), and the two sources can diverge
        // on custom-library setups. See docs/API.md's search/add Notes.
        fillOptionsSelect(rootSelect, arrMeta.libraries, 'path', 'name');
    } catch (e) {
        console.warn('[pelicula] arr-meta error:', e);
        if (profileSelect) profileSelect.innerHTML = html`<option value="">Default</option>`.str;
        if (rootSelect) rootSelect.innerHTML = html`<option value="">Default</option>`.str;
    }
}

function closeAddOptionsModal() {
    document.getElementById('add-options-modal').classList.add('hidden');
    // Always clear the slot on close too — state must never leak between cards.
    const extra = document.getElementById('add-options-extra');
    if (extra) extra.innerHTML = '';
    const confirmBtn = document.getElementById('add-options-confirm-btn');
    if (confirmBtn) confirmBtn.textContent = 'Add';
    const arrFields = document.getElementById('add-options-arr-fields');
    if (arrFields) arrFields.classList.remove('hidden');
    addOptionsState = null; // mode fully reset — the next open() sets it explicitly
}

async function confirmAddOptions() {
    if (!addOptionsState) return;
    const {type, id, addBtn, optionsBtn, mode} = addOptionsState;

    // Read the season selection before closing — closeAddOptionsModal clears
    // #add-options-extra.
    const sel = readSeasonSelection();
    if (sel.present && sel.checked.length === 0) {
        // Nothing checked: the backend rejects an explicit [] with 400, and
        // there is no "monitor nothing" add/request — keep the modal open
        // with an inline hint instead of sending it.
        showSeasonHint();
        return;
    }
    // Default selection (all regular seasons, no Specials) or no picker at
    // all → omit the seasons key so the payload stays byte-identical to the
    // pre-season-support shape.
    const seasons = (!sel.present || sel.isDefault) ? undefined : sel.checked;

    if (mode === 'request') {
        const idKey = type === 'movie' ? 'tmdbId' : 'tvdbId';
        const hit = lastResults.find(function(r) { return r[idKey] === id; });
        closeAddOptionsModal();
        if (optionsBtn) optionsBtn.disabled = true;
        const ok = await submitRequest(
            type,
            type === 'movie' ? id : 0,
            type === 'series' ? id : 0,
            hit ? hit.title : '',
            hit ? (hit.year || 0) : 0,
            hit ? (hit.poster || '') : '',
            addBtn,
            seasons
        );
        if (optionsBtn) {
            if (ok) optionsBtn.style.display = 'none'; // card is now requested — matches renderResultCard's !added gate
            else optionsBtn.disabled = false;           // failed — submitRequest already reset addBtn; let this be retried too
        }
        return;
    }

    const profileVal = document.getElementById('add-options-profile').value;
    const rootVal = document.getElementById('add-options-root').value;
    closeAddOptionsModal();

    if (optionsBtn) optionsBtn.disabled = true;
    const overrides = {
        profileId: profileVal ? parseInt(profileVal, 10) : 0,
        rootPath: rootVal || ''
    };
    if (seasons !== undefined) overrides.seasons = seasons;
    const ok = await addMedia(type, id, addBtn, overrides);
    if (optionsBtn) {
        if (ok) optionsBtn.style.display = 'none'; // card is now added — matches renderResultCard's !added gate
        else optionsBtn.disabled = false;           // failed — addMedia already reset addBtn; let this be retried too
    }
}

// submitRequest posts a viewer's media request and drives btn's
// loading/success/failure states — same button-state contract as addMedia.
// seasons (series only, Phase 2.2) is an optional non-empty int array from
// the "Request seasons…" modal; the one-click Request button on a card omits
// it (default: all seasons), and it's never sent empty (mirrors addMedia's
// seasons contract).
async function submitRequest(type, tmdbId, tvdbId, title, year, poster, btn, seasons) {
    try {
        const body = {type: type, title: title, year: year, poster: poster};
        if (type === 'movie') body.tmdb_id = tmdbId;
        else body.tvdb_id = tvdbId;
        if (seasons && seasons.length) body.seasons = seasons;
        const data = await post('/api/pelicula/requests', body);
        if (!data) {
            toast('Request failed: not authorized', {error: true});
            if (btn) { btn.textContent = 'Request'; btn.disabled = false; }
            return false;
        }
        if (btn) { btn.textContent = 'Requested'; btn.disabled = true; }
        if (window._users_setRequestsLoaded) window._users_setRequestsLoaded(false);
        if (window.loadRequests) await window.loadRequests();
        const requestsSection = document.getElementById('requests-section');
        if (requestsSection) requestsSection.scrollIntoView({behavior: 'smooth'});
        return true;
    } catch (e) {
        const reason = e.status === 403 ? 'not authorized'
            : (e.body && e.body.error) || e.message || 'Network error';
        toast('Request failed: ' + reason, {error: true});
        if (btn) { btn.textContent = 'Request'; btn.disabled = false; }
        return false;
    }
}

// ── Component registration ────────────────────────────────────────────────
component('search', function (el, storeProxy) {
    function init() {
        searchInput   = document.getElementById('search-input');
        searchResults = document.getElementById('search-results');
        searchFilters = document.getElementById('search-filters');

        // Clear any stale localStorage added-cache from older versions
        localStorage.removeItem('peliculaAdded');

        // Input: debounce → doSearch
        searchInput.addEventListener('input', function() {
            clearTimeout(searchTimeout);
            const q = searchInput.value.trim();
            if (q.length < 2) {
                searchResults.className = 'search-results'; searchResults.innerHTML = '';
                searchFilters.classList.remove('visible');
                lastResults = [];
                lockedResult = null;
                return;
            }
            searchFilters.classList.add('visible');
            searchTimeout = setTimeout(function() { doSearch(q); }, 600);
        });

        // Escape blurs the search input (hides results without clearing query)
        searchInput.addEventListener('keydown', function(e) {
            if (e.key === 'Escape') searchInput.blur();
        });

        // Expand results when focusing search input
        searchInput.addEventListener('focus', function() {
            if (searchInput.value.trim().length >= 2 && lastResults.length) {
                renderResults(lastResults, false);
                searchFilters.classList.add('visible');
            }
        });

        // Collapse on click-away
        document.addEventListener('click', function(e) {
            // Don't collapse while the add-options modal is open — it lives
            // outside .search-box/.search-results, so clicks inside it (e.g.
            // the quality-profile select) would otherwise register as
            // "away" and collapse the results behind it.
            if (document.querySelector('.modal-overlay:not(.hidden)')) return;
            if (!e.target.closest('.search-box') && !e.target.closest('.search-results')) {
                if (searchResults.classList.contains('visible') && lastResults.length > 1) {
                    renderResults(lastResults, true);
                    searchFilters.classList.remove('visible');
                } else if (searchResults.classList.contains('visible')) {
                    searchResults.className = 'search-results collapsed';
                }
            }
        });

        // Collapse on scroll
        let scrollTick = false;
        window.addEventListener('scroll', function() {
            if (scrollTick) return;
            scrollTick = true;
            requestAnimationFrame(function() {
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
    }

    return {
        render: function() {},  // no template rendering — operates on existing DOM
        loadOnce: init,
    };
});

// ── Event delegation ──────────────────────────────────────────────────────

// Filter buttons
document.getElementById('search-filters').addEventListener('click', e => {
    const btn = e.target.closest('.filter-btn');
    if (btn) setFilter(btn);
});

// Search results: cards and action buttons
document.getElementById('search-results').addEventListener('click', e => {
    const el = e.target.closest('[data-action]');
    if (!el) return;
    const action = el.dataset.action;
    if (action === 'show-detail') {
        showMediaDetail(parseInt(el.dataset.tmdb, 10), parseInt(el.dataset.tvdb, 10), el.dataset.type);
    } else if (action === 'add-media') {
        addMedia(el.dataset.type, el.dataset.type === 'movie' ? parseInt(el.dataset.tmdb, 10) : parseInt(el.dataset.tvdb, 10), el);
    } else if (action === 'add-media-options') {
        const card = el.closest('.search-card');
        const addBtn = card && card.querySelector('.search-add');
        if (addBtn) {
            openAddOptionsModal(el.dataset.type, el.dataset.type === 'movie' ? parseInt(el.dataset.tmdb, 10) : parseInt(el.dataset.tvdb, 10), addBtn, el);
        }
    } else if (action === 'submit-request') {
        el.disabled = true; el.textContent = '…';
        submitRequest(el.dataset.type, parseInt(el.dataset.tmdb, 10), parseInt(el.dataset.tvdb, 10), el.dataset.title, parseInt(el.dataset.year, 10), el.dataset.poster, el);
    } else if (action === 'request-options') {
        // Viewer "Request seasons…" — reuses #add-options-modal in 'request'
        // mode (see openAddOptionsModal). The card's one-click Request button
        // is passed through so submitRequest can flip it to "Requested" too,
        // keeping both affordances on the card in sync.
        const card = el.closest('.search-card');
        const requestBtn = card && card.querySelector('.search-request');
        if (requestBtn) {
            openAddOptionsModal(el.dataset.type, el.dataset.type === 'movie' ? parseInt(el.dataset.tmdb, 10) : parseInt(el.dataset.tvdb, 10), requestBtn, el, 'request');
        }
    } else if (action === 'expand-results') {
        expandResults();
    }
});

// Add-with-options modal buttons
document.getElementById('add-options-cancel-btn').addEventListener('click', closeAddOptionsModal);
document.getElementById('add-options-confirm-btn').addEventListener('click', confirmAddOptions);

// Season picker quick-selects, delegated on the static #add-options-extra
// slot (its innerHTML is replaced on every modal open/close — see
// openAddOptionsModal/closeAddOptionsModal — so this listener is wired once
// here rather than re-bound per render).
document.getElementById('add-options-extra').addEventListener('click', e => {
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    const extra = document.getElementById('add-options-extra');
    const checkboxes = extra.querySelectorAll('.' + SEASON_CHECKBOX_CLASS);
    if (btn.dataset.action === 'season-select-all') {
        checkboxes.forEach(cb => { cb.checked = true; });
        hideSeasonHint();
    } else if (btn.dataset.action === 'season-select-latest') {
        let latest = null;
        checkboxes.forEach(cb => {
            const n = parseInt(cb.value, 10);
            if (n !== 0 && (latest === null || n > latest)) latest = n;
        });
        checkboxes.forEach(cb => {
            const n = parseInt(cb.value, 10);
            // No regular season exists (Specials-only edge case) — fall back
            // to checking everything rather than leaving nothing selected.
            cb.checked = latest === null ? true : (n === latest);
        });
        hideSeasonHint();
    }
});

// Hide the "select at least one season" hint as soon as the visitor checks
// anything by hand, so it doesn't linger after a failed confirm attempt.
document.getElementById('add-options-extra').addEventListener('change', e => {
    if (e.target.classList.contains(SEASON_CHECKBOX_CLASS) && e.target.checked) hideSeasonHint();
});

