// nginx/search.js
// Search component — registered with PeliculaFW; mounted by dashboard.js.
// Depends on: framework.js (PeliculaFW), dashboard.js (tfetch, store, requestsLoaded, loadRequests).

'use strict';

(function () {
    const { component, html, raw } = PeliculaFW;

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
        const isManager = store.get('role') === 'manager' || store.get('role') === 'admin';
        const actionBtn = isManager
            ? html`<button
                    class="${added ? 'search-add added' : 'search-add'}"
                    ${added ? raw('disabled') : raw('')}
                    data-type="${r.type}"
                    data-tmdb="${tmdbId}"
                    data-tvdb="${tvdbId}"
                    data-testid="search-add-btn"
                    onclick="event.stopPropagation();addMedia(this.dataset.type,this.dataset.type==='movie'?parseInt(this.dataset.tmdb):parseInt(this.dataset.tvdb),this)"
                  >${added ? 'Added' : 'Add'}</button>`
            : html`<button
                    class="search-request"
                    data-type="${r.type}"
                    data-tmdb="${tmdbId}"
                    data-tvdb="${tvdbId}"
                    data-title="${r.title}"
                    data-year="${r.year || 0}"
                    data-poster="${r.poster || ''}"
                    data-testid="search-request-btn"
                    onclick="event.stopPropagation();submitRequest(this.dataset.type,parseInt(this.dataset.tmdb),parseInt(this.dataset.tvdb),this.dataset.title,parseInt(this.dataset.year),this.dataset.poster);this.textContent='Requested';this.disabled=true"
                  >Request</button>`;
        const detailChips = buildDetailChips(r);
        const overview = r.overview ? html`<div class="search-overview">${r.overview}</div>` : raw('');
        return html`
            <div class="search-card" data-testid="search-result-card" data-tmdb="${tmdbId}" data-tvdb="${tvdbId}" data-type="${r.type}"
                 onclick="showMediaDetail(${tmdbId},${tvdbId},'${r.type}')">
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
            markup += '<div class="search-show-more" onclick="expandResults(); event.stopPropagation();">Show <span class="count">' + (items.length - 1) + '</span> more result' + (items.length > 2 ? 's' : '') + '</div>';
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

    // ── Public functions (exposed on window for onclick handlers) ─────────────

    function doSearch(q) {
        lockedResult = null;
        searchResults.innerHTML = '<div class="search-searching-msg">Searching</div>';
        searchResults.className = 'search-results searching';
        const typeParam = searchType ? '&type=' + searchType : '';
        return fetch('/api/pelicula/search?q=' + encodeURIComponent(q) + typeParam)
            .then(function(res) { return res.json(); })
            .then(function(data) {
                lastResults = data.results || [];
                renderResults(lastResults, false);
            })
            .catch(function(e) {
                console.warn('[pelicula] search error:', e);
                searchResults.innerHTML = '<div class="no-items">Search unavailable</div>';
                searchResults.className = 'search-results visible';
            });
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

    async function addMedia(type, id, btn) {
        btn.disabled = true; btn.textContent = '\u2026';
        try {
            const idKey = type === 'movie' ? 'tmdbId' : 'tvdbId';
            const hit = lastResults.find(function(r) { return r[idKey] === id; });
            const body = type === 'movie' ? {type: type, tmdbId: id} : {type: type, tvdbId: id};
            if (hit) { body.title = hit.title; body.year = hit.year || 0; body.poster = hit.poster || ''; }
            const res = await fetch('/api/pelicula/search/add', { method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body) });
            if (res.ok) {
                if (hit) { hit.added = true; }
                btn.textContent = 'Added'; btn.classList.add('added');
            } else { btn.textContent = 'Error'; setTimeout(function() { btn.textContent = 'Add'; btn.disabled = false; }, 2000); }
        } catch(e) { btn.textContent = 'Error'; setTimeout(function() { btn.textContent = 'Add'; btn.disabled = false; }, 2000); }
    }

    async function submitRequest(type, tmdbId, tvdbId, title, year, poster) {
        try {
            const body = {type: type, title: title, year: year, poster: poster};
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
            requestsLoaded = false; // force reload (global in dashboard.js)
            await loadRequests();
            const requestsSection = document.getElementById('requests-section');
            if (requestsSection) requestsSection.scrollIntoView({behavior: 'smooth'});
        } catch (e) { alert('Network error'); }
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

    // ── Window exports (for onclick handlers in index.html) ───────────────────
    window.doSearch        = doSearch;
    window.addMedia        = addMedia;
    window.submitRequest   = submitRequest;
    window.setFilter       = setFilter;
    window.expandResults   = expandResults;
    window.showMediaDetail = showMediaDetail;
}());
