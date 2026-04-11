# Peligrosa — User Interaction Safety Layer

Users are dangerous. Remote users are especially so. Peligrosa is the conceptual layer that captures every place where Pelicula touches input from outside the operator trust boundary — authentication, user management, invite redemption, viewer requests, incoming webhooks, the folder browser escape guard, CSRF origin checks, and the hardened remote Jellyfin vhost.

The name is intentional: **peligrosa** (Spanish, *dangerous*). Every piece of code under this umbrella is a place where the system could be abused. Treat it with appropriate care.

---

## Threat Model

Pelicula is **LAN-first**. The design baseline assumes:

- The admin dashboard (`:7354`) is reachable only from a trusted local network. It is not hardened for public internet exposure.
- Service-to-service communication between containers relies on Docker's private networks and an IP-based auth bypass inside each *arr app (`AuthenticationRequired=DisabledForLocalAddresses`). This is intentional — ``pelicula up`` enforces it on every start.
- Auth is always on. The only unauthenticated path is the loopback auto-session for requests from the host machine — not a defense against a determined network attacker, but not an open door either.

**Peligrosa remote vhost (opt-in):** When `REMOTE_ACCESS_ENABLED=true`, a second nginx vhost exposes **only Jellyfin** on a separate hardened port. No admin routes (`/sonarr`, `/radarr`, `/api/pelicula`, etc.) are reachable from the remote vhost. The admin port 7354 is never exposed.

**What is not in scope:**
- Hardening for multi-tenant or public-internet admin exposure.
- Defense against host compromise — anyone with shell access wins.
- Protection of torrent traffic beyond the VPN kill-switch. All torrent traffic exits through Gluetun's WireGuard tunnel; if the tunnel drops, qBittorrent loses internet.

---

## The Safety Surface

### Authentication (`middleware/peligrosa/`)

Auth is always on. Credentials are verified against Jellyfin's `/Users/AuthenticateByName`. Roles stored in `/config/pelicula/roles.json`. Jellyfin admins automatically get `admin` role. No passwords stored by Pelicula — Jellyfin is the authority.

**Sessions:** In-memory `map[token]session`, `pelicula_session` HttpOnly cookie, `SameSite=Lax`. 24-hour session lifetime; 10-minute cleanup goroutine removes expired sessions and stale rate-limit entries.

**Login rate limiter:** 5 failed attempts per IP in a 5-minute sliding window → HTTP 429. In-memory; resets on restart.

**Guards:** `Guard` (viewer+), `GuardManager` (manager+), `GuardAdmin` (admin). Wired per-route in `main.go`.

### Loopback Auto-Session (`middleware/peligrosa/loopback.go`)

Requests from the host machine are granted a transient admin session without a cookie — no login required from the box running the stack. This is the host-machine convenience path; LAN and remote clients must authenticate normally.

The grant is **transient**: no session cookie is set. Every request from the host re-runs the check.

Three gates must all pass:

1. **Trusted upstream CIDR** — `r.RemoteAddr` must fall within `httputil.TrustedUpstreamCIDR` (default `172.16.0.0/12`, the Docker bridge range). A direct connection to `middleware:8181` bypassing nginx would fail here.
2. **Loopback `X-Real-IP`** — nginx sets `X-Real-IP` from `$remote_addr` on every request, overwriting any client-supplied value. Only a client that connected to nginx on `127.0.0.1` / `::1` has a loopback `X-Real-IP`.
3. **Loopback `Host`** — the `Host` header must be `localhost`, `127.0.0.1`, or `::1`. LAN clients connecting via `http://<lan-ip>:7354/` have the LAN IP in `Host`, not loopback.

**Why it's safe:** nginx rewrites `X-Real-IP` from `$remote_addr` unconditionally — a spoofed header from a LAN client is overwritten before it reaches middleware. `Host` must be loopback, which LAN clients never send. Gate 1 ensures the request came through nginx (docker bridge peer), not a direct socket connection to middleware.

