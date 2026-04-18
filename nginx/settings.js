// nginx/settings.js
// Settings component — registered with PeliculaFW; mounted by dashboard.js.

import { component, onTab, openDrawer, closeDrawer, wireSwitches } from '/framework.js';
import { get, post, del } from '/api.js';

// ── Module-level state ────────────────────────────────────────────────────
let _settingsLoaded = false;
let _settingsData   = {};
let _profilesCache  = [];
let arrMetaLoaded   = false;
let _arrMeta        = null;

// ── Toggle helpers ────────────────────────────────────────────────────────

function setToggle(id, on) {
    const el = document.getElementById(id);
    if (!el) return;
    el.setAttribute('aria-checked', on ? 'true' : 'false');
}

function toggleSetting(btn) {
    const current = btn.getAttribute('aria-checked') === 'true';
    btn.setAttribute('aria-checked', current ? 'false' : 'true');
    if (btn.dataset.key === 'dual_sub_enabled') updateDualSubOpts();
}

function updateDualSubOpts() {
    const el   = document.getElementById('st-dualsub');
    const opts = document.getElementById('st-dualsub-opts');
    if (!el || !opts) return;
    opts.style.display = el.getAttribute('aria-checked') === 'true' ? '' : 'none';
}

function updateNotifMode() {
    // placeholder — no extra UI to update currently
}

function updateProfilesSummary() {
    const el = document.getElementById('st-profiles-summary-status');
    if (!el) return;
    const total  = _profilesCache.length;
    const active = _profilesCache.filter(p => p.enabled !== false).length;
    if (total === 0) {
        el.textContent = 'No profiles configured';
    } else {
        el.textContent = total + ' profile' + (total !== 1 ? 's' : '') + ', ' + active + ' active';
    }
}

function updateSubsSummary() {
    const el = document.getElementById('st-subs-summary-status');
    if (!el) return;
    const langs = (document.getElementById('st-sub-langs')?.value || '').trim();
    const dual  = document.getElementById('st-dualsub')?.getAttribute('aria-checked') === 'true';
    let text = langs || 'none';
    if (dual) text += ' \u00b7 dual on';
    el.textContent = text;
}

function updateRemoteSummary() {
    const el = document.getElementById('st-remote-summary-status');
    if (!el) return;
    const ms = _settingsData.middleware || {};
    const dot = document.createElement('span');
    if (ms.remote_access_enabled === 'true') {
        dot.className = 'status-dot active';
        const host = ms.remote_hostname || 'configured';
        const cert = ms.remote_cert_mode || 'self-signed';
        el.textContent = '';
        el.appendChild(dot);
        el.appendChild(document.createTextNode(host + ' \u00b7 ' + cert));
    } else {
        dot.className = 'status-dot inactive';
        el.textContent = '';
        el.appendChild(dot);
        el.appendChild(document.createTextNode('Disabled'));
    }
}

function updateCertMode() {
    const mode = document.querySelector('input[name="st-cert-mode"]:checked');
    const leOpts = document.getElementById('st-le-opts');
    if (!leOpts) return;
    leOpts.style.display = (mode && mode.value === 'letsencrypt') ? '' : 'none';
}

// ── Settings load / save ──────────────────────────────────────────────────

