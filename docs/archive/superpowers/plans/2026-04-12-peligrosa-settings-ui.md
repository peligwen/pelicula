# Peligrosa Settings UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a read/write Remote Access settings panel to the dashboard Settings tab so Peligrosa can be configured without editing `.env` directly.

**Architecture:** The backend API already supports remote access fields: `GET /api/pelicula/settings` returns them, `POST /api/pelicula/settings` writes them. This plan adds frontend-only work: HTML panel + JS load/save. A status badge reflects whether remote access is currently enabled and which cert mode is active. Changes show a "stack restart required" notice because they write `.env`.

**Tech Stack:** Vanilla JS, existing `toggleSetting()` pattern, existing `tfetch()` wrapper

---

### File Map

| File | Change |
|------|--------|
| `nginx/index.html` | Add Remote Access settings panel (admin-only, after the Access panel) |
| `nginx/settings.js` | Populate panel in `loadSettingsTab()`; add `saveRemoteAccess()` and `updateCertMode()`; export via `window.*` |

No backend changes. No new files.

---

### Task 1: Add Remote Access panel HTML to `index.html`

**Files:**
- Modify: `nginx/index.html` — insert after the Access panel (the `<div class="settings-panel admin-only">` that ends around line 536)

- [ ] **Step 1: Locate the insertion point**

Find the end of the Access panel in `nginx/index.html`:

```
grep -n "Open registration" nginx/index.html
```

The Access panel is a `<div class="settings-panel admin-only">` containing the "Open registration" toggle row. Insert the Remote Access panel immediately after its closing `</div>`.

- [ ] **Step 2: Insert the Remote Access panel**

After the closing `</div>` of the Access panel, add:

```html
                <!-- Remote Access (Peligrosa) -->
                <div class="settings-panel admin-only">
                    <div class="settings-panel-title" style="display:flex;align-items:center;justify-content:space-between">
                        <span>Remote Access</span>
                        <span id="st-remote-status" class="st-remote-badge"></span>
                    </div>
                    <label class="toggle-row">
                        <span class="toggle-label">Enable remote access</span>
                        <span class="toggle-sub">Serve the dashboard over HTTPS from a public hostname (Peligrosa). Requires a stack restart to activate.</span>
                        <button class="toggle" id="st-remote-enabled" role="switch" onclick="toggleSetting(this)" data-key="remote_access_enabled">
                            <span class="toggle-track"><span class="toggle-thumb"></span></span>
                        </button>
                    </label>
                    <div id="st-remote-opts">
                        <div class="settings-field-row">
                            <label class="settings-field-label" for="st-remote-hostname">Hostname</label>
                            <span class="settings-field-hint">Bare hostname, no scheme or port (e.g. <code>jellyfin.example.com</code>)</span>
                            <input type="text" id="st-remote-hostname" class="settings-input" placeholder="jellyfin.example.com">
                        </div>
                        <div class="settings-field-row">
                            <label class="settings-field-label" for="st-remote-http-port">HTTP port</label>
                            <input type="number" id="st-remote-http-port" class="settings-input" placeholder="80" min="1" max="65535">
                        </div>
                        <div class="settings-field-row">
                            <label class="settings-field-label" for="st-remote-https-port">HTTPS port</label>
                            <input type="number" id="st-remote-https-port" class="settings-input" placeholder="8920" min="1" max="65535">
                        </div>
                        <div class="settings-field-row">
                            <label class="settings-field-label">Certificate mode</label>
                            <div class="settings-radio-row">
                                <label><input type="radio" name="st-cert-mode" value="letsencrypt" onchange="updateCertMode()"> Let&#x2019;s Encrypt</label>
                                <label><input type="radio" name="st-cert-mode" value="byo" onchange="updateCertMode()"> BYO certificate</label>
                                <label><input type="radio" name="st-cert-mode" value="self-signed" onchange="updateCertMode()"> Self-signed</label>
                            </div>
                        </div>
                        <div id="st-le-opts" style="display:none">
                            <div class="settings-field-row">
                                <label class="settings-field-label" for="st-le-email">Let&#x2019;s Encrypt email</label>
                                <input type="email" id="st-le-email" class="settings-input" placeholder="you@example.com">
                            </div>
                            <label class="toggle-row">
                                <span class="toggle-label">Staging certificates</span>
                                <span class="toggle-sub">Use Let&#x2019;s Encrypt staging CA (for testing; not trusted by browsers)</span>
                                <button class="toggle" id="st-le-staging" role="switch" onclick="toggleSetting(this)" data-key="remote_le_staging">
                                    <span class="toggle-track"><span class="toggle-thumb"></span></span>
                                </button>
                            </label>
                        </div>
                    </div>
                    <div class="settings-actions" style="margin-top:0.75rem">
                        <button class="settings-save-btn" onclick="saveRemoteAccess()">Save remote access</button>
                        <span class="settings-save-status" id="st-remote-save-status"></span>
                    </div>
                    <p class="settings-field-hint" style="margin-top:0.25rem;color:var(--muted)">Changes require a stack restart: <code>pelicula restart nginx</code></p>
                </div>
```