**Override the upstream CIDR:** set `PELICULA_UPSTREAM_CIDR` in `.env` if your Docker bridge is outside `172.16.0.0/12` (uncommon).

**Remote role capping (defense-in-depth):** The remote nginx vhost injects `X-Pelicula-Remote: true` on all proxy blocks. The middleware's `effectiveRole()` caps any session to `viewer` when this header is present, regardless of the stored role. This prevents a compromised admin credential from escalating via the remote vhost. The LAN vhost strips the header (`proxy_set_header X-Pelicula-Remote ""`) to prevent spoofing. `HandleCheck` returns `"remote": true/false` so the dashboard can adapt.

### Open Registration (`middleware/peligrosa/register.go`)

Optional LAN-only public registration without invite tokens. Controlled by `PELICULA_OPEN_REGISTRATION` in `.env` (default `false`), toggleable via settings UI.

When enabled, `/register` without a `?t=` token shows a registration form. `POST /api/pelicula/register` creates a Jellyfin user and assigns `viewer` role. Rate-limited by IP (reuses auth limiter). LAN-only via `requireLocalOriginStrict` — not exposed on the remote vhost.

### CSRF Origin Guard (`middleware/peligrosa/auth.go`)

`isLocalOrigin(origin string) bool` — returns true if the `Origin` header is an RFC1918/localhost address. Parses as URL to prevent substring-match bypasses. Returns false for empty strings.

Two middleware wrappers enforce the policy per-route in `main.go`:
- **`requireLocalOriginStrict`** — rejects state-mutating requests (POST/PUT/PATCH/DELETE) with empty or non-local Origin. Used for admin-only endpoints where only a LAN browser should ever mutate state: `/settings`, `/settings/reset`, `/setup`.
- **`requireLocalOriginSoft`** — allows empty Origin (API/curl callers) but rejects non-empty non-local Origins. Used for endpoints where programmatic callers are valid: `/users`, `/users/`, `/invites`, `/invites/`.

Safe methods (GET/HEAD) always pass through. `admin_ops.go` uses `requireLocalOriginStrict` for all state-mutating requests — these paths require both a valid admin session and a local Origin.

### Users and User Management (`middleware/jellyfin.go`)

Pelicula manages users through Jellyfin. `CreateJellyfinUser` creates the Jellyfin account and maps the Jellyfin user ID to a Pelicula role in `roles.json`. Delete removes both.

Roles: `viewer` (read + request), `manager` (search + add + pause downloads), `admin` (full access + user management).

### Invites (`middleware/peligrosa/invites.go`)

