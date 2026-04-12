// nginx/pipeline.js
// Pipeline board component — registered with PeliculaFW; mounted by dashboard.js.
// Depends on: framework.js (PeliculaFW), dashboard.js (tfetch, store, formatSpeed, formatETA,
//   formatSize, dlPauseFromBtn, dlCancelFromBtn, openBlocklistFromBtn,
//   retryFromBtn, cancelJobFromBtn, resubFromBtn, openJobDrawer).

'use strict';

(function () {
    const { component, html, raw, createPoller } = PeliculaFW;

    // ── Module-level state ────────────────────────────────────────────────────

    const PL_INTERVAL = 30;
    // Poller is initialised lazily in loadOnce (needs checkPipeline defined first)
    let plPoller = null;

    // Event log state
    let _eventLogLoaded = false;
    let _eventPage = 1;
    let _eventFilter = '';

    // ── Lane config ───────────────────────────────────────────────────────────

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

    // ── Pipeline fetch + render ───────────────────────────────────────────────

    async function checkPipeline() {
        try {
            const res = await tfetch('/api/pelicula/pipeline');
            if (!res.ok) throw new Error();
            const data = await res.json();
            renderPipeline(data);
            // Update VPN sidebar speed stats
            const s = data.stats || {};
            setText('s-dl', formatSpeed(s.dl_speed || 0));
            setText('s-ul', formatSpeed(s.up_speed || 0));
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

        // FLIP First: snapshot card positions before DOM changes
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
                cardsEl.innerHTML = '<div class="pl-empty">\u2014</div>';
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

        // FLIP Last+Invert+Play: animate cards that moved
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
            speedText = html`${item.detail}`.str;
        }

        // Left-side meta: pct + error snippet
        const metaLeft = html`${pct}%${item.error ? raw(' \u2014 ' + html`${item.error.substring(0, 80)}`.str) : ''}`.str;

        const badge = LANE_BADGE[item.lane] || '';
        let subsBadge = '';
        if (item.stage === 'await_subs') {
            const waiting = (item.missing_subs || []).filter(l => !(item.subs_acquired || []).includes(l));
            if (waiting.length) {
                subsBadge = html`<span class="proc-badge proc-info" title="Waiting for Bazarr to deliver subtitles">Acquiring: ${waiting.join(', ')}</span>`.str;
            }
        } else if (item.subs_acquired && item.subs_acquired.length) {
            subsBadge = html`<span class="proc-badge proc-ok" title="Subtitles acquired by Bazarr">Subs: ${item.subs_acquired.join(', ')}</span>`.str;
        } else if (item.missing_subs && item.missing_subs.length) {
            subsBadge = html`<span class="proc-badge proc-warn" title="Bazarr will fetch these">Missing subs: ${item.missing_subs.join(', ')}</span>`.str;
        }

        const role = document.body.dataset.role || store.get('role');
        const canAdmin = role === 'admin';
        const canManage = role === 'manager' || role === 'admin';
        const actions = item.actions || [];
        const src = item.source || {};
        const qbtHash = src.qbt_hash || '';
        const arrType = src.arr_type || '';
        const jobId = src.job_id || '';

        let actionBtns = '';
        if (actions.includes('pause') && canManage) {
            actionBtns += isPaused
                ? html`<button class="dl-btn resume" title="Resume" data-hash="${qbtHash}" onclick="dlPauseFromBtn(this,false)">&#9654;</button>`.str
                : html`<button class="dl-btn pause" title="Pause" data-hash="${qbtHash}" onclick="dlPauseFromBtn(this,true)">&#9646;&#9646;</button>`.str;
        }
        if (actions.includes('cancel') && canAdmin) {
            actionBtns += html`<button class="dl-btn cancel" title="Cancel" data-hash="${qbtHash}" data-category="${arrType}" data-name="${fullTitle}" onclick="dlCancelFromBtn(this,false)">&#10005;</button>`.str;
        }
        if (actions.includes('blocklist') && canAdmin) {
            actionBtns += html`<button class="dl-btn blocklist" title="Remove &amp; blocklist" data-hash="${qbtHash}" data-category="${arrType}" data-name="${fullTitle}" onclick="openBlocklistFromBtn(this)">&#8856;</button>`.str;
        }
        if (actions.includes('retry') && canAdmin) {
            actionBtns += html`<button class="dl-btn resume" title="Retry" data-job-id="${jobId}" onclick="retryFromBtn(this)">&#8635;</button>`.str;
        }
        if (actions.includes('cancel_job') && canAdmin) {
            actionBtns += html`<button class="dl-btn cancel" title="Cancel job" data-job-id="${jobId}" onclick="cancelJobFromBtn(this)">&#10005;</button>`.str;
        }
        if (actions.includes('view_log') && src.job_id) {
            actionBtns += html`<button class="dl-btn" onclick="openJobDrawer('${jobId}')" title="View details" style="font-size:0.7rem;padding:0.2rem 0.4rem">&#9654;</button>`.str;
        }
        if (actions.includes('dismiss') && canAdmin) {
            actionBtns += html`<button class="dl-btn" title="Dismiss" data-job-id="${jobId}" onclick="dismissJobFromBtn(this)" style="color:#555">&#10006;</button>`.str;
        }

        // Validation checks for failed items
        let checksHTML = '';
        if (isFailed && item.checks) {
            const c = item.checks;
            checksHTML = html`<div class="proc-check-list">${raw(
                [['integrity', c.integrity], ['duration', c.duration], ['sample', c.sample]].map(function(pair) {
                    const v = pair[1]; if (!v) return '';
                    const cls = ['pass', 'fail', 'warn'].includes(v) ? v : 'skip';
                    return html`<span class="proc-check proc-check-${cls}">${pair[0]}: ${v}</span>`.str;
                }).join('')
            )}</div>`.str;
        }

        const cardClass = 'download-item' + (isFailed ? ' pl-card-failed' : isDone ? ' pl-card-done' : '');
        const yearSpan = year ? html`<span class="pl-year">${year}</span>`.str : '';

        return html`<div class="${cardClass}" data-key="${item.key}" data-lane="${item.lane}">
            <div class="download-header">
            <div class="download-name" onclick="this.classList.toggle('expanded')" title="${fullTitle}">${title}${raw(yearSpan)}</div>
            <div class="download-actions">${raw(badge)}${raw(subsBadge)}${raw(actionBtns)}</div>
            </div>
            <div class="download-bar-bg"><div class="download-bar ${barClass}" style="width:${pct}%"></div></div>
            <div class="download-meta"><span>${raw(metaLeft)}</span><span>${raw(speedText)}</span></div>
            ${raw(checksHTML)}
        </div>`.str;
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

    // ── Event log ─────────────────────────────────────────────────────────────

    function onEventLogToggle(details) {
        if (details.open && !_eventLogLoaded) {
            _eventLogLoaded = true;
            loadEventLog(1, '');
        }
    }

    function setEventFilter(btn, filter) {
        document.querySelectorAll('.pl-chip').forEach(c => c.classList.remove('pl-chip-active'));
        btn.classList.add('pl-chip-active');
        _eventFilter = filter;
        _eventPage = 1;
        loadEventLog(_eventPage, _eventFilter);
    }

    async function loadEventLog(page, filter) {
        const list = document.getElementById('pl-event-list');
        const pager = document.getElementById('pl-event-pager');
        if (!list) return;
        list.innerHTML = '<div style="color:var(--muted);font-size:0.78rem;padding:0.5rem 0">Loading\u2026</div>';
        try {
            let url = '/api/procula/events?page=' + page + '&page_size=20';
            if (filter) url += '&filter=' + encodeURIComponent(filter);
            const res = await fetch(url);
            if (!res.ok) throw new Error();
            const data = await res.json();
            const events = Array.isArray(data) ? data : (data.events || []);
            const total = data.total || events.length;
            if (!events.length) {
                list.innerHTML = '<div style="color:var(--muted);font-size:0.78rem;padding:0.5rem 0">No events found.</div>';
                if (pager) pager.innerHTML = '';
                return;
            }
            const iconMap = {validate: '\u2713', transcode: '\u25b6', catalog: '\u2605', action: '\u25cf', error: '\u26a0'};
            list.innerHTML = events.map(ev => {
                const icon = iconMap[ev.type] || '\u25cf';
                const time = new Date(ev.at || ev.timestamp).toLocaleString();
                return html`<div class="pl-event-item"><span class="pl-event-icon">${icon}</span><div class="pl-event-body"><div class="pl-event-title">${ev.message || ev.event || ev.type}</div><div class="pl-event-meta">${ev.title || ''}${ev.title ? ' \u00b7 ' : ''}${time}</div></div></div>`.str;
            }).join('');
            // Pager
            if (pager) {
                const pages = Math.ceil(total / 20);
                let pgHtml = '';
                if (page > 1) pgHtml += html`<button onclick="loadEventLog(${page-1},'${_eventFilter}')">&#8592; Prev</button>`.str;
                pgHtml += html`<span style="font-size:0.68rem;color:var(--muted);padding:0.2rem 0.4rem">${page} / ${pages||1}</span>`.str;
                if (page < pages) pgHtml += html`<button onclick="loadEventLog(${page+1},'${_eventFilter}')">Next &#8594;</button>`.str;
                pager.innerHTML = pgHtml;
            }
            _eventPage = page;
        } catch (e) {
            list.innerHTML = '<div style="color:var(--muted);font-size:0.78rem;padding:0.5rem 0">Failed to load events.</div>';
        }
    }

    // ── Component registration ────────────────────────────────────────────────

    component('pipeline', function(el, storeProxy, props) {
        return {
            render: function() {},  // operates on existing DOM
            loadOnce: function() {
                checkPipeline();
                plPoller = createPoller(checkPipeline, PL_INTERVAL);
                setTimeout(function() { plPoller.start(); }, 1200);
                window.plPoller = plPoller;
            },
        };
    });

    // ── Window exports ────────────────────────────────────────────────────────
    // checkPipeline is called by dashboard.js refresh() and by dlPause/dlCancel callbacks.
    window.checkPipeline         = checkPipeline;
    window.renderPipeline        = renderPipeline;
    window.manualRefreshPipeline = function() {
        if (plPoller) plPoller.refresh(); else checkPipeline();
    };
    window.dismissJobFromBtn     = dismissJobFromBtn;
    window.onEventLogToggle      = onEventLogToggle;
    window.setEventFilter        = setEventFilter;
    window.loadEventLog          = loadEventLog;

}());