async function loadSettingsTab() {
    try {
        const [psResult, msResult] = await Promise.allSettled([
            get('/api/pelicula/procula-settings'),
            get('/api/pelicula/settings'),
        ]);
        if (psResult.status === 'fulfilled' && psResult.value) {
            const ps = psResult.value;
            _settingsData.procula = ps;
            setToggle('st-validation', ps.validation_enabled !== false);
            setToggle('st-transcoding', ps.transcoding_enabled !== false);
            setToggle('st-cataloging', ps.catalog_enabled !== false);
            setToggle('st-dualsub', !!ps.dual_sub_enabled);
            const pairs = document.getElementById('st-dualsub-pairs');
            if (pairs) pairs.value = (ps.dual_sub_pairs || []).join('\n');
            const translator = ps.dual_sub_translator || 'none';
            document.querySelectorAll('input[name="st-translator"]').forEach(r => { r.checked = r.value === translator; });
            updateDualSubOpts();
        }
        if (msResult.status === 'fulfilled' && msResult.value) {
            const ms = msResult.value;
            _settingsData.middleware = ms;
            const langs = document.getElementById('st-sub-langs');
            if (langs) langs.value = ms.sub_langs || '';
            const mode = ms.notifications_mode || 'internal';
            document.querySelectorAll('input[name="st-notif"]').forEach(r => { r.checked = r.value === mode; });
            setToggle('st-open-registration', ms.open_registration === 'true' || ms.open_registration === true);
            const searchMode = ms.search_mode || 'tmdb';
            document.querySelectorAll('input[name="search_mode"]').forEach(r => { r.checked = r.value === searchMode; });

            // Remote access
            setToggle('st-remote-enabled', ms.remote_access_enabled === 'true');
            const hostname = document.getElementById('st-remote-hostname');
            if (hostname) hostname.value = ms.remote_hostname || '';
            const httpPort = document.getElementById('st-remote-http-port');
            if (httpPort) httpPort.value = ms.remote_http_port || '';
            const httpsPort = document.getElementById('st-remote-https-port');
            if (httpsPort) httpsPort.value = ms.remote_https_port || '';
            const certMode = ms.remote_cert_mode || 'self-signed';
            document.querySelectorAll('input[name="st-cert-mode"]').forEach(r => { r.checked = r.value === certMode; });
            updateCertMode();
            const leEmail = document.getElementById('st-le-email');
            if (leEmail) leEmail.value = ms.remote_le_email || '';
            setToggle('st-le-staging', ms.remote_le_staging === 'true');
        }
        updateRemoteSummary();
        updateSubsSummary();
        _settingsLoaded = true;
    } catch (e) { console.warn('[pelicula] settings load error:', e); }
}

async function saveSettingsTab() {
    const statusEl = document.getElementById('st-save-status');
    if (statusEl) statusEl.textContent = 'Saving\u2026';
    try {
        const procPayload = {
            validation_enabled:  document.getElementById('st-validation')?.getAttribute('aria-checked') === 'true',
            transcoding_enabled: document.getElementById('st-transcoding')?.getAttribute('aria-checked') === 'true',
            catalog_enabled:     document.getElementById('st-cataloging')?.getAttribute('aria-checked') === 'true',
        };
        const middlewarePayload = {
            notifications_mode: document.querySelector('input[name="st-notif"]:checked')?.value || 'internal',
            open_registration:  document.getElementById('st-open-registration')?.getAttribute('aria-checked') === 'true' ? 'true' : 'false',
            search_mode:        document.querySelector('input[name="search_mode"]:checked')?.value || 'tmdb',
        };
        const [r1, r2] = await Promise.allSettled([
            post('/api/pelicula/procula-settings', procPayload),
            post('/api/pelicula/settings', middlewarePayload),
        ]);
        if (r1.status === 'fulfilled' && r2.status === 'fulfilled') {
            if (statusEl) { statusEl.textContent = 'Saved \u2713'; setTimeout(() => { statusEl.textContent = ''; }, 3000); }
        } else {
            if (statusEl) statusEl.textContent = 'Save failed';
        }
    } catch (e) {
        if (statusEl) statusEl.textContent = 'Save failed';
    }
}

async function saveSubtitlesDrawer() {
    const statusEl = document.getElementById('st-subs-save-status');
    if (statusEl) statusEl.textContent = 'Saving\u2026';
    try {
        const procPayload = {
            dual_sub_enabled:    document.getElementById('st-dualsub')?.getAttribute('aria-checked') === 'true',
            dual_sub_pairs:      (document.getElementById('st-dualsub-pairs')?.value || '').split('\n').map(s => s.trim()).filter(Boolean),
            dual_sub_translator: document.querySelector('input[name="st-translator"]:checked')?.value || 'none',
        };
        const middlewarePayload = {
            sub_langs: document.getElementById('st-sub-langs')?.value || '',
        };
        const [r1, r2] = await Promise.allSettled([
            post('/api/pelicula/procula-settings', procPayload),
            post('/api/pelicula/settings', middlewarePayload),
        ]);
        if (r1.status === 'fulfilled' && r2.status === 'fulfilled') {
            if (statusEl) { statusEl.textContent = 'Saved \u2713'; setTimeout(() => { if (statusEl) statusEl.textContent = ''; }, 3000); }
            updateSubsSummary();
            closeSettingsDrawer('subs');
        } else {
            if (statusEl) statusEl.textContent = 'Save failed';
        }
    } catch (e) {
        if (statusEl) statusEl.textContent = 'Save failed';
    }
}