- [ ] **Step 3: Commit**

```bash
git add nginx/index.html
git commit -m "feat(settings): add Remote Access settings panel HTML"
```

---

### Task 2: Populate and save the Remote Access panel in `settings.js`

**Files:**
- Modify: `nginx/settings.js`

Three additions:
1. `updateCertMode()` — shows/hides `st-le-opts` based on selected cert mode radio
2. Populate the panel in `loadSettingsTab()` from the middleware settings response
3. `saveRemoteAccess()` — POST the remote access fields to `/api/pelicula/settings`

- [ ] **Step 1: Write the test (manual — no unit test framework for frontend JS)**

After completing the implementation, you will verify by loading the Settings tab in the browser. Tests are listed as browser checks at the end of this task.

- [ ] **Step 2: Add `updateCertMode` function**

In `nginx/settings.js`, add after the `updateNotifMode` function:

```js
    function updateCertMode() {
        const mode = document.querySelector('input[name="st-cert-mode"]:checked');
        const leOpts = document.getElementById('st-le-opts');
        if (!leOpts) return;
        leOpts.style.display = (mode && mode.value === 'letsencrypt') ? '' : 'none';
    }
```

- [ ] **Step 3: Populate the Remote Access panel in `loadSettingsTab()`**

In `loadSettingsTab()`, inside the `if (msRes.ok)` block, after the existing field assignments (after the `setToggle('st-open-registration', ...)` line), add:

```js
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

                // Status badge
                const badge = document.getElementById('st-remote-status');
                if (badge) {
                    if (ms.remote_access_enabled === 'true') {
                        badge.textContent = 'active \u00b7 ' + certMode;
                        badge.style.color = 'var(--mint, #7dda93)';
                    } else {
                        badge.textContent = 'disabled';
                        badge.style.color = 'var(--muted, #9080a8)';
                    }
                }
```

- [ ] **Step 4: Add `saveRemoteAccess` function**

In `nginx/settings.js`, add after `saveSettingsTab`:

```js
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
                if (statusEl) { statusEl.textContent = 'Saved \u2713 — restart nginx to apply'; setTimeout(() => { if (statusEl) statusEl.textContent = ''; }, 6000); }
            } else {
                const data = await resp.json().catch(() => ({}));
                if (statusEl) statusEl.textContent = 'Save failed: ' + (data.error || resp.status);
            }
        } catch (e) {
            if (statusEl) statusEl.textContent = 'Save failed';
        }
    }
```

- [ ] **Step 5: Export `saveRemoteAccess` and `updateCertMode` via `window`**

In the window exports block at the bottom of `settings.js`, add:

```js
    window.saveRemoteAccess            = saveRemoteAccess;
    window.updateCertMode              = updateCertMode;
```

- [ ] **Step 6: Commit the JS changes**

```bash
git add nginx/settings.js
git commit -m "feat(settings): add Remote Access load/save logic to settings.js"
```

- [ ] **Step 7: Browser verification**

Start the stack and open the Settings tab:

1. **Panel loads**: The Remote Access panel appears (admin only). Current `.env` values are populated — `st-remote-enabled` toggle reflects `REMOTE_ACCESS_ENABLED`, hostname/ports/cert-mode fields match their `.env` values.
2. **Cert mode toggle**: Selecting "Let's Encrypt" shows the email + staging fields; selecting "BYO" or "Self-signed" hides them.
3. **Status badge**: Shows "active · letsencrypt" (green) or "disabled" (muted) correctly.
4. **Save**: Change a value (e.g. toggle remote access). Click "Save remote access". The status line shows "Saved ✓ — restart nginx to apply" for 6 seconds. Reload the page — the changed value persists.
5. **Backend validation**: Try setting `remote_hostname` to `http://bad hostname:port` and saving — the API returns an error and the status line shows it.

---

## Self-Review

**Spec coverage:**

| Requirement | Covered |
|-------------|---------|
| Dashboard settings panel for `REMOTE_ACCESS_ENABLED` | ✓ toggle `st-remote-enabled` |
| Hostname, port, cert mode, LE email | ✓ all fields in HTML + JS |
| Dashboard status badge | ✓ `st-remote-status` badge in panel title |
| Setup wizard integration | ✗ Deferred — marked in issue as "currently skipped entirely" |

**Backend note:** The `.env` write goes through `POST /api/pelicula/settings` which is already guarded by `auth.GuardAdmin` + `httputil.RequireLocalOriginStrict`. No backend changes needed.

**Restart notice:** The panel footer and save status both remind the user to run `pelicula restart nginx` — nginx is the service that reads `REMOTE_ACCESS_ENABLED` and activates the remote vhost.
