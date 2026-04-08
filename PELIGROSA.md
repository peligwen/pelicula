# Peligrosa — User Interaction Safety Layer

Users are dangerous. Remote users are especially so. Peligrosa is the conceptual layer that captures every place where Pelicula touches input from outside the operator trust boundary — authentication, user management, invite redemption, viewer requests, incoming webhooks, the folder browser escape guard, CSRF origin checks, and the hardened remote Jellyfin vhost.

The name is intentional: **peligrosa** (Spanish, *dangerous*). Every piece of code under this umbrella is a place where the system could be abused. Treat it with appropriate care.

---

## Threat Model

Pelicula is **LAN-first**. The design baseline assumes:

- The admin dashboard (`:7354`) is reachable only from a trusted local network. It is not hardened for public internet exposure.
- Service-to-service communication between containers relies on Docker's private networks and an IP-based auth bypass inside each *arr app (`AuthenticationRequired=DisabledForLocalAddresses`). This is intentional — `./pelicula up` enforces it on every start.
- `PELICULA_AUTH` is an optional convenience layer for shared households, not a defense against a determined network attacker.

**Peligrosa remote vhost (opt-in):** When `REMOTE_ACCESS_ENABLED=true`, a second nginx vhost exposes **only Jellyfin** on a separate hardened port. No admin routes (`/sonarr`, `/radarr`, `/api/pelicula`, etc.) are reachable from the remote vhost. The admin port 7354 is never exposed.

**What is not in scope:**
- Hardening for multi-tenant or public-internet admin exposure.
- Defense against host compromise — anyone with shell access wins.
- Protection of torrent traffic beyond the VPN kill-switch. All torrent traffic exits through Gluetun's WireGuard tunnel; if the tunnel drops, qBittorrent loses internet.

---

## The Safety Surface

### Authentication (`middleware/auth.go`)

Three modes via `PELICULA_AUTH`:

| Mode | Behavior |
|------|----------|
| `off` (default) | All requests pass through. No session required. |
| `password` / `true` | Single shared password; caller is always admin. Legacy alias: `true` stored in `.env`, accepted at runtime. |
| `users` | Full user model from `/config/pelicula/users.json`. Roles: `viewer`, `manager`, `admin`. |

**Sessions:** In-memory `map[token]session`, `pelicula_session` HttpOnly cookie, `SameSite=Lax`. 24-hour session lifetime; 10-minute cleanup goroutine removes expired sessions and stale rate-limit entries.