async function saveRemoteAccess() {
    const statusEl = document.getElementById('st-remote-save-status');
    if (statusEl) statusEl.textContent = 'Saving\u2026';
    const certMode = document.querySelector('input[name="st-cert-mode"]:checked');
    const body = {
        remote_access_enabled: document.getElementById('st-remote-enabled')?.getAttribute('aria-checked') === 'true' ? 'true' : 'false',
        remote_hostname:       document.getElementById('st-remote-hostname')?.value.trim() || '',
        remote_http_port:      document.getElementById('st-remote-http-port')?.value.trim() || '',
        remote_https_port:     document.getElementById('st-remote-https-port')?.value.trim() || '',
        remote_cert_mode:      certMode ? certMode.value : 'self-signed',
        remote_le_email:       document.getElementById('st-le-email')?.value.trim() || '',
        remote_le_staging:     document.getElementById('st-le-staging')?.getAttribute('aria-checked') === 'true' ? 'true' : 'false',
    };
    try {
        await post('/api/pelicula/settings', body);
        if (statusEl) { statusEl.textContent = 'Saved \u2713 \u2014 restart nginx to apply'; setTimeout(() => { if (statusEl) statusEl.textContent = ''; }, 6000); }
        Object.assign(_settingsData.middleware || (_settingsData.middleware = {}), body);
        updateRemoteSummary();
        closeSettingsDrawer('remote');
    } catch (e) {
        const errMsg = (e.body && e.body.error) || e.message || 'unknown';
        if (statusEl) statusEl.textContent = 'Save failed: ' + errMsg;
    }
}

// ── Transcoding profiles ──────────────────────────────────────────────────

async function loadProfilesPanel() {
    const listEl = document.getElementById('st-profiles-list');
    if (!listEl) return;
    try {
        const profiles = await get('/api/pelicula/transcode/profiles');
        if (!profiles) { listEl.textContent = 'Failed to load profiles'; return; }
        _profilesCache = profiles || [];
        renderProfilesList(_profilesCache);
        updateProfilesSummary();
    } catch (e) { listEl.textContent = 'Error loading profiles'; }
}

function renderProfilesList(profiles) {
    const listEl = document.getElementById('st-profiles-list');
    if (!listEl) return;
    if (!profiles || !profiles.length) {
        listEl.textContent = 'No profiles. Click \u201cInstall defaults\u201d or fill in the form below.';
        return;
    }
    const rows = profiles.map((p, i) => {
        const conds = [];
        if (p.conditions && p.conditions.codecs_include && p.conditions.codecs_include.length)
            conds.push('codecs: ' + p.conditions.codecs_include.join(', '));
        if (p.conditions && p.conditions.min_height)
            conds.push('min height: ' + p.conditions.min_height + 'px');
        const row = document.createElement('div');
        row.className = 'st-profile-row';
        const info = document.createElement('div');
        info.className = 'st-profile-info';
        info.append(Object.assign(document.createElement('span'), { className: 'st-profile-name', textContent: p.name }));
        const badge = document.createElement('span');
        badge.className = 'st-profile-badge ' + (p.enabled ? 'on' : 'off');
        badge.textContent = p.enabled ? 'on' : 'off';
        info.append(badge);
        if (conds.length) {
            const cond = document.createElement('span');
            cond.className = 'st-profile-cond';
            cond.textContent = '(' + conds.join(' \u00b7 ') + ')';
            info.append(cond);
        }
        if (p.description) {
            info.append(Object.assign(document.createElement('div'), { className: 'st-profile-desc', textContent: p.description }));
        }
        const btns = document.createElement('div');
        btns.className = 'st-profile-btns';
        const editBtn = document.createElement('button');
        editBtn.className = 'sm-btn';
        editBtn.textContent = 'Edit';
        editBtn.addEventListener('click', function() { editProfileByIdx(i); });
        const delBtn = document.createElement('button');
        delBtn.className = 'sm-btn sm-btn-danger';
        delBtn.textContent = 'Delete';
        delBtn.addEventListener('click', function() { deleteProfile(p.name); });
        btns.append(editBtn, delBtn);
        row.append(info, btns);
        return row;
    });
    listEl.replaceChildren.apply(listEl, rows);
}

