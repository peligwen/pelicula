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
            const [r1, r2] = await Promise.all([
                tfetch('/api/pelicula/procula-settings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(procPayload) }),
                tfetch('/api/pelicula/settings',         { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(middlewarePayload) }),
            ]);
            if (r1.ok && r2.ok) {
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
            const resp = await tfetch('/api/pelicula/settings', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(body),
            });
            if (resp.ok) {
                if (statusEl) { statusEl.textContent = 'Saved \u2713 \u2014 restart nginx to apply'; setTimeout(() => { if (statusEl) statusEl.textContent = ''; }, 6000); }
                Object.assign(_settingsData.middleware || (_settingsData.middleware = {}), body);
                updateRemoteSummary();
                closeSettingsDrawer('remote');
            } else {
                const data = await resp.json().catch(() => ({}));
                if (statusEl) statusEl.textContent = 'Save failed: ' + (data.error || resp.status);
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

    // ── Blocked releases ──────────────────────────────────────────────────────

    async function loadBlockedReleases() {
        const container = document.getElementById('st-blocked-releases-list');
        if (!container) return;
        container.textContent = 'Loading\u2026';
        try {
            const res = await fetch('/api/procula/blocked-releases', { credentials: 'same-origin' });
            if (!res.ok) throw new Error('HTTP ' + res.status);
            const rows = await res.json();
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
            const res = await fetch('/api/procula/blocked-releases/' + id, {
                method: 'DELETE',
                credentials: 'same-origin',
            });
            if (!res.ok) throw new Error('HTTP ' + res.status);
            // Reload the list
            await loadBlockedReleases();
        } catch (e) {
            btn.disabled = false;
            btn.textContent = 'Unblock';
            alert('Unblock failed: ' + e.message);
        }
    }

    // ── Libraries ────────────────────────────────────────────────────────────────

    async function loadLibraries() {
        const listEl = document.getElementById('st-libraries-list');
        if (!listEl) return;
        listEl.textContent = 'Loading\u2026';
        try {
            const res = await tfetch('/api/pelicula/libraries');
            if (!res.ok) { listEl.textContent = 'Failed to load libraries'; return; }
            const libs = await res.json();
            renderLibraries(libs || []);
        } catch (e) { listEl.textContent = 'Error loading libraries'; }
    }

    function renderLibraries(libs) {
        const listEl = document.getElementById('st-libraries-list');
        if (!listEl) return;
        if (!libs.length) {
            listEl.textContent = 'No libraries configured.';
            return;
        }
        listEl.replaceChildren();
        libs.forEach(function(lib) {
            const row = document.createElement('div');
            row.style.cssText = 'display:flex;align-items:flex-start;justify-content:space-between;gap:.75rem;padding:.5rem 0;border-bottom:1px solid var(--border)';

            const info = document.createElement('div');
            info.style.cssText = 'flex:1;min-width:0';

            const title = document.createElement('div');
            title.style.cssText = 'font-size:.85rem;font-weight:500';
            title.textContent = lib.name;
            if (lib.builtin) {
                const badge = document.createElement('span');
                badge.style.cssText = 'display:inline-block;margin-left:.4rem;padding:.1rem .35rem;font-size:.65rem;background:var(--border);border-radius:3px;color:var(--muted);vertical-align:middle';
                badge.textContent = 'Built-in';
                title.appendChild(badge);
            }
            info.appendChild(title);

            const meta = document.createElement('div');
            meta.style.cssText = 'font-size:.72rem;color:var(--muted);margin-top:.15rem';
            const parts = [lib.slug, lib.type, lib.arr !== 'none' ? lib.arr : null, lib.processing].filter(Boolean);
            meta.textContent = parts.join(' \u00b7 ');
            info.appendChild(meta);

            if (lib.path) {
                const pathNote = document.createElement('div');
                pathNote.style.cssText = 'font-size:.7rem;color:var(--muted);margin-top:.1rem;font-style:italic';
                pathNote.textContent = lib.path + ' \u2014 Requires stack restart to take effect';
                info.appendChild(pathNote);
            }

            row.appendChild(info);

            if (!lib.builtin) {
                const btn = document.createElement('button');
                btn.textContent = 'Delete';
                btn.style.cssText = 'flex-shrink:0;padding:.3rem .7rem;border-radius:4px;border:1px solid var(--border);background:transparent;color:var(--muted);font-size:.75rem;cursor:pointer';
                btn.addEventListener('click', function() { deleteLibrary(lib.slug, lib.name); });
                row.appendChild(btn);
            }

            listEl.appendChild(row);
        });
    }

    function libAutoSlug() {
        const nameEl = document.getElementById('lib-name');
        const slugEl = document.getElementById('lib-slug');
        if (!nameEl || !slugEl) return;
        // Only auto-fill if the user hasn't manually edited the slug
        if (slugEl.dataset.manual === 'true') return;
        slugEl.value = nameEl.value.toLowerCase().replace(/\s+/g, '-').replace(/[^a-z0-9-]/g, '').replace(/^-+|-+$/g, '');
    }

    async function addLibrary() {
        const statusEl = document.getElementById('st-lib-status');
        const name = (document.getElementById('lib-name')?.value || '').trim();
        const slug = (document.getElementById('lib-slug')?.value || '').trim();
        if (!name) { if (statusEl) statusEl.textContent = 'Name is required'; return; }
        if (!slug) { if (statusEl) statusEl.textContent = 'Slug is required'; return; }
        if (!/^[a-z0-9][a-z0-9-]*$/.test(slug)) {
            if (statusEl) statusEl.textContent = 'Slug must start with a letter or number and contain only lowercase letters, numbers, and hyphens';
            return;
        }

        const lib = {
            name: name,
            slug: slug,
            type: document.getElementById('lib-type')?.value || 'other',
            arr:  document.getElementById('lib-arr')?.value || 'none',
            processing: document.getElementById('lib-processing')?.value || 'audit',
            path: (document.getElementById('lib-path')?.value || '').trim(),
        };

        if (statusEl) statusEl.textContent = 'Saving\u2026';
        try {
            const res = await tfetch('/api/pelicula/libraries', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(lib),
            });
            if (res.ok) {
                if (statusEl) { statusEl.textContent = 'Added \u2713'; setTimeout(function() { if (statusEl) statusEl.textContent = ''; }, 3000); }
                // Clear form
                ['lib-name', 'lib-slug', 'lib-path'].forEach(function(id) { const el = document.getElementById(id); if (el) el.value = ''; });
                const slugEl = document.getElementById('lib-slug');
                if (slugEl) slugEl.dataset.manual = '';
                loadLibraries();
                // If the user supplied an external path and wired it to an arr, offer scan & register.
                if (lib.path && lib.arr !== 'none') {
                    scanAndRegisterLibrary(lib.slug, lib.arr, statusEl);
                }
            } else {
                const data = await res.json().catch(function() { return {}; });
                if (statusEl) statusEl.textContent = 'Failed: ' + (data.error || res.status);
            }
        } catch (e) { if (statusEl) statusEl.textContent = 'Save failed'; }
    }

    // scanAndRegisterLibrary: after a library with an external path is created,
    // offer to scan the library directory and register its contents with Radarr/Sonarr.
    // This is a convenience shortcut for the common "adopt existing library" flow.
    async function scanAndRegisterLibrary(slug, arr, statusEl) {
        const containerPath = '/media/' + slug;
        if (!confirm('Scan \u201c' + containerPath + '\u201d and register existing media with ' + (arr === 'radarr' ? 'Radarr' : 'Sonarr') + '?\n\nFiles must already be in a compatible layout (Title (Year)/ for movies, Title/Season XX/ for TV). No files will be moved.')) {
            return;
        }
        if (statusEl) statusEl.textContent = 'Scanning\u2026';
        try {
            const scanRes = await tfetch('/api/pelicula/library/scan', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ folders: [containerPath] }),
            });
            if (!scanRes.ok) {
                const err = await scanRes.json().catch(function() { return {}; });
                if (statusEl) statusEl.textContent = 'Scan failed: ' + (err.error || scanRes.status);
                return;
            }
            const scanData = await scanRes.json();
            const items = (scanData || [])
                .filter(function(r) { return r.status === 'new' && r.match; })
                .map(function(r) {
                    return {
                        type: r.match.type === 'series' ? 'series' : 'movie',
                        tmdbId: r.match.tmdbId || 0,
                        tvdbId: r.match.tvdbId || 0,
                        title: r.match.title,
                        year: r.match.year || 0,
                        season: r.match.season || 0,
                        episode: r.match.episode || 0,
                        rootFolderPath: containerPath,
                        monitored: false,
                        sourcePath: r.file,
                        destPath: r.suggestedPath || '',
                    };
                });
            if (!items.length) {
                if (statusEl) { statusEl.textContent = 'Nothing new to register \u2713'; setTimeout(function() { if (statusEl) statusEl.textContent = ''; }, 3000); }
                return;
            }
            if (statusEl) statusEl.textContent = 'Registering ' + items.length + ' item(s)\u2026';
            const applyRes = await tfetch('/api/pelicula/library/apply', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ items: items, strategy: 'register', validate: false }),
            });
            if (applyRes.ok) {
                const result = await applyRes.json().catch(function() { return {}; });
                const added = result.added || 0;
                if (statusEl) { statusEl.textContent = 'Registered ' + added + ' item(s) \u2713'; setTimeout(function() { if (statusEl) statusEl.textContent = ''; }, 4000); }
            } else {
                const err = await applyRes.json().catch(function() { return {}; });
                if (statusEl) statusEl.textContent = 'Register failed: ' + (err.error || applyRes.status);
            }
        } catch (e) { if (statusEl) statusEl.textContent = 'Scan & register failed'; }
    }

    async function deleteLibrary(slug, name) {
        if (!confirm('Delete library \u201c' + name + '\u201d? This cannot be undone.')) return;
        try {
            const res = await tfetch('/api/pelicula/libraries/' + encodeURIComponent(slug), { method: 'DELETE' });
            if (res.ok || res.status === 204) {
                loadLibraries();
            } else {
                const data = await res.json().catch(function() { return {}; });
                alert('Delete failed: ' + (data.error || res.status));
            }
        } catch (e) { alert('Delete failed'); }
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
        PeliculaFW.openDrawer(drawer, backdrop);
    }

    function closeSettingsDrawer(name) {
        const drawerId = _settingsDrawers[name];
        if (!drawerId) return;
        PeliculaFW.closeDrawer(
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
            loadLibraries();
        }

        function init() {
            PeliculaFW.onTab('settings', onTabChanged);
            // Track manual edits to lib-slug so auto-fill stops overriding user input
            var slugEl = document.getElementById('lib-slug');
            if (slugEl) {
                slugEl.addEventListener('input', function() { slugEl.dataset.manual = 'true'; });
            }
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
    window.openSettingsDrawer    = openSettingsDrawer;
    window.closeSettingsDrawer   = closeSettingsDrawer;
    window.saveSubtitlesDrawer   = saveSubtitlesDrawer;
    window.toggleSetting          = toggleSetting;
    window.updateNotifMode        = updateNotifMode;
    window.saveRemoteAccess            = saveRemoteAccess;
    window.updateCertMode              = updateCertMode;
    window.saveSettingsTab        = saveSettingsTab;
    window.clearProfileForm       = clearProfileForm;
    window.saveProfile            = saveProfile;
    window.installDefaultProfiles = installDefaultProfiles;
    window.saveRequestsSettings   = saveRequestsSettings;
    window.addLibrary             = addLibrary;
    window.libAutoSlug            = libAutoSlug;
    // loadArrMeta is called from applyRole() in dashboard.js; the flag lives here.
    window.loadArrMeta = function () {
        if (!arrMetaLoaded) { loadArrMeta(); arrMetaLoaded = true; }
    };
}());
