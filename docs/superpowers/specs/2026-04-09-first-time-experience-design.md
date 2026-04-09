# First-Time Experience Redesign

## Problem

The current first-time flow requires two commands (`pelicula setup`, then `pelicula up`), generates an admin password the user must copy from a terminal screen, and forces configuration choices (auth toggle, port, VPN country) that should have sensible defaults. The flow should be: clone, run one command, open browser, register, configure, use.

## Desired Flow

```
git clone … && cd pelicula
./pelicula up
# open http://localhost:7354
# → Step 1: Register admin (username + password)
# → Step 2: Storage paths
# → Step 3: VPN (optional, skippable)
# → Step 4: Confirm & Launch
# → auto-login → dashboard
```

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| CLI entry point | `pelicula up` only | One command. `setup` removed. |
| Admin credentials | User-chosen username + password | No generated password to copy from terminal. Optional "Generate for me" with copy button. |
| Auth mode | Always `jellyfin` | If you're registering an admin, auth is on. Toggle removed. |
| VPN | Optional, skippable | Stack works without VPN for import/process/play. |
| VPN country | Hardcoded Netherlands | Best P2P availability. Not a user choice. |
| Port | Not in wizard | Default 7354, changeable in `.env` later. |
| No-VPN stack | Compose profiles | gluetun + qBittorrent + Prowlarr in `vpn` profile. Clean — no failing containers. |
| Post-setup transition | Auto-redirect + auto-login | Wizard polls for stack health, authenticates with stored credentials, redirects to dashboard. |
| Library/work paths | Single "Media Directory" field | Advanced toggle reveals separate fields for power users. |

## Architecture Changes

### 1. CLI — `cmd/pelicula/`

**`main.go`** — Remove `case "setup"` branch entirely.

**`cmd_up.go`** — When no `.env` exists:
1. Detect platform (existing `Detect()`)
2. Start setup containers via `docker-compose.setup.yml`
3. Open browser to `http://localhost:7354/`
4. Poll for `.env` creation (existing logic from `cmdSetup`)
5. Tear down setup containers
6. Continue with normal `up` flow

This absorbs `cmdSetup()` logic directly into `cmdUp()`.