function editProfileByIdx(idx) {
    const p = _profilesCache[idx];
    if (!p) return;
    document.getElementById('pf-edit-original-name').value = p.name;
    document.getElementById('pf-name').value = p.name || '';
    document.getElementById('pf-desc').value = p.description || '';
    document.getElementById('pf-codecs').value = (p.conditions && p.conditions.codecs_include || []).join(', ');
    document.getElementById('pf-minheight').value = (p.conditions && p.conditions.min_height) || '';
    document.getElementById('pf-vcodec').value = (p.output && p.output.video_codec) || 'libx264';
    document.getElementById('pf-preset').value = (p.output && p.output.video_preset) || 'medium';
    document.getElementById('pf-crf').value = (p.output && p.output.video_crf != null) ? p.output.video_crf : 20;
    document.getElementById('pf-maxheight').value = (p.output && p.output.max_height) || '';
    document.getElementById('pf-acodec').value = (p.output && p.output.audio_codec) || 'aac';
    document.getElementById('pf-achannels').value = (p.output && p.output.audio_channels) || '';
    document.getElementById('pf-suffix').value = (p.output && p.output.suffix) || '';
    setToggle('pf-enabled', p.enabled !== false);
    const cancelBtn = document.getElementById('pf-cancel-btn');
    if (cancelBtn) cancelBtn.style.display = '';
    document.getElementById('pf-name').scrollIntoView({ behavior: 'smooth', block: 'center' });
}

function clearProfileForm() {
    ['pf-edit-original-name', 'pf-name', 'pf-desc', 'pf-codecs', 'pf-minheight', 'pf-maxheight', 'pf-achannels', 'pf-suffix'].forEach(function(id) {
        const el = document.getElementById(id);
        if (el) el.value = '';
    });
    document.getElementById('pf-vcodec').value = 'libx264';
    document.getElementById('pf-preset').value = 'medium';
    document.getElementById('pf-crf').value = 20;
    document.getElementById('pf-acodec').value = 'aac';
    setToggle('pf-enabled', true);
    const cancelBtn = document.getElementById('pf-cancel-btn');
    if (cancelBtn) cancelBtn.style.display = 'none';
    const statusEl = document.getElementById('st-profile-status');
    if (statusEl) statusEl.textContent = '';
}

async function saveProfile() {
    const statusEl = document.getElementById('st-profile-status');
    const name = document.getElementById('pf-name').value.trim();
    if (!name) { if (statusEl) statusEl.textContent = 'Name is required'; return; }

    const codecsRaw = document.getElementById('pf-codecs').value.trim();
    const codecs    = codecsRaw ? codecsRaw.split(',').map(function(s) { return s.trim().toLowerCase(); }).filter(Boolean) : [];
    const minH      = parseInt(document.getElementById('pf-minheight').value) || 0;
    const maxH      = parseInt(document.getElementById('pf-maxheight').value) || 0;
    const channels  = parseInt(document.getElementById('pf-achannels').value) || 0;

    const conditions = {};
    if (codecs.length) conditions.codecs_include = codecs;
    if (minH) conditions.min_height = minH;

    const output = {
        video_codec:  document.getElementById('pf-vcodec').value,
        video_preset: document.getElementById('pf-preset').value,
        video_crf:    parseInt(document.getElementById('pf-crf').value) || 20,
        audio_codec:  document.getElementById('pf-acodec').value,
        suffix:       document.getElementById('pf-suffix').value.trim(),
    };
    if (maxH) output.max_height = maxH;
    if (channels) output.audio_channels = channels;

    const profile = {
        name: name,
        description: document.getElementById('pf-desc').value.trim(),
        enabled: document.getElementById('pf-enabled') && document.getElementById('pf-enabled').getAttribute('aria-checked') === 'true',
        conditions: conditions,
        output: output,
    };

    // If we renamed, delete the old profile first
    const originalName = document.getElementById('pf-edit-original-name').value;
    if (originalName && originalName !== name) {
        await del('/api/pelicula/transcode/profiles/' + encodeURIComponent(originalName));
    }

    if (statusEl) statusEl.textContent = 'Saving\u2026';
    try {
        await post('/api/pelicula/transcode/profiles', profile);
        if (statusEl) { statusEl.textContent = 'Saved \u2713'; setTimeout(function() { if (statusEl) statusEl.textContent = ''; }, 3000); }
        clearProfileForm();
        loadProfilesPanel();
    } catch (e) { if (statusEl) statusEl.textContent = 'Save failed'; }
}

