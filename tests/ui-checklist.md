# Pelicula UI Checklist

Manual / MCP-driven UI walkthroughs. Each entry can be executed by Claude via Chrome MCP browser tools (or Playwright MCP) at verify-time. Not part of automated CI; this is a curated regression list.

## Audit notes (Phase 4)

- **HTTP-level coverage (automated, run via `pelicula verify`):** Catalog, Jobs, Users, and Settings tabs are covered by `tests/sweep-catalog.sh`, `tests/sweep-jobs.sh`, `tests/sweep-users.sh`, and `tests/sweep-settings.sh`.
- **Wizard walkthrough (this entry):** Audited and tightened in Phase 4. The setup wizard is a DOM state machine driven by JS — it advances panels, validates fields client-side, and polls `/api/pelicula/health` until services are up. The register page similarly mutates its heading/hint based on a `/api/pelicula/register/check` response. Neither flow is reproducible via curl; a real browser session is required.
- **Storage-explorer / import flow:** Inspected in Phase 4. The storage tab (`#storage-tab-explorer`, `data-testid="storage-explorer-section"`) hosts a multi-step JS state machine: file tree (`#browse-tree`) → `#btn-import` → 3-step modal (Match `#step-match`, Configure `#step-configure`, Apply `#step-apply`) with multiple API fetches. This qualifies for a browser-session entry. It is **not** added here yet because deterministic execution requires fixture media files staged at known paths in `WORK_DIR` before the browse tree has anything selectable — the current bash harness does not do that staging. DOM hooks are already in place (`data-testid` on `storage-explorer-section`, `browse-tree`, `btn-import`, `import-modal`, `btn-configure`, `apply-content`, `apply-nav`) and ready for a future entry once fixture staging is added to the harness.

---

## Setup wizard end-to-end

**Preconditions:**
- Stack up via `pelicula up`.
- Wizard in fresh-setup state: `.env` must not exist (first run), OR delete `.env` and run `pelicula up` to launch the setup container stack on port 7354.
  - To re-enter wizard state on an already-configured system: `rm .env && pelicula up` (stack is temporarily torn down and the setup wizard container starts instead).
  - Note: `pelicula reset-config` (no args) is a soft-reset for service configs — it does **not** re-enter setup mode. It requires `pelicula up` afterward, which starts the main stack (not the wizard).
- Browser MCP tool of choice loaded (Playwright MCP: load `browser_navigate`, `browser_snapshot`, `browser_click`, `browser_console_messages`, `browser_evaluate`; Chrome MCP: call `tabs_context_mcp` first).
- No `PELICULA_TEST_JELLYFIN_PASSWORD` or other Jellyfin credential env vars are required for the wizard — Jellyfin auth is configured after initial setup, not during it.

**Why this is browser-only (not HTTP-coverable):** The wizard is a single-page JS state machine that advances between panels (`#panel-1` → `#panel-2` → `#panel-3` → `#panel-done`), performs client-side validation, and polls `/api/pelicula/health` for up to 3 minutes. The register page mutates its own heading/hint based on a `register/check` API response. There is no server-rendered redirect or form-POST flow that curl can drive.

**Steps:**

1. Navigate to `http://localhost:7354/`. Expect: page title is "PELICULA — Setup"; the step indicator shows three steps labeled "1 VPN", "2 Storage", "3 Confirm"; step 1 is active (`data-step="1"` has class `active`); heading inside `#panel-1` reads "ProtonVPN Setup".

2. **Step 1 — VPN key (optional).** Panel `#panel-1` is active. The password input has `id="vpn-key"` and label text "WireGuard Private Key". Either:
   - Leave `#vpn-key` blank and click the link with exact text `Skip — I'll set up VPN later` (em-dash `—`, not a hyphen) to proceed without VPN. This calls `skipVPN()` which sets `vpnSkipped = true` and advances to step 2. **OR**
   - Enter a valid 44-character base64 WireGuard private key ending in `=` into `#vpn-key` and click the button labeled "Next" (`class="btn-primary"` in `#panel-1`). Leaving the field blank and clicking "Next" also works — the JS sets `vpnSkipped = true` automatically when the field is empty.

   Expect: `#panel-2` becomes active; heading reads "Storage"; step indicator shows step 1 as done and step 2 as active.