32-byte crypto/rand token, base64url-encoded (43 chars), stored at `/config/pelicula/invites.json` (0600). Not HMAC-signed — a future Peligrosa upgrade will add HMAC-signing so tokens are verifiable without a database lookup. See [roadmap](#roadmap).

`Invite{Token, Label, ExpiresAt, MaxUses, Uses, Revoked, RedeemedBy[]}`. States: active / revoked / expired / exhausted.

Redemption creates a Jellyfin user via the bridge. The `/api/pelicula/invites/:token/redeem` endpoint is public but invite-gated; admin management endpoints are admin-only.

### Request Queue (`middleware/peligrosa/requests.go`)

Viewer-created media requests: viewers submit via the dashboard search; admins approve or deny with configurable Radarr/Sonarr quality profiles. Apprise notifies on state change.

The trust split is structural in the route table: `GET/POST /api/pelicula/requests` is `Guard` (any logged-in user); `POST/DELETE /api/pelicula/requests/{id}` is `GuardAdmin`.

### Webhook Secret (`middleware/hooks.go`)

`WEBHOOK_SECRET` in `.env` — generated by the setup wizard, appended as `?secret=` to the auto-wired Radarr/Sonarr webhook URL. `handleImportHook` uses `crypto/subtle.ConstantTimeCompare` to reject mismatched secrets. Missing secret = no check (backward compat for pre-wizard installs).

The import webhook endpoint is also restricted to Docker-internal networks in `nginx.conf` — a second line of defense.

Path allowlist (`isUnderPrefixes` / `isAllowedWebhookPath`): validates that reported file paths in incoming webhook payloads are under known media roots before forwarding to Procula.

### Folder Browser Guard (`middleware/library.go`)

`handleBrowse` powers the import wizard's server-side directory listing. Resolves symlinks via `filepath.EvalSymlinks`, then re-checks the resolved path against the allowlist roots to prevent path-traversal escape. Callers cannot navigate outside the configured browse roots even through symlinks.

### Remote Jellyfin Vhost

`nginx/remote.conf.template` is rendered to `${CONFIG_DIR}/nginx/remote.conf` on every ``pelicula up`` when `REMOTE_ACCESS_ENABLED=true`.

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

Enable via `the Settings UI → 8) Remote access`.

---

## Known Limitations

- **WireGuard private key** and API keys are stored in `.env` in plaintext on the host. ``pelicula up` (first-run setup)` sets `chmod 600`, but anyone with host access can read it.
- **Auth rate limiter** is in-memory, per-IP, and resets on middleware restart. Protects against online brute force.
- **Invite tokens** are random (32 bytes, 256-bit entropy) — token validity requires a database lookup. This is intentional: brute-force enumeration is infeasible, and revocation/exhaustion cannot be made stateless without a blocklist anyway.
- **Self-signed HTTPS** breaks Chrome on the LAN (Chrome blocks JS). Default LAN setup uses HTTP; use Peligrosa remote vhost for TLS.
- **`WEBHOOK_SECRET`** is optional for backward compatibility. Fresh installs get a random secret from setup; nginx additionally restricts the endpoint to Docker-internal networks.
- **CSRF guards** use `requireLocalOriginStrict` / `requireLocalOriginSoft` wrappers wired per-route in `main.go`. `admin_ops.go` uses `requireLocalOriginStrict` — state-mutating admin ops require both a valid admin session and a local Origin.
- **Remote role capping** relies on the `X-Pelicula-Remote` header injected by the remote nginx vhost. The LAN vhost strips it. If nginx is bypassed and the middleware is accessed directly, the header won't be present and the cap won't apply (defense-in-depth only — middleware is not directly exposed).

---

## Reading the Code

| File | What it owns |
|------|-------------|
| `middleware/peligrosa/auth.go` | Sessions, login rate limiter, `isLocalOrigin` CSRF guard, `Guard`/`GuardManager`/`GuardAdmin`, remote role capping (`effectiveRole`) |
| `middleware/peligrosa/loopback.go` | Loopback auto-session (3-gate check: trusted CIDR + loopback X-Real-IP + loopback Host) |
| `middleware/peligrosa/register.go` | Open LAN registration (optional, `PELICULA_OPEN_REGISTRATION`) |
| `middleware/peligrosa/invites.go` | Invite token lifecycle, redemption |
| `middleware/peligrosa/requests.go` | Viewer request queue, approval/denial flow |
| `middleware/peligrosa/routes.go` | `peligrosa.RegisterRoutes` — the subpackage's public API surface |
| `middleware/jellyfin.go` | `handleUsers`, `handleUsersWithID`, `CreateJellyfinUser` |
| `middleware/pipeline.go` | Unified pipeline aggregation (downloads + procula jobs + monitoring requests) |
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

- **HMAC invite tokens** — sign tokens so validity is verifiable without a DB read
- ~~**Central CSRF middleware**~~ — shipped: `requireLocalOriginStrict` / `requireLocalOriginSoft` wired per-route in `main.go`
- ~~**`middleware/peligrosa/` subpackage**~~ — shipped: auth, invites, requests, and webhook validation extracted into `middleware/peligrosa/` with an explicit API surface (`peligrosa.RegisterRoutes`)