async function deleteProfile(name) {
    if (!confirm('Delete profile \u201c' + name + '\u201d?')) return;
    try {
        await del('/api/pelicula/transcode/profiles/' + encodeURIComponent(name));
        loadProfilesPanel();
    } catch (e) { /* ignore */ }
}

async function installDefaultProfiles() {
    const defaults = [
        { name: 'Compatibility 1080p', enabled: true, description: 'Re-encode HEVC/AV1 to H.264 for broad device compatibility, capped at 1080p.', conditions: { codecs_include: ['hevc', 'h265', 'av1'] }, output: { video_codec: 'libx264', video_preset: 'medium', video_crf: 20, max_height: 1080, audio_codec: 'aac', audio_channels: 2, suffix: '-compat' } },
        { name: 'Compatibility 720p',  enabled: true, description: 'Re-encode HEVC/AV1 to H.264 at 720p for mobile and older devices.',            conditions: { codecs_include: ['hevc', 'h265', 'av1'] }, output: { video_codec: 'libx264', video_preset: 'medium', video_crf: 22, max_height: 720,  audio_codec: 'aac', audio_channels: 2, suffix: '-mobile' } },
        { name: 'Downscale 4K to 1080p', enabled: true, description: 'Downscale 4K (2160p+) content to 1080p H.264 to save storage.',             conditions: { min_height: 2160 },                           output: { video_codec: 'libx264', video_preset: 'medium', video_crf: 20, max_height: 1080, audio_codec: 'copy', suffix: '-1080p' } },
    ];
    for (var i = 0; i < defaults.length; i++) {
        await post('/api/pelicula/transcode/profiles', defaults[i]);
    }
    loadProfilesPanel();
}

// ── Arr meta + Download Defaults ──────────────────────────────────────────

async function loadArrMeta() {
    try {
        const meta = await get('/api/pelicula/arr-meta');
        if (!meta) return;
        _arrMeta = meta;
        populateRequestsSettings(_arrMeta);
    } catch (e) { console.warn('[pelicula] loadArrMeta error', e); }
}

function populateRequestsSettings(meta) {
    const fillSelect = function(selectId, items, valueKey, labelKey) {
        const el = document.getElementById(selectId);
        if (!el || !items) return;
        const defaultOpt = document.createElement('option');
        defaultOpt.value = '';
        defaultOpt.textContent = '\u2014 use default \u2014';
        el.replaceChildren(defaultOpt);
        items.forEach(function(item) {
            const opt = document.createElement('option');
            opt.value = String(item[valueKey]);
            opt.textContent = String(item[labelKey]);
            el.appendChild(opt);
        });
    };
    fillSelect('req-radarr-profile', meta && meta.radarr && meta.radarr.qualityProfiles, 'id', 'name');
    fillSelect('req-radarr-root',    meta && meta.radarr && meta.radarr.rootFolders,    'path', 'path');
    fillSelect('req-sonarr-profile', meta && meta.sonarr && meta.sonarr.qualityProfiles, 'id', 'name');
    fillSelect('req-sonarr-root',    meta && meta.sonarr && meta.sonarr.rootFolders,    'path', 'path');
}