3. **Step 2 — Storage paths.** Panel `#panel-2` is active. Verify the following fields are pre-populated by the auto-detect call to `/api/pelicula/setup/detect`:
   - `id="path-config"` (label: "Config Directory") — pre-filled with detected config path.
   - `id="path-media"` (label: "Media Directory") — pre-filled with detected media path.
   - `id="published-url"` (label: "Jellyfin LAN URL") — optionally pre-filled; leave as detected or clear it.

   The `<details id="advanced-paths">` element (summary text: "Advanced: separate library & work paths") can be expanded to expose `id="path-library"` (Finished Media) and `id="path-work"` (Downloads & Processing); leave both blank unless testing split-disk paths.

   Expect: both `#path-config` and `#path-media` are non-empty (auto-detected defaults populated them).

4. Click the button labeled "Next" (`class="btn-primary"` in `#panel-2`, `onclick="goStep(3)"`). Expect: `#panel-3` becomes active immediately (no error, no hang); heading `id="confirm-heading"` reads "Ready to Go"; `#summary` contains a table with rows for Config, Media, and VPN — VPN cell reads "Skipped" (red/muted span) if VPN was skipped, or "Netherlands ✓" (green span) if a key was entered.

5. **Step 3 — Confirm.** Panel `#panel-3` is active. Optionally expand the "Additional Libraries" section by clicking the `<summary>` inside `<details id="setup-libraries-details">` (visible text: "Additional Libraries (optional)"). Built-in "Movies" and "TV Shows" entries are shown as read-only rows; custom libraries can be added using `id="setup-lib-name"`, the type/arr/processing `<select>` elements, and the "Add Library" button. Click the button `id="btn-submit"` (text: "Launch Pelicula") to submit.

   Expect: all step panels lose the `active` class; `#panel-done` becomes active; heading `id="done-heading"` reads "Starting Up"; `id="done-status"` reads "Waiting for services..."; a spinner is visible.

6. **Startup polling.** The page polls `/api/pelicula/health` every 2 seconds (up to 90 iterations = 3 minutes). Status element `id="done-status"` updates each tick to "Waiting for services... (Ns)". Expect: once health returns a non-setup status, `#done-status` updates to "Services are up! Redirecting..." and the browser navigates to `http://localhost:7354/register`.

7. **Registration page — Create Admin Account.** Page at `/register` loads with page title "Join Pelicula" and the PELICULA logo. Because this is an `initial_setup` redirect (no `?t=` token), the page calls `/api/pelicula/register/check`, gets `initial_setup: true`, and mutates the heading `.reg-heading` to "Create Admin Account" and the hint `.reg-hint` to "Create your admin account. You'll use these credentials to log in and manage everything." The registration form (`#reg-form-wrap`) is shown; the loading state (`#reg-loading`) is hidden.

   Fields:
   - `id="reg-username"` (label: "Username") — type the desired admin username; focus is set automatically.
   - `id="reg-password"` (label: "Password") — auto-populated by `suggestPassword()` and shown in plain text; override by typing if desired. "Generate password" button is `id="suggest-pw"`.
   - `id="reg-confirm"` (label: "Confirm password") — auto-populated to match; override if you changed the password field.

   Click `id="reg-submit"` (button text: "Create account"). On success the page auto-POSTs to `/api/pelicula/auth/login` with the same credentials and redirects to `http://localhost:7354/` (no manual login step).

   Expect: browser navigates to `http://localhost:7354/`.

8. **Dashboard.** Expect: dashboard at `/` renders normally — no wizard overlay; the search bar is visible; service health cards are displayed; the tab bar shows tabs for search, catalog, jobs, storage, users, and settings.

**Pass criteria:** every step advances as described; step 2 "Next" click renders step 3 immediately (no error, no hang); final registration submit auto-logs in and lands on the dashboard at `/`.
