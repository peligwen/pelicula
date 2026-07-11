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
    const actionBtn = isManager
        ? raw(addBtn.str + addOptionsBtn.str)
        : html`<button
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
// states. overrides is optional — {profileId, rootPath} from the "Add with
// options…" modal; omitted (or falsy fields) preserves the plain-Add
// behavior exactly. Returns true on success, false on failure, so callers
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

// ── Add-with-options modal ──────────────────────────────────────────────
// Manager+ only affordance next to Add: lets the operator override quality
// profile / target library before the add, via GET /api/pelicula/arr-meta
// (radarr or sonarr branch, by card type) and POST /api/pelicula/search/add's
// optional profileId/rootPath fields. Reuses addMedia's toast-on-failure and
// button-state handling — see addMedia above.
//
// #add-options-extra (index.html) is a deliberately empty extension slot: a
// later phase mounts a series season picker there. This module intentionally
// does not touch it beyond leaving it alone on open/close.
let addOptionsState = null; // {type, id, addBtn, optionsBtn} while the modal is open

function fillOptionsSelect(select, items, valueKey, labelKey) {
    const defaultOpt = html`<option value="">Default</option>`.str;
    if (!select) return;
    if (!items || !items.length) { select.innerHTML = defaultOpt; return; }
    const opts = items.map(function(item) {
        return html`<option value="${item[valueKey]}">${item[labelKey]}</option>`.str;
    }).join('');
    select.innerHTML = defaultOpt + opts;
}

async function openAddOptionsModal(type, id, addBtn, optionsBtn) {
    const idKey = type === 'movie' ? 'tmdbId' : 'tvdbId';
    const hit = lastResults.find(function(r) { return r[idKey] === id; });
    addOptionsState = {type: type, id: id, addBtn: addBtn, optionsBtn: optionsBtn};

    const nameEl = document.getElementById('add-options-name');
    if (nameEl) nameEl.textContent = hit ? (hit.title + (hit.year ? ' (' + hit.year + ')' : '')) : '';

    const profileSelect = document.getElementById('add-options-profile');
    const rootSelect = document.getElementById('add-options-root');
    if (profileSelect) profileSelect.innerHTML = html`<option value="">Loading\u2026</option>`.str;
    if (rootSelect) rootSelect.innerHTML = html`<option value="">Loading\u2026</option>`.str;
    document.getElementById('add-options-modal').classList.remove('hidden');

    try {
        const meta = await get('/api/pelicula/arr-meta');
        const arrMeta = (meta && (type === 'movie' ? meta.radarr : meta.sonarr)) || {};
        fillOptionsSelect(profileSelect, arrMeta.qualityProfiles, 'id', 'name');
        fillOptionsSelect(rootSelect, arrMeta.rootFolders, 'path', 'path');
    } catch (e) {
        console.warn('[pelicula] arr-meta error:', e);
        if (profileSelect) profileSelect.innerHTML = html`<option value="">Default</option>`.str;
        if (rootSelect) rootSelect.innerHTML = html`<option value="">Default</option>`.str;
    }
}

function closeAddOptionsModal() {
    document.getElementById('add-options-modal').classList.add('hidden');
    addOptionsState = null;
}

async function confirmAddOptions() {
    if (!addOptionsState) return;
    const {type, id, addBtn, optionsBtn} = addOptionsState;
    const profileVal = document.getElementById('add-options-profile').value;
    const rootVal = document.getElementById('add-options-root').value;
    closeAddOptionsModal();

    if (optionsBtn) optionsBtn.disabled = true;
    const ok = await addMedia(type, id, addBtn, {
        profileId: profileVal ? parseInt(profileVal, 10) : 0,
        rootPath: rootVal || ''
    });
    if (optionsBtn) {
        if (ok) optionsBtn.style.display = 'none'; // card is now added — matches renderResultCard's !added gate
        else optionsBtn.disabled = false;           // failed — addMedia already reset addBtn; let this be retried too
    }
}

async function submitRequest(type, tmdbId, tvdbId, title, year, poster, btn) {
    try {
        const body = {type: type, title: title, year: year, poster: poster};
        if (type === 'movie') body.tmdb_id = tmdbId;
        else body.tvdb_id = tvdbId;
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
    } else if (action === 'expand-results') {
        expandResults();
    }
});

// Add-with-options modal buttons
document.getElementById('add-options-cancel-btn').addEventListener('click', closeAddOptionsModal);
document.getElementById('add-options-confirm-btn').addEventListener('click', confirmAddOptions);