async function saveRequestsSettings() {
    const getEl = function(id) { return document.getElementById(id); };
    const body = {};
    const radarrProfile = getEl('req-radarr-profile') && getEl('req-radarr-profile').value;
    const radarrRoot    = getEl('req-radarr-root')    && getEl('req-radarr-root').value;
    const sonarrProfile = getEl('req-sonarr-profile') && getEl('req-sonarr-profile').value;
    const sonarrRoot    = getEl('req-sonarr-root')    && getEl('req-sonarr-root').value;
    if (radarrProfile) body.requests_radarr_profile_id = radarrProfile;
    if (radarrRoot)    body.requests_radarr_root       = radarrRoot;
    if (sonarrProfile) body.requests_sonarr_profile_id = sonarrProfile;
    if (sonarrRoot)    body.requests_sonarr_root       = sonarrRoot;
    try {
        await post('/api/pelicula/settings', body);
        const statusEl = document.getElementById('requests-settings-save-status');
        if (statusEl) { statusEl.textContent = 'Saved \u2713'; setTimeout(function() { statusEl.textContent = ''; }, 3000); }
    } catch (e) {
        const errMsg = (e.body && e.body.error) || e.message || 'unknown';
        const statusEl = document.getElementById('requests-settings-save-status');
        if (statusEl) statusEl.textContent = 'Save failed: ' + errMsg;
    }
}

// ── Blocked releases ──────────────────────────────────────────────────────

async function loadBlockedReleases() {
    const container = document.getElementById('st-blocked-releases-list');
    if (!container) return;
    container.textContent = 'Loading\u2026';
    try {
        const rows = await get('/api/procula/blocked-releases');
        if (!rows) { container.textContent = 'Failed to load blocked releases.'; return; }
        renderBlockedReleases(rows || []);
    } catch (e) {
        container.textContent = 'Failed to load blocked releases.';
    }
}

function renderBlockedReleases(rows) {
    const container = document.getElementById('st-blocked-releases-list');
    if (!container) return;
    if (!rows.length) {
        container.textContent = 'No blocked releases.';
        return;
    }
    container.replaceChildren();
    for (const row of rows) {
        const div = document.createElement('div');
        div.style.cssText = 'display:flex;align-items:flex-start;justify-content:space-between;gap:.75rem;padding:.5rem 0;border-bottom:1px solid var(--border)';

        const info = document.createElement('div');
        info.style.cssText = 'flex:1;min-width:0';
        const title = document.createElement('div');
        title.style.cssText = 'font-size:.85rem;font-weight:500;white-space:nowrap;overflow:hidden;text-overflow:ellipsis';
        title.textContent = row.display_title || row.file_path;
        title.title = row.file_path;
        info.appendChild(title);

        const meta = document.createElement('div');
        meta.style.cssText = 'font-size:.72rem;color:var(--muted);margin-top:.15rem';
        const date = row.blocked_at ? new Date(row.blocked_at).toLocaleDateString() : '';
        meta.textContent = [row.arr_app, date, row.reason].filter(Boolean).join(' \u00b7 ');
        info.appendChild(meta);

        const btn = document.createElement('button');
        btn.textContent = 'Unblock';
        btn.style.cssText = 'flex-shrink:0;padding:.3rem .7rem;border-radius:4px;border:1px solid var(--border);background:transparent;color:var(--muted);font-size:.75rem;cursor:pointer';
        btn.addEventListener('click', () => unblockRelease(row.id, btn));

        div.appendChild(info);
        div.appendChild(btn);
        container.appendChild(div);
    }
}

async function unblockRelease(id, btn) {
    btn.disabled = true;
    btn.textContent = 'Unblocking\u2026';
    try {
        await del('/api/procula/blocked-releases/' + id);
        await loadBlockedReleases();
    } catch (e) {
        btn.disabled = false;
        btn.textContent = 'Unblock';
        alert('Unblock failed: ' + e.message);
    }
}