**`cmd_setup.go`** — Delete file (or gut it — keep `setupDirs()` if it's called from `cmdUp`).

**`env.go`**:
- Add `JELLYFIN_ADMIN_USER` to `envKeyOrder`
- Remove `PELICULA_AUTH` from wizard-settable fields (always write `"jellyfin"`)
- `PELICULA_PORT` gets default `"7354"`, not collected in wizard

**`compose.go`**:
- When `WIREGUARD_PRIVATE_KEY` is empty/missing, do NOT pass `--profile vpn`
- When set, pass `--profile vpn` to `docker compose up`
- Existing `--profile apprise` logic is unchanged

### 2. Setup Wizard API — `middleware/setup.go`

**New endpoint: `GET /api/pelicula/setup/generate-password`**
- Returns `{"password": "<15-char readable password>"}`
- Uses existing `genPassword()` logic (needs to be moved from CLI helpers to middleware, or duplicated — it's 10 lines)

**Modified: `POST /api/pelicula/setup`**

New request shape:
```json
{
  "admin_username": "gwen",
  "admin_password": "user-chosen-or-generated",
  "config_dir": "./config",
  "media_dir": "~/media",
  "library_dir": "",
  "work_dir": "",
  "wireguard_key": "",
  "vpn_skipped": true
}
```

Validation:
- `admin_username`: required, non-empty, alphanumeric + underscore, no spaces
- `admin_password`: required, minimum 8 characters
- `config_dir`, `media_dir`: required, valid paths
- `library_dir`, `work_dir`: optional — if empty, both default to `media_dir`
- `wireguard_key`: optional — if empty, `vpn_skipped` must be true

Generated values (unchanged):
- `PROCULA_API_KEY` (32-char random)
- `WEBHOOK_SECRET` (32-char hex)

`.env` output changes:
- `JELLYFIN_ADMIN_USER=<username>` (new)
- `JELLYFIN_PASSWORD=<password>` (user-chosen instead of generated)
- `PELICULA_AUTH=jellyfin` (always, no toggle)
- `PELICULA_PORT=7354` (always default)
- `SERVER_COUNTRIES=Netherlands` (always)
- `WIREGUARD_PRIVATE_KEY=` (empty string when skipped)

### 3. Setup Wizard UI — `nginx/setup.html`

Complete rewrite of the wizard steps:

**Step 1: Create Admin Account**
- Username field
- Password field with "Generate one for me" link
  - Calls `GET /api/pelicula/setup/generate-password`
  - Shows generated password in visible monospace field with copy button
  - Warning: "Save this password — it won't be shown again after setup"
  - "Generate another" link for re-rolling
- Confirm password field (hidden when using generated password)
- Client-side validation: passwords match, min 8 chars, username non-empty

**Step 2: Storage**
- Config Directory (pre-filled from `/api/pelicula/setup/detect`)
- Media Directory (pre-filled from detect)
- Collapsible "Advanced: separate library & work paths"
  - Finished Media path
  - Downloads & Processing path
  - One-line explanation: "Split media across disks — e.g. finished media on a large HDD, downloads on a fast SSD"

**Step 3: VPN (Optional)**
- WireGuard Private Key field
- Warning: "Do NOT enable Moderate NAT when generating"
- Static display: "Server: Netherlands (best for P2P availability)"
- Collapsible "Why ProtonVPN? Do you work for them?" explaining Gluetun supports 30+ providers, ProtonVPN has reliable WireGuard + port forwarding + P2P
- "Skip — I'll set up VPN later" link

**Step 4: Confirm & Launch**
- Summary: Admin username, config path, media path, VPN status
- VPN status shown as green "Netherlands checkmark" or orange "Skipped" with explanation
- When skipped: "Downloading disabled. Import, processing, and streaming still work."
- "Launch Pelicula" button

**Post-submit transition:**
1. Show loading screen: "Starting Pelicula..."
2. Store admin credentials in `sessionStorage` (cleared on tab close)
3. Poll `GET /api/pelicula/health` every 2 seconds
4. When health returns non-setup status (stack is up):
   - `POST /api/pelicula/auth/login` with stored credentials
   - On success: clear `sessionStorage`, redirect to `/`
   - Dashboard shows welcome banner: "Welcome, gwen! Your media stack is ready."

### 4. Docker Compose — `docker-compose.yml`

Add `profiles: [vpn]` to three services:
- `gluetun`
- `qbittorrent` (already runs on gluetun's network, inherently depends on it)
- `prowlarr` (indexer — talks to external sites, should be behind VPN for privacy)

All other services (nginx, pelicula-api, procula, sonarr, radarr, jellyfin, bazarr) start unconditionally.

### 5. Middleware Startup — `middleware/main.go`, `middleware/jellyfin.go`

**`jellyfin.go`**:
- Read `JELLYFIN_ADMIN_USER` env var (default: `"admin"`)
- Use it instead of hardcoded `"admin"` when creating the Jellyfin admin user via startup wizard

**`main.go`**:
- Remove the `PELICULA_AUTH` toggle — always initialize Jellyfin auth
- Auth is always on

**`autowire.go`**:
- Check if `WIREGUARD_PRIVATE_KEY` is set
- If empty: skip download client wiring (no qBittorrent to wire), skip Prowlarr wiring
- Still wire: root folders, Jellyfin, webhooks

### 6. Dashboard — `nginx/dashboard.js`

**VPN status awareness:**
- Add VPN status to an existing endpoint (e.g., `GET /api/pelicula/status` or `GET /api/pelicula/auth/check`)
- When VPN is not configured, show a persistent banner:
  - "VPN not configured — downloading is disabled. [Set up VPN →]"
  - Link goes to settings page or a VPN setup section

**Welcome banner:**
- On first login after setup (can check `sessionStorage` flag or a server-side "first login" marker), show:
  - "Welcome, {username}! Your media stack is ready."

### 7. `.env` Migration — `cmd/pelicula/env.go`

For existing users upgrading:
- If `JELLYFIN_ADMIN_USER` is missing, add it with default `"admin"`
- If `PELICULA_AUTH` is `"off"`, leave it — don't force auth on existing installations
- Migration only applies defaults for missing keys, never overwrites

## Services by Profile

| Service | Always | VPN profile |
|---------|--------|-------------|
| nginx | yes | |
| pelicula-api | yes | |
| procula | yes | |
| jellyfin | yes | |
| sonarr | yes | |
| radarr | yes | |
| bazarr | yes | |
| gluetun | | yes |
| qbittorrent | | yes |
| prowlarr | | yes |

## Files to Modify

| File | Change |
|------|--------|
| `cmd/pelicula/main.go` | Remove `setup` command routing |
| `cmd/pelicula/cmd_setup.go` | Delete or extract `setupDirs()` |
| `cmd/pelicula/cmd_up.go` | Absorb setup flow for first run |
| `cmd/pelicula/env.go` | Add `JELLYFIN_ADMIN_USER`, migration |
| `cmd/pelicula/compose.go` | Conditional `--profile vpn` |
| `middleware/setup.go` | New fields, generate-password endpoint, always-jellyfin |
| `middleware/main.go` | Remove auth toggle, always init auth |
| `middleware/jellyfin.go` | Use `JELLYFIN_ADMIN_USER` env var |
| `middleware/autowire.go` | Skip VPN-dependent wiring when no key |
| `nginx/setup.html` | Rewrite wizard: register → paths → VPN → confirm |
| `nginx/dashboard.js` | VPN banner, welcome message |
| `docker-compose.yml` | Add `profiles: [vpn]` to gluetun, qbittorrent, prowlarr |
| `docker-compose.setup.yml` | No changes expected |

## Verification

1. **Fresh install (with VPN):** `./pelicula up` → wizard → register admin → set paths → enter WireGuard key → confirm → auto-login → dashboard with all services
2. **Fresh install (no VPN):** Same flow but skip VPN step → dashboard shows VPN banner → only import/process/play available
3. **Existing install upgrade:** `pelicula up` with existing `.env` → migration adds `JELLYFIN_ADMIN_USER=admin` → no behavior change
4. **Generated password:** Click "Generate" → password shown → copy button works → can log in with generated password after setup
5. **Advanced paths:** Toggle advanced → set different library/work dirs → verify mounts in `docker inspect`
6. **Auth always on:** No way to disable auth through the wizard → `PELICULA_AUTH` always `jellyfin` in new installs
