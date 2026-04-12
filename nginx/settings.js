// nginx/settings.js
// Settings component — registered with PeliculaFW; mounted by dashboard.js.
// Depends on: framework.js (PeliculaFW), dashboard.js (tfetch, store).

'use strict';

(function () {
    const { component } = PeliculaFW;

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

    // ── Settings load / save ──────────────────────────────────────────────────

    async function loadSettingsTab() {
        try {
            const [psRes, msRes] = await Promise.all([
                tfetch('/api/pelicula/procula-settings'),
                tfetch('/api/pelicula/settings'),
            ]);
            if (psRes.ok) {
                const ps = await psRes.json();
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
            if (msRes.ok) {
                const ms = await msRes.json();
                _settingsData.middleware = ms;
                const langs = document.getElementById('st-sub-langs');
                if (langs) langs.value = ms.sub_langs || '';
                const mode = ms.notifications_mode || 'internal';
                document.querySelectorAll('input[name="st-notif"]').forEach(r => { r.checked = r.value === mode; });
                setToggle('st-open-registration', ms.open_registration === 'true' || ms.open_registration === true);
            }
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
                dual_sub_enabled:    document.getElementById('st-dualsub')?.getAttribute('aria-checked') === 'true',
                dual_sub_pairs:      (document.getElementById('st-dualsub-pairs')?.value || '').split('\n').map(s => s.trim()).filter(Boolean),
                dual_sub_translator: document.querySelector('input[name="st-translator"]:checked')?.value || 'none',
            };
            const middlewarePayload = {
                sub_langs:          document.getElementById('st-sub-langs')?.value || '',
                notifications_mode: document.querySelector('input[name="st-notif"]:checked')?.value || 'internal',
                open_registration:  document.getElementById('st-open-registration')?.getAttribute('aria-checked') === 'true' ? 'true' : 'false',
            };
            const [r1, r2] = await Promise.all([
                tfetch('/api/pelicula/procula-settings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(procPayload) }),
                tfetch('/api/pelicula/settings',         { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(middlewarePayload) }),
            ]);
            if (r1.ok && r2.ok) {
                if (statusEl) { statusEl.textContent = 'Saved \u2713'; setTimeout(() => { statusEl.textContent = ''; }, 3000); }
            } else {
                if (statusEl) statusEl.textContent = 'Save failed';
            }
        } catch (e) {
            if (statusEl) statusEl.textContent = 'Save failed';
        }
    }

    // ── Transcoding profiles ──────────────────────────────────────────────────

    async function loadProfilesPanel() {
        const listEl = document.getElementById('st-profiles-list');
        if (!listEl) return;
        try {
            const res = await tfetch('/api/pelicula/transcode/profiles');
            if (!res.ok) { listEl.textContent = 'Failed to load profiles'; return; }
            const profiles = await res.json();
            _profilesCache = profiles || [];
            renderProfilesList(_profilesCache);
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
            await tfetch('/api/pelicula/transcode/profiles/' + encodeURIComponent(originalName), { method: 'DELETE' });
        }

        if (statusEl) statusEl.textContent = 'Saving\u2026';
        try {
            const res = await tfetch('/api/pelicula/transcode/profiles', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(profile),
            });
            if (res.ok) {
                if (statusEl) { statusEl.textContent = 'Saved \u2713'; setTimeout(function() { if (statusEl) statusEl.textContent = ''; }, 3000); }
                clearProfileForm();
                loadProfilesPanel();
            } else {
                if (statusEl) statusEl.textContent = 'Save failed';
            }
        } catch (e) { if (statusEl) statusEl.textContent = 'Save failed'; }
    }

    async function deleteProfile(name) {
        if (!confirm('Delete profile \u201c' + name + '\u201d?')) return;
        try {
            await tfetch('/api/pelicula/transcode/profiles/' + encodeURIComponent(name), { method: 'DELETE' });
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
            await tfetch('/api/pelicula/transcode/profiles', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(defaults[i]) });
        }
        loadProfilesPanel();
    }

    // ── Arr meta + Download Defaults ──────────────────────────────────────────

    async function loadArrMeta() {
        try {
            const resp = await fetch('/api/pelicula/arr-meta');
            if (!resp.ok) return;
            _arrMeta = await resp.json();
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
        const get = function(id) { return document.getElementById(id); };
        const body = {};
        const radarrProfile = get('req-radarr-profile') && get('req-radarr-profile').value;
        const radarrRoot    = get('req-radarr-root')    && get('req-radarr-root').value;
        const sonarrProfile = get('req-sonarr-profile') && get('req-sonarr-profile').value;
        const sonarrRoot    = get('req-sonarr-root')    && get('req-sonarr-root').value;
        if (radarrProfile) body.requests_radarr_profile_id = radarrProfile;
        if (radarrRoot)    body.requests_radarr_root       = radarrRoot;
        if (sonarrProfile) body.requests_sonarr_profile_id = sonarrProfile;
        if (sonarrRoot)    body.requests_sonarr_root       = sonarrRoot;
        try {
            const resp = await fetch('/api/pelicula/settings', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', 'Origin': window.location.origin },
                body: JSON.stringify(body),
            });
            const data = await resp.json();
            if (!resp.ok) {
                const statusEl = document.getElementById('requests-settings-save-status');
                if (statusEl) statusEl.textContent = 'Save failed: ' + (data.error || resp.status);
                return;
            }
            const statusEl = document.getElementById('requests-settings-save-status');
            if (statusEl) { statusEl.textContent = 'Saved \u2713'; setTimeout(function() { statusEl.textContent = ''; }, 3000); }
        } catch (e) { alert('Network error'); }
    }

    // ── Component registration ────────────────────────────────────────────────

    component('settings', function (el) {
        function onTabChanged() {
            if (!_settingsLoaded) loadSettingsTab();
            loadProfilesPanel();
            if (!arrMetaLoaded) { loadArrMeta(); arrMetaLoaded = true; }
        }

        function init() {
            PeliculaFW.onTab('settings', onTabChanged);
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

    // ── Window exports ────────────────────────────────────────────────────────
    // Called from onclick handlers in index.html and from applyRole() in dashboard.js.
    window.toggleSetting          = toggleSetting;
    window.updateNotifMode        = updateNotifMode;
    window.saveSettingsTab        = saveSettingsTab;
    window.clearProfileForm       = clearProfileForm;
    window.saveProfile            = saveProfile;
    window.installDefaultProfiles = installDefaultProfiles;
    window.saveRequestsSettings   = saveRequestsSettings;
    // loadArrMeta is called from applyRole() in dashboard.js; the flag lives here.
    window.loadArrMeta = function () {
        if (!arrMetaLoaded) { loadArrMeta(); arrMetaLoaded = true; }
    };
}());