// ── Settings drawer helpers ───────────────────────────────────────────────

const _settingsDrawers = {
    remote:   'st-remote-drawer',
    profiles: 'st-profiles-drawer',
    subs:     'st-subs-drawer',
};

function openSettingsDrawer(name) {
    const drawerId = _settingsDrawers[name];
    if (!drawerId) return;
    const drawer   = document.getElementById(drawerId);
    const backdrop = document.getElementById('settings-drawer-backdrop');
    if (!drawer || !backdrop) return;
    if (name === 'profiles') loadProfilesPanel();
    backdrop.onclick = function () { closeSettingsDrawer(name); };
    openDrawer(drawer, backdrop);
}

function closeSettingsDrawer(name) {
    const drawerId = _settingsDrawers[name];
    if (!drawerId) return;
    closeDrawer(
        document.getElementById(drawerId),
        document.getElementById('settings-drawer-backdrop')
    );
}

// ── Component registration ────────────────────────────────────────────────

component('settings', function (el) {
    function onTabChanged() {
        if (!_settingsLoaded) loadSettingsTab();
        loadProfilesPanel();
        if (!arrMetaLoaded) { loadArrMeta(); arrMetaLoaded = true; }
        loadBlockedReleases();
    }

    function init() {
        onTab('settings', onTabChanged);
    }

    function destroy() {
        // onTab listeners are lightweight — no cleanup needed
    }

    return {
        render:   function () {},  // no template rendering — operates on existing DOM
        loadOnce: init,
        destroy:  destroy,
    };
});

// ── Event delegation ──────────────────────────────────────────────────────

// Toggle switches — covers settings main panel and all st-* drawers
document.addEventListener('click', e => {
    const toggle = e.target.closest('.toggle[role="switch"]');
    if (!toggle) return;
    const inSettingsSection = document.getElementById('settings-section')?.contains(toggle);
    const inSettingsDrawer  = !!toggle.closest('.drawer[id^="st-"]');
    if (inSettingsSection || inSettingsDrawer) toggleSetting(toggle);
});

// Settings summary rows — delegate by data-drawer
document.querySelectorAll('[data-drawer]').forEach(row => {
    row.addEventListener('click', e => {
        if (!e.target.closest('[data-drawer-btn]')) {
            openSettingsDrawer(row.dataset.drawer);
        }
    });
});
document.querySelectorAll('[data-drawer-btn]').forEach(btn => {
    btn.addEventListener('click', e => {
        e.stopPropagation();
        openSettingsDrawer(btn.dataset.drawerBtn);
    });
});

// Drawer close buttons
document.querySelectorAll('[data-close-drawer]').forEach(btn => {
    btn.addEventListener('click', () => closeSettingsDrawer(btn.dataset.closeDrawer));
});

// Notification mode radios
document.querySelectorAll('[name="st-notif"]').forEach(r => {
    r.addEventListener('change', updateNotifMode);
});

// Certificate mode radios
document.querySelectorAll('[name="st-cert-mode"]').forEach(r => {
    r.addEventListener('change', updateCertMode);
});

// Profiles drawer buttons
document.getElementById('pf-install-defaults-btn')?.addEventListener('click', installDefaultProfiles);
document.getElementById('pf-save-btn')?.addEventListener('click', saveProfile);
document.getElementById('pf-cancel-btn')?.addEventListener('click', clearProfileForm);

// Save buttons
document.getElementById('settings-save-btn')?.addEventListener('click', saveSettingsTab);
document.getElementById('requests-settings-save-btn')?.addEventListener('click', saveRequestsSettings);
document.getElementById('save-subs-drawer-btn')?.addEventListener('click', saveSubtitlesDrawer);
document.getElementById('save-remote-drawer-btn')?.addEventListener('click', saveRemoteAccess);

// Wire Space-key handler on all switches present at page load
wireSwitches();

// ── Window exports ────────────────────────────────────────────────────────
// loadArrMeta is called from applyRole() in dashboard.js; the flag lives here.
window.loadArrMeta = function () {
    if (!arrMetaLoaded) { loadArrMeta(); arrMetaLoaded = true; }
};