**Password hashing:** `sha256v2:SALT:HASH` — `sha256(SALT + ":" + username + ":" + plaintext)`. Legacy unsalted SHA-256 accepted on read, never written. bcrypt/argon2 migration is a [Peligrosa roadmap item](#roadmap).

**Login rate limiter:** 5 failed attempts per IP in a 5-minute sliding window → HTTP 429. In-memory; resets on restart.

**Guards:** `Guard` (viewer+), `GuardManager` (manager+), `GuardAdmin` (admin). Wired per-route in `main.go`.

### CSRF Origin Guard (`middleware/auth.go`)

`isLocalOrigin(origin string) bool` — rejects requests where the `Origin` header is empty or is not an RFC1918/localhost address. Parses as URL and checks hostname to prevent substring-match bypasses.

Two inline patterns used across the codebase:
- **Strict** (`!isLocalOrigin(origin)`) — rejects empty origin. Used by settings, setup, admin ops — places where only an operator at a LAN browser should ever POST.
- **Soft** (`origin != "" && !isLocalOrigin(origin)`) — allows missing origin (API/curl callers), rejects browser cross-origin. Used by user CRUD and invite create — where programmatic callers are valid.

### Users and User Management (`middleware/jellyfin.go`, `middleware/auth.go`)

Pelicula uses a **dual-write bridge**: `CreateJellyfinUser` creates the Jellyfin account first, then appends to `users.json`. Delete reverses in the same order. If the Jellyfin step fails, the local record is not written.

User schema (`users.json`):
```json
{ "username": "alice", "password": "sha256v2:SALT:HASH", "role": "viewer" }
```

Roles: `viewer` (read + request), `manager` (search + add + pause downloads), `admin` (full access + user management).

### Invites (`middleware/invites.go`)

32-byte crypto/rand token, base64url-encoded (43 chars), stored at `/config/pelicula/invites.json` (0600). Not HMAC-signed — a future Peligrosa upgrade will add HMAC-signing so tokens are verifiable without a database lookup. See [roadmap](#roadmap).

`Invite{Token, Label, ExpiresAt, MaxUses, Uses, Revoked, RedeemedBy[]}`. States: active / revoked / expired / exhausted.

Redemption creates a Jellyfin user via the bridge. The `/api/pelicula/invites/:token/redeem` endpoint is public but invite-gated; admin management endpoints are admin-only.

### Request Queue (`middleware/requests.go`)

Viewer-created media requests: viewers submit via the dashboard search; admins approve or deny with configurable Radarr/Sonarr quality profiles. Apprise notifies on state change.

The trust split is structural in the route table: `GET/POST /api/pelicula/requests` is `Guard` (any logged-in user); `POST/DELETE /api/pelicula/requests/{id}` is `GuardAdmin`.

### Webhook Secret (`middleware/hooks.go`)

`WEBHOOK_SECRET` in `.env` — generated by the setup wizard, appended as `?secret=` to the auto-wired Radarr/Sonarr webhook URL. `handleImportHook` uses `crypto/subtle.ConstantTimeCompare` to reject mismatched secrets. Missing secret = no check (backward compat for pre-wizard installs).

The import webhook endpoint is also restricted to Docker-internal networks in `nginx.conf` — a second line of defense.

Path allowlist (`isUnderPrefixes` / `isAllowedWebhookPath`): validates that reported file paths in incoming webhook payloads are under known media roots before forwarding to Procula.

### Folder Browser Guard (`middleware/library.go`)

`handleBrowse` powers the import wizard's server-side directory listing. Resolves symlinks via `filepath.EvalSymlinks`, then re-checks the resolved path against the allowlist roots to prevent path-traversal escape. Callers cannot navigate outside the configured browse roots even through symlinks.

### Remote Jellyfin Vhost

`nginx/remote.conf.template` is rendered to `${CONFIG_DIR}/nginx/remote.conf` on every `./pelicula up` when `REMOTE_ACCESS_ENABLED=true`.

**Hardening:**
- `return 444` catch-all on both ports drops requests with unknown Host headers (prevents IP scanning)
- `GET/POST /System/Logs` and `/System/Info` hard-deny with 403
- HSTS + TLS 1.2+ enforced
- Per-IP auth rate-limiting: `limit_req_zone jf_auth` on `AuthenticateByName` (5/min, burst 3)
- `client_max_body_size 1m`
- No admin paths (`/sonarr`, `/radarr`, `/api/pelicula`, etc.) are proxied — Jellyfin only

**Ports:**

| Port | Purpose |
|------|---------|
| `REMOTE_HTTPS_PORT` (default 8920) | Hardened Jellyfin HTTPS |
| `REMOTE_HTTP_PORT` (default 80) | ACME challenge + 301 redirect to HTTPS |

**DNS:** Let's Encrypt cannot issue certs for raw IPs. A real DNS hostname is required. DDNS is handled externally (router DDNS, ddclient, Cloudflare, etc.).

**Certbot reload:** certbot runs with `pid: service:nginx` (shared PID namespace). After cert renewal, the deploy-hook copies resolved cert files to `${CONFIG_DIR}/certs/remote` and signals nginx master with `kill -HUP $(pgrep -o nginx)` — zero-downtime reload.

**Environment variables:**

| Variable | Default | Notes |
|----------|---------|-------|
| `REMOTE_ACCESS_ENABLED` | `false` | Master switch |
| `REMOTE_HOSTNAME` | — | e.g. `home.duckdns.org`. Required when enabled. |
| `REMOTE_HTTP_PORT` | `80` | HTTP port for ACME challenge + redirect |
| `REMOTE_HTTPS_PORT` | `8920` | HTTPS port for Jellyfin |
| `REMOTE_CERT_MODE` | `letsencrypt` | `letsencrypt` \| `byo` \| `self-signed` |
| `REMOTE_LE_EMAIL` | — | Required for `letsencrypt` mode |
| `REMOTE_LE_STAGING` | `false` | Use LE staging CA for testing |

Enable via `./pelicula configure → 8) Remote access`.

---

## Known Limitations

- **Password hashing** uses salted SHA-256. SHA-256 is fast on GPUs and is not an ideal password KDF. Use a strong, unique password. A bcrypt/argon2id migration is on the [roadmap](#roadmap).
- **WireGuard private key** and API keys are stored in `.env` in plaintext on the host. `./pelicula setup` sets `chmod 600`, but anyone with host access can read it.
- **Auth rate limiter** is in-memory, per-IP, and resets on middleware restart. Protects against online brute force, not offline cracking of a leaked users file.
- **Invite tokens** are random but not HMAC-signed — token validity requires a database lookup. HMAC signing is a [roadmap item](#roadmap).
- **Self-signed HTTPS** breaks Chrome on the LAN (Chrome blocks JS). Default LAN setup uses HTTP; use Peligrosa remote vhost for TLS.
- **`WEBHOOK_SECRET`** is optional for backward compatibility. Fresh installs get a random secret from setup; nginx additionally restricts the endpoint to Docker-internal networks.
- **CSRF guards** are inline per-handler, not a central middleware. Central middleware is a [roadmap item](#roadmap).

---

## Reading the Code

| File | What it owns |
|------|-------------|
| `middleware/auth.go` | Sessions, password hashing, login rate limiter, `isLocalOrigin` CSRF guard, `Guard`/`GuardManager`/`GuardAdmin` |
| `middleware/invites.go` | Invite token lifecycle, redemption |
| `middleware/requests.go` | Viewer request queue, approval/denial flow |
| `middleware/jellyfin.go` | `handleUsers`, `handleUsersWithID`, `CreateJellyfinUser` dual-write bridge |
| `middleware/hooks.go` | Webhook secret validation, path allowlist |
| `middleware/library.go` | `handleBrowse` folder browser + symlink escape prevention |
| `middleware/settings.go` | Settings read/write (uses `isLocalOrigin` for strict CSRF guard) |
| `middleware/setup.go` | Setup wizard (uses `isLocalOrigin`) |
| `middleware/admin_ops.go` | Container restart/rebuild ops (uses `isLocalOrigin`) |
| `middleware/main.go` | Route table — trust level wired per endpoint |
| `nginx/remote.conf.template` | Remote vhost nginx config |
| `nginx/remote.conf.template` → `${CONFIG_DIR}/nginx/remote.conf` | Rendered at startup |

---

## Roadmap

See [ROADMAP.md — Peligrosa initiative](ROADMAP.md#peligrosa-initiative) for the full backlog. Active items:

- **bcrypt/argon2id** — replace SHA-256 password KDF
- **HMAC invite tokens** — sign tokens so validity is verifiable without a DB read
- **Central CSRF middleware** — one `requireLocalOrigin` wrapper wired per-route in `main.go` instead of inline checks across 5 files
- **`middleware/peligrosa/` subpackage** — extract the trust boundary into its own Go package with an explicit API surface
- **SSO** — layer Jellyfin/Plex auth over the Phase B user model (deferred, no timeline)
