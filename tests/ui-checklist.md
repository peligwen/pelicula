# Pelicula UI Checklist

Manual / MCP-driven UI walkthroughs. Each entry can be executed by Claude via Chrome MCP browser tools (or Playwright MCP) at verify-time. Not part of automated CI; this is a curated regression list.

**Phase 2 HTTP coverage (automated, run via `pelicula verify`):**
Catalog, Jobs, Users, and Settings tabs are covered by HTTP-level smoke tests in `tests/sweep-catalog.sh`, `tests/sweep-jobs.sh`, `tests/sweep-users.sh`, and `tests/sweep-settings.sh`. Only the wizard walkthrough below requires a browser session.

## Setup wizard end-to-end

**Preconditions:**
- Stack up via `pelicula up`.
- Wizard in fresh-setup state: `.env` must not exist (first run), OR delete `.env` and run `pelicula up` to launch the setup container stack on port 7354.
  - To re-enter wizard state on an already-configured system: `rm .env && pelicula up` (stack is temporarily torn down and the setup wizard container starts instead).
  - Note: `pelicula reset-config` (no args) is a soft-reset for service configs — it does **not** re-enter setup mode. It requires `pelicula up` afterward, which starts the main stack (not the wizard).
- Browser MCP tool of choice loaded (Playwright MCP: load `browser_navigate`, `browser_snapshot`, `browser_click`, `browser_console_messages`, `browser_evaluate`; Chrome MCP: call `tabs_context_mcp` first).

**Steps:**

1. Navigate to `http://localhost:7354/`. Expect: wizard renders at step 1 with heading "ProtonVPN Setup". Step indicator shows step 1 ("1 VPN") active.

2. Step 1 — VPN key (optional): The page shows a password field labeled "WireGuard Private Key". Either:
   - Leave the field blank and click the link "Skip — I'll set up VPN later" to proceed without VPN, OR
   - Enter a valid 44-character base64 WireGuard private key ending in `=` and click the button labeled "Next".
   Expect: step 2 panel renders with heading "Storage".

3. Step 2 — Storage paths: The page shows two required text fields:
   - "Config Directory" (id: `path-config`) — pre-filled with the detected config path.
   - "Media Directory" (id: `path-media`) — pre-filled with the detected media path.
   Verify both fields are non-empty (auto-detected defaults should populate them).

4. Click the button labeled "Next" at the bottom of the step 2 panel. Expect: step 3 panel renders with heading "Ready to Go" and a summary table showing the Config and Media directory values, plus the VPN status ("Skipped" or "Netherlands ✓").

5. Step 3 — Confirm: Review the summary. Optionally expand "Additional Libraries" to add custom libraries. Click the button labeled "Launch Pelicula". Expect: page transitions to a "Starting Up" panel with a spinner and status text "Waiting for services...".

6. Wait for startup polling: The page polls `/api/pelicula/health` every 2 seconds for up to 3 minutes. Expect: status text updates to "Services are up! Redirecting...", then browser redirects to `http://localhost:7354/register`.

7. Registration page: Expect: `/register` loads the registration page (heading "PELICULA", subtitle "Setup"). Complete account creation (username/password fields, "Create account" button). Expect: redirect to dashboard at `http://localhost:7354/`.

8. Dashboard: Expect: dashboard renders normally at `/` — no wizard, shows the search bar, service health cards, and tab bar (search, catalog, jobs, storage, users, settings).

**Pass criteria:** every step advances as described; step 2 "Next" click renders step 3 immediately (no error, no hang); final registration step lands on the dashboard at `/`.
