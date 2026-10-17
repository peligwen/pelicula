/* Pelicula dashboard. Vanilla JS, no inline handlers (CSP: script-src 'self').
 * Every API string is run through esc() before it reaches innerHTML.
 * One user: the Jellyfin administrator. There are no roles to check. */
(function () {
  'use strict';

  const { esc, api } = window.Pelicula;
  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

  const TABS = ['search', 'jobs', 'settings'];
  const STATUS_POLL_MS = 10000;
  const JOBS_POLL_MS = 5000;
  const TAB_POLL_MS = 15000;

  const SETTING_META = {
    validation_enabled: ['Validate imports', 'Run ffprobe on every imported file: integrity, sample size and runtime checks.'],
    auto_blocklist: ['Blocklist and re-search failures', 'When validation fails, blocklist the release, delete the bad file and search again.']
  };
  const SETTING_ORDER = ['validation_enabled', 'auto_blocklist'];

  const state = {
    me: null,
    tab: 'search',
    status: null,
    search: { q: '', results: null, loading: false, error: '', busy: {} },
    settings: null,      // { settings: {...}, info: {...} }
    sig: {},             // last painted signature per region (skip identical repaints)
    inflight: {},
    loaded: {},          // regions that already hold real data
    timers: { status: null, tab: null }
  };

  /* ── formatting ─────────────────────────────────────────────────────── */

  function fmtBytes(n) {
    n = Number(n) || 0;
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return (i === 0 || n >= 100 ? Math.round(n) : n.toFixed(1)) + ' ' + units[i];
  }
  const fmtSpeed = (n) => (Number(n) > 0 ? fmtBytes(n) + '/s' : '0 B/s');

  function fmtEta(sec) {
    sec = Number(sec);
    if (!(sec > 0) || sec >= 8640000) return '—';
    if (sec < 60) return Math.round(sec) + 's';
    if (sec < 3600) return Math.round(sec / 60) + 'm';
    if (sec < 86400) return Math.floor(sec / 3600) + 'h ' + Math.round((sec % 3600) / 60) + 'm';
    return Math.floor(sec / 86400) + 'd ' + Math.round((sec % 86400) / 3600) + 'h';
  }

  function fmtDuration(sec) {
    sec = Math.round(Number(sec) || 0);
    if (sec <= 0) return '';
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    return h ? h + 'h ' + m + 'm' : m + 'm';
  }

  // Go zero times ("0001-01-01...") and nulls count as "no date".
  function parseDate(v) {
    if (!v) return null;
    const d = new Date(v);
    return isNaN(d.getTime()) || d.getFullYear() < 1971 ? null : d;
  }

  function ago(v) {
    const d = parseDate(v);
    if (!d) return '';
    const s = Math.max(0, Math.round((Date.now() - d.getTime()) / 1000));
    if (s < 45) return 'just now';
    if (s < 3600) return Math.round(s / 60) + 'm ago';
    if (s < 86400) return Math.round(s / 3600) + 'h ago';
    return Math.round(s / 86400) + 'd ago';
  }

  const safeUrl = (u) => (typeof u === 'string' && /^https?:\/\//i.test(u) ? u : '');
  const cap = (s) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : '');
  const typeLabel = (t) => (t === 'series' ? 'Series' : 'Movie');
  const errMsg = (e) => (e && e.message) || 'Something went wrong';
  const empty = (msg) => `<p class="empty">${esc(msg)}</p>`;

  /* ── small UI helpers ───────────────────────────────────────────────── */

  function toast(msg, kind) {
    const el = document.createElement('div');
    el.className = 'toast toast-' + (kind || 'info');
    el.textContent = msg;
    $('#toasts').appendChild(el);
    setTimeout(() => el.remove(), kind === 'error' ? 6000 : 3500);
  }

  // Run an API mutation; toast failures (401 is handled by the login overlay).
  async function attempt(fn, okMsg) {
    try {
      const res = await fn();
      if (okMsg) toast(okMsg, 'ok');
      return { ok: true, data: res };
    } catch (e) {
      if (e.status !== 401) toast(errMsg(e), 'error');
      return { ok: false, error: e };
    }
  }

  // Repaint a region only when its data (or the current minute, for relative times) changed.
  function paint(key, el, data, render) {
    const sig = Math.floor(Date.now() / 60000) + '|' + JSON.stringify(data);
    if (state.sig[key] === sig) return;
    state.sig[key] = sig;
    el.innerHTML = render(data);
  }

  // Poll-safe loader: skips if the previous call is still running, drops results after logout.
  async function load(key, el, fetcher, render) {
    if (state.inflight[key] || !state.me) return;
    state.inflight[key] = true;
    try {
      const data = await fetcher();
      if (!state.me) return;
      state.loaded[key] = true;
      paint(key, el, data, render);
    } catch (e) {
      if (e.status === 401 || !state.me) return;
      if (!state.loaded[key]) {
        state.sig[key] = '';
        el.innerHTML = empty('Could not load this: ' + errMsg(e));
      }
    } finally {
      state.inflight[key] = false;
    }
  }

  // Confirm dialog. Resolves null on cancel, else { checked }.
  function ask(opts) {
    return new Promise((resolve) => {
      const d = $('#dialog');
      $('#dialog-title').textContent = opts.title;
      $('#dialog-msg').textContent = opts.message || '';
      $('#dialog-check-row').hidden = !opts.checkbox;
      $('#dialog-check').checked = !!(opts.checkbox && opts.checkbox.checked);
      $('#dialog-check-label').textContent = opts.checkbox ? opts.checkbox.label : '';
      const ok = $('#dialog-ok');
      ok.textContent = opts.ok || 'OK';
      ok.classList.toggle('btn-danger', !!opts.danger);
      ok.classList.toggle('btn-primary', !opts.danger);
      d.returnValue = '';
      const done = () => {
        d.removeEventListener('close', done);
        resolve(d.returnValue === 'ok' ? { checked: $('#dialog-check').checked } : null);
      };
      d.addEventListener('close', done);
      d.showModal();
    });
  }

  /* ── login / session ────────────────────────────────────────────────── */

  function showLogin(msg) {
    clearInterval(state.timers.status);
    clearInterval(state.timers.tab);
    state.me = null;
    state.status = null;
    state.sig = {};
    state.loaded = {};
    state.search = { q: '', results: null, loading: false, error: '', busy: {} };
    state.settings = null;
    $$('.region').forEach((el) => { el.innerHTML = ''; });
    $('#transfer').textContent = '';
    $('#who').hidden = true;
    $('#app').inert = true;
    $('#login-overlay').hidden = false;
    const err = $('#login-error');
    err.hidden = !msg;
    err.textContent = msg || '';
    $('#login-submit').disabled = false;
    $('#login-username').focus();
  }

  function onLoggedIn() {
    state.sig = {};
    state.loaded = {};
    $('#login-overlay').hidden = true;
    $('#login-error').hidden = true;
    $('#app').inert = false;
    $('#who').hidden = false;
    $('#who-name').textContent = state.me.username;
    renderStatus();
    const wanted = location.hash.slice(1);
    showTab(TABS.includes(wanted) ? wanted : 'search');
    loadStatus();
    state.timers.status = setInterval(() => { if (!document.hidden) loadStatus(); }, STATUS_POLL_MS);
  }

  async function onLoginSubmit(ev) {
    ev.preventDefault();
    const f = ev.target;
    const btn = $('#login-submit');
    btn.disabled = true;
    $('#login-error').hidden = true;
    try {
      state.me = await api('POST', '/api/auth/login',
        { username: f.username.value.trim(), password: f.password.value }, { noAuthHook: true });
      f.password.value = '';
      onLoggedIn();
    } catch (e) {
      const msg = e.status === 401 ? 'Wrong username or password.'
        : e.status === 403 ? 'Only the Jellyfin administrator can use this dashboard.'
        : errMsg(e);
      const err = $('#login-error');
      err.textContent = msg;
      err.hidden = false;
      btn.disabled = false;
      $('#login-password').select();
    }
  }

  async function logout() {
    try { await api('POST', '/api/auth/logout', undefined, { noAuthHook: true }); } catch (e) { /* show login regardless */ }
    showLogin();
  }

  async function boot() {
    api.onUnauthorized = () => { if (state.me || $('#login-overlay').hidden) showLogin(); };
    try {
      state.me = await api('GET', '/api/auth/me', undefined, { noAuthHook: true });
      onLoggedIn();
    } catch (e) {
      showLogin(e.status === 401 ? '' : errMsg(e));
    }
  }

  /* ── status strip ───────────────────────────────────────────────────── */

  async function loadStatus() {
    if (!state.me) return;
    try {
      const s = await api('GET', '/api/status');
      if (!state.me) return;
      state.status = s;
    } catch (e) {
      if (e.status === 401) return;
      state.status = null;
    }
    renderStatus();
  }

  function renderStatus() {
    const el = $('#status-strip');
    const s = state.status;
    if (!s) {
      el.innerHTML = '<span class="muted"><span class="dot"></span>Status unavailable</span>';
      return;
    }
    const svcs = (s.services || []).map((v) => {
      const dot = `<span class="dot ${v.ok ? 'ok' : 'bad'}" title="${v.ok ? 'up' : 'down'}"></span>`;
      const name = v.path ? `<a href="${esc(v.path)}">${esc(v.name)}</a>` : esc(v.name);
      return `<li>${dot}${name}<span class="sr-only"> ${v.ok ? 'up' : 'down'}</span></li>`;
    }).join('');

    const vpn = s.vpn || {};
    let vpnHtml;
    if (!vpn.enabled) {
      vpnHtml = '<span class="muted">VPN off</span>';
    } else {
      const bits = [];
      bits.push(`<span class="kv"><span class="k">IP</span> ${esc(vpn.public_ip || '—')}${vpn.country ? ' (' + esc(vpn.country) + ')' : ''}</span>`);
      bits.push(`<span class="kv"><span class="k">Port</span> ${vpn.forwarded_port > 0 ? esc(vpn.forwarded_port) : '—'}</span>`);
      bits.push(`<span class="kv"><span class="k">Tunnel</span> ${esc(vpn.tunnel || 'unknown')}</span>`);
      vpnHtml = '<span class="vpn"><span class="k">VPN</span> ' + bits.join(' ') + '</span>';
    }

    const parts = [`<ul class="svc-list">${svcs}</ul>`, vpnHtml];
    if (s.wired === false) parts.push('<span class="pill pill-warn">Setting up&hellip;</span>');
    if (s.queued_jobs > 0) parts.push(`<span class="muted">${esc(s.queued_jobs)} queued</span>`);
    if (s.version) parts.push(`<span class="muted ver">v${esc(String(s.version).replace(/^v/, ''))}</span>`);
    el.innerHTML = parts.join('');
  }

  /* ── tabs + polling ─────────────────────────────────────────────────── */

  function showTab(name) {
    if (!TABS.includes(name)) name = 'search';
    state.tab = name;
    TABS.forEach((t) => {
      const sel = t === name;
      const btn = $('#tab-btn-' + t);
      btn.setAttribute('aria-selected', String(sel));
      btn.tabIndex = sel ? 0 : -1;
      $('#panel-' + t).hidden = !sel;
    });
    try { history.replaceState(null, '', location.pathname + '#' + name); } catch (e) { /* ignore */ }
    refreshTab();
    clearInterval(state.timers.tab);
    state.timers.tab = setInterval(() => { if (!document.hidden) refreshTab(); },
      name === 'jobs' ? JOBS_POLL_MS : TAB_POLL_MS);
    if (name === 'search' && !state.search.results) $('#search-q').focus();
  }

  function refreshTab() {
    if (!state.me) return;
    switch (state.tab) {
      case 'search': renderSearch(); break;
      case 'jobs': loadDownloads(); loadJobs(); break;
      case 'settings': loadSettings(); break;
    }
  }

  function onTabKey(ev) {
    if (ev.key !== 'ArrowLeft' && ev.key !== 'ArrowRight') return;
    const tabs = $$('.tab', $('#tablist'));
    const i = tabs.indexOf(document.activeElement);
    if (i < 0) return;
    const next = tabs[(i + (ev.key === 'ArrowRight' ? 1 : tabs.length - 1)) % tabs.length];
    next.focus();
    showTab(next.dataset.tab);
    ev.preventDefault();
  }

  /* ── search ─────────────────────────────────────────────────────────── */

  const resultKey = (r) => r.type + ':' + (r.tmdb_id || 0) + ':' + (r.tvdb_id || 0);

  function resultActions(r, i) {
    const html = [];
    if (r.in_library) {
      html.push('<button type="button" class="btn btn-sm btn-badge" data-testid="result-in-library" disabled>In library</button>');
      if (r.has_file) html.push('<a class="btn btn-primary btn-sm" data-testid="result-watch" href="/jellyfin/" target="_blank" rel="noopener">Watch</a>');
    } else {
      const busy = state.search.busy[resultKey(r)] ? ' disabled aria-busy="true"' : '';
      html.push(`<button type="button" class="btn btn-primary btn-sm" data-testid="result-add" data-action="add" data-i="${i}"${busy}>Add</button>`);
    }
    return html.join('');
  }

  function resultCard(r, i) {
    const poster = safeUrl(r.poster);
    const img = poster
      ? `<img src="${esc(poster)}" alt="" loading="lazy" referrerpolicy="no-referrer">`
      : '<span class="poster-ph">No poster</span>';
    return `<article class="card result" data-testid="result-card" data-title="${esc(r.title)}" data-type="${esc(r.type)}" data-i="${i}">
      <div class="poster">${img}</div>
      <div class="result-body">
        <h3>${esc(r.title)}${r.year ? ` <span class="muted">(${esc(r.year)})</span>` : ''}</h3>
        <div><span class="chip">${typeLabel(r.type)}</span></div>
        <p class="overview">${esc(r.overview || 'No description available.')}</p>
        <div class="actions">${resultActions(r, i)}</div>
      </div>
    </article>`;
  }

  function renderSearch() {
    const s = state.search;
    const el = $('#search-results');
    if (s.loading) { el.innerHTML = empty('Searching…'); return; }
    if (s.error) { el.innerHTML = empty(s.error); return; }
    if (!s.results) { el.innerHTML = empty('Search for a movie or show. Titles you already have are marked.'); return; }
    if (!s.results.length) { el.innerHTML = empty('Nothing found for “' + s.q + '”.'); return; }
    el.innerHTML = '<div class="results">' + s.results.map(resultCard).join('') + '</div>';
  }

  function refreshCard(i) {
    const slot = $(`#search-results .result[data-i="${i}"] .actions`);
    if (slot) slot.innerHTML = resultActions(state.search.results[i], i);
  }

  async function doSearch(ev) {
    ev.preventDefault();
    const q = $('#search-q').value.trim();
    if (!q) return;
    state.search = { q, results: null, loading: true, error: '', busy: {} };
    renderSearch();
    const mine = state.search;
    try {
      const d = await api('GET', '/api/search?q=' + encodeURIComponent(q));
      mine.results = (d && d.results) || [];
    } catch (e) {
      if (e.status === 401) return;
      mine.results = [];
      mine.error = 'Search failed: ' + errMsg(e);
    }
    mine.loading = false;
    if (state.search === mine) renderSearch();
  }

  async function addTitle(i) {
    const mine = state.search;
    const r = mine.results && mine.results[i];
    if (!r) return;
    const key = resultKey(r);
    mine.busy[key] = true;
    refreshCard(i);
    const ids = { type: r.type, tmdb_id: r.tmdb_id || 0, tvdb_id: r.tvdb_id || 0 };
    const res = await attempt(() => api('POST', '/api/search/add', ids), `Added “${r.title}”. Searching for releases.`);
    if (res.ok) { r.in_library = true; r.arr_id = res.data && res.data.arr_id; }
    delete mine.busy[key];
    if (state.search === mine) refreshCard(i);
    loadStatus();
  }

  /* ── jobs: downloads + recent imports ───────────────────────────────── */

  function dlState(st) {
    const s = String(st || '');
    if (/^(paused|stopped)/i.test(s)) return { label: 'Paused', cls: 'idle', paused: true };
    if (/^(downloading|forcedDL)$/i.test(s)) return { label: 'Downloading', cls: 'info' };
    if (/^(uploading|forcedUP|stalledUP)$/i.test(s)) return { label: 'Seeding', cls: 'ok' };
    if (/^stalledDL$/i.test(s)) return { label: 'Stalled', cls: 'warn' };
    if (/^queued/i.test(s)) return { label: 'Queued', cls: 'idle' };
    if (/^metaDL$/i.test(s)) return { label: 'Fetching metadata', cls: 'info' };
    if (/^(checking|allocating|moving)/i.test(s)) return { label: cap(s.replace(/([A-Z])/g, ' $1').toLowerCase()), cls: 'info' };
    if (/error|missing/i.test(s)) return { label: 'Error', cls: 'err' };
    return { label: s || 'Unknown', cls: 'idle' };
  }

  function renderDownloads(d) {
    const t = d.transfer || {};
    $('#transfer').textContent = d.vpn ? `↓ ${fmtSpeed(t.dl_speed)}  ↑ ${fmtSpeed(t.up_speed)}` : '';
    if (!d.vpn) return empty('Downloads need the VPN profile. Add a WireGuard key and run pelicula up to enable qBittorrent.');
    const list = d.downloads || [];
    if (!list.length) return empty('No active downloads.');
    return '<ul class="rows">' + list.map((x) => {
      const st = dlState(x.state);
      const pct = Math.max(0, Math.min(100, Math.round((Number(x.progress) || 0) * 1000) / 10));
      const toggle = st.paused
        ? `<button type="button" class="btn btn-sm" data-testid="download-resume" data-action="dl-resume" data-hash="${esc(x.hash)}">Resume</button>`
        : `<button type="button" class="btn btn-sm" data-testid="download-pause" data-action="dl-pause" data-hash="${esc(x.hash)}">Pause</button>`;
      const remove = `<button type="button" class="btn btn-sm btn-danger-outline" data-testid="download-remove" data-action="dl-remove" data-hash="${esc(x.hash)}" data-name="${esc(x.name)}">Remove</button>`;
      return `<li class="row dl" data-testid="download-row">
        <div class="row-main">
          <div class="row-title ellipsis" title="${esc(x.name)}">${esc(x.name)}</div>
          <div class="bar" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${pct}"><span style="width:${pct}%"></span></div>
          <div class="muted small">${pct}% of ${esc(fmtBytes(x.size))} · ↓ ${esc(fmtSpeed(x.dlspeed))} ↑ ${esc(fmtSpeed(x.upspeed))} · ETA ${esc(fmtEta(x.eta))}${x.category ? ' · ' + esc(x.category) : ''}</div>
        </div>
        <div class="row-side"><span class="pill pill-${st.cls}">${esc(st.label)}</span>${toggle}${remove}</div>
      </li>`;
    }).join('') + '</ul>';
  }

  function loadDownloads() {
    return load('downloads', $('#downloads-list'), () => api('GET', '/api/downloads'), renderDownloads);
  }

  async function downloadAction(hash, verb) {
    const res = await attempt(() => api('POST', `/api/downloads/${encodeURIComponent(hash)}/${verb}`));
    if (res.ok) loadDownloads();
  }

  async function removeDownload(hash, name) {
    const r = await ask({
      title: 'Remove download',
      message: `Remove “${name}” and delete its files?`,
      checkbox: { label: 'Also blocklist this release so it is not grabbed again', checked: false },
      ok: 'Remove',
      danger: true
    });
    if (!r) return;
    const res = await attempt(() => api('DELETE', `/api/downloads/${encodeURIComponent(hash)}?blocklist=${r.checked ? 'true' : 'false'}`), 'Download removed.');
    if (res.ok) loadDownloads();
  }

  function chipCls(v) {
    if (v === 'pass') return 'ok';
    if (v === 'fail') return 'err';
    if (v === 'warn') return 'warn';
    return 'idle';
  }

  function parseResult(j) {
    if (!j.result) return null;
    try {
      const r = JSON.parse(j.result);
      return r && typeof r === 'object' ? r : null;
    } catch (e) {
      return null;
    }
  }

  function jobSummary(r) {
    if (!r) return '';
    if (r.skipped) return '<div class="chips"><span class="chip">Validation skipped</span></div>';
    const chips = [['Integrity', r.integrity], ['Sample', r.sample], ['Duration', r.duration]]
      .filter((c) => c[1])
      .map((c) => `<span class="chip chip-${chipCls(c[1])}">${c[0]}: ${esc(c[1])}</span>`);
    const bits = [];
    if (r.video) bits.push(esc(r.video) + (r.width && r.height ? ` ${esc(r.width)}×${esc(r.height)}` : ''));
    if (Array.isArray(r.audio) && r.audio.length) bits.push('Audio: ' + r.audio.map(esc).join(', '));
    if (Array.isArray(r.subtitles) && r.subtitles.length) bits.push('Subs: ' + r.subtitles.map(esc).join(', '));
    if (r.duration_sec) bits.push(esc(fmtDuration(r.duration_sec)));
    return (chips.length ? `<div class="chips">${chips.join('')}</div>` : '')
      + (bits.length ? `<div class="muted small">${bits.join(' · ')}</div>` : '');
  }

  function renderJobs(list) {
    if (!list.length) return empty('No imports yet. Files show up here once Sonarr or Radarr import them.');
    return '<ul class="rows">' + list.map((j) => {
      const r = parseResult(j);
      const err = j.error || (r && !r.passed && r.reason) || '';
      const when = ago(j.finished_at) || ago(j.created_at);
      const retry = j.status === 'failed'
        ? `<button type="button" class="btn btn-sm" data-testid="job-retry" data-action="retry" data-id="${esc(j.id)}">Retry</button>` : '';
      const cls = { passed: 'ok', failed: 'err', running: 'info', queued: 'idle' }[j.status] || 'idle';
      return `<li class="row" data-testid="job-row" data-status="${esc(j.status)}" data-title="${esc(j.title || '')}">
        <div class="row-main">
          <div class="row-title">${esc(j.title || j.path)} <span class="chip">${esc(cap(j.arr_type))}</span></div>
          <div class="muted small">${esc(when)}${j.attempts > 1 ? ' · attempt ' + esc(j.attempts) : ''}${j.size ? ' · ' + esc(fmtBytes(j.size)) : ''}</div>
          ${jobSummary(r)}
          ${err ? `<div class="error-text small">${esc(err)}</div>` : ''}
        </div>
        <div class="row-side"><span class="pill pill-${cls}" data-testid="job-status">${esc(j.status)}</span>${retry}</div>
      </li>`;
    }).join('') + '</ul>';
  }

  function loadJobs() {
    return load('jobs', $('#jobs-list'), async () => {
      const d = await api('GET', '/api/jobs?limit=50');
      return (d && d.jobs) || [];
    }, renderJobs);
  }

  async function retryJob(id) {
    const res = await attempt(() => api('POST', `/api/jobs/${encodeURIComponent(id)}/retry`), 'Job queued again.');
    if (res.ok) { loadJobs(); loadStatus(); }
  }

  /* ── settings ───────────────────────────────────────────────────────── */

  function renderToggles(settings) {
    const keys = SETTING_ORDER.filter((k) => k in settings).concat(Object.keys(settings).filter((k) => !SETTING_ORDER.includes(k)).sort());
    if (!keys.length) return empty('No settings available.');
    return '<div class="card toggles">' + keys.map((k) => {
      const meta = SETTING_META[k] || [k, ''];
      return `<label class="toggle">
        <span class="toggle-text"><span class="toggle-title">${esc(meta[0])}</span>${meta[1] ? `<span class="muted small">${esc(meta[1])}</span>` : ''}</span>
        <input type="checkbox" class="switch" role="switch" data-testid="setting-${esc(k)}" data-setting="${esc(k)}" ${settings[k] === 'true' ? 'checked' : ''}>
      </label>`;
    }).join('') + '</div>';
  }

  function renderInfo(info) {
    const rows = [
      ['Version', info.version],
      ['VPN', info.vpn_enabled ? 'enabled' : 'disabled'],
      ['Server countries', info.server_countries],
      ['Timezone', info.tz],
      ['Config dir', info.config_dir],
      ['Library dir', info.library_dir],
      ['Work dir', info.work_dir]
    ].filter((r) => r[1] !== undefined && r[1] !== null && r[1] !== '');
    return '<dl class="card info">' + rows.map((r) => `<div><dt>${esc(r[0])}</dt><dd>${esc(r[1])}</dd></div>`).join('') + '</dl>';
  }

  function paintSettings() {
    const d = state.settings;
    if (!d) return;
    paint('settings-toggles', $('#settings-toggles'), d.settings, renderToggles);
    paint('settings-info', $('#settings-info'), d.info, renderInfo);
  }

  function loadSettings() {
    if (state.inflight.settings || !state.me) return;
    state.inflight.settings = true;
    return api('GET', '/api/settings').then((d) => {
      if (!state.me) return;
      state.settings = { settings: (d && d.settings) || {}, info: (d && d.info) || {} };
      paintSettings();
    }).catch((e) => {
      if (e.status !== 401 && !state.settings) $('#settings-toggles').innerHTML = empty('Could not load settings: ' + errMsg(e));
    }).finally(() => { state.inflight.settings = false; });
  }

  async function onSettingChange(input) {
    const key = input.dataset.setting;
    input.disabled = true;
    const val = input.checked ? 'true' : 'false';
    const res = await attempt(() => api('PUT', '/api/settings', { [key]: val }), 'Setting saved.');
    if (res.ok) {
      state.settings.settings[key] = val;
      Object.assign(state.settings.settings, (res.data && res.data.settings) || res.data || {});
    } else {
      input.checked = !input.checked;
    }
    input.disabled = false;
    state.sig['settings-toggles'] = '';
    paintSettings();
  }

  /* ── wiring ─────────────────────────────────────────────────────────── */

  const ACTIONS = {
    add: (el) => addTitle(+el.dataset.i),
    'dl-pause': (el) => downloadAction(el.dataset.hash, 'pause'),
    'dl-resume': (el) => downloadAction(el.dataset.hash, 'resume'),
    'dl-remove': (el) => removeDownload(el.dataset.hash, el.dataset.name),
    retry: (el) => retryJob(el.dataset.id)
  };

  function bind() {
    document.addEventListener('click', (ev) => {
      const tab = ev.target.closest('.tab');
      if (tab) { showTab(tab.dataset.tab); return; }
      const el = ev.target.closest('[data-action]');
      if (el && ACTIONS[el.dataset.action] && !el.disabled) ACTIONS[el.dataset.action](el);
    });
    document.addEventListener('change', (ev) => {
      const t = ev.target;
      if (t.matches && t.matches('[data-setting]')) onSettingChange(t);
    });
    document.addEventListener('visibilitychange', () => {
      if (!document.hidden && state.me) { loadStatus(); refreshTab(); }
    });
    // Broken poster URLs fall back to the placeholder (error events do not bubble, so capture).
    document.addEventListener('error', (ev) => {
      const img = ev.target;
      if (!img || img.tagName !== 'IMG') return;
      img.hidden = true;
      if (img.parentElement.classList.contains('poster')) img.insertAdjacentHTML('afterend', '<span class="poster-ph">No poster</span>');
    }, true);
    $('#tablist').addEventListener('keydown', onTabKey);
    $('#login-form').addEventListener('submit', onLoginSubmit);
    $('#logout-btn').addEventListener('click', logout);
    $('#search-form').addEventListener('submit', doSearch);
    $('#dialog-cancel').addEventListener('click', () => $('#dialog').close('cancel'));
    $('#login-overlay').addEventListener('keydown', (ev) => { if (ev.key === 'Escape') ev.preventDefault(); });
  }

  bind();
  boot();
})();
