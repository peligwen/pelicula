# Peligrosa — User Interaction Safety Layer

Users are dangerous. Remote users are especially so. Peligrosa is the conceptual layer that captures every place where Pelicula touches input from outside the operator trust boundary — authentication, user management, invite redemption, viewer requests, incoming webhooks, the folder browser escape guard, CSRF origin checks, and the hardened remote Jellyfin vhost.

The name is intentional: **peligrosa** (Spanish, *dangerous*). Every piece of code under this umbrella is a place where the system could be abused. Treat it with appropriate care.

---

## Threat Model

Pelicula is **LAN-first**. The design baseline assumes:

- The admin dashboard (`:7354`) is reachable only from a trusted local network. It is not hardened for public internet exposure.
- Service-to-service communication between containers relies on Docker's private networks and an IP-based auth bypass inside each *arr app (`AuthenticationRequired=DisabledForLocalAddresses`). This is intentional — ``pelicula up`` enforces it on every start.
- Auth is always on. The only unauthenticated path is the loopback auto-session for requests from the host machine — not a defense against a determined network attacker, but not an open door either.

**Peligrosa remote vhost (opt-in):** When `REMOTE_MODE=portforward`, a second nginx vhost exposes **only Jellyfin** on a separate hardened port. No admin routes (`/sonarr`, `/radarr`, `/api/pelicula`, etc.) are reachable from the remote vhost. The admin port 7354 is never exposed.

**Invariant — remote vhost must never proxy `/api/pelicula/`:** Neither `nginx/remote.conf.template` nor `nginx/remote-simple.conf.template` contains a `/api/pelicula/` location block. This must remain true. The middleware's `effectiveRole()` caps remote-session roles to `viewer` when `X-Pelicula-Remote: true` is present, but this is defense-in-depth only — it does not block viewer-gated endpoints. Endpoint exposure is the primary gate and it lives at nginx, not in the middleware. Adding a `/api/pelicula/` block to either remote template would expose admin and manager endpoints to the internet.

**What is not in scope:**
- Hardening for multi-tenant or public-internet admin exposure.
- Defense against host compromise — anyone with shell access wins.
- Protection of torrent traffic beyond the VPN kill-switch. All torrent traffic exits through Gluetun's WireGuard tunnel; if the tunnel drops, qBittorrent loses internet.

---

## The Safety Surface

### Authentication (`middleware/internal/peligrosa/`)

Auth is always on. Credentials are verified against Jellyfin's `/Users/AuthenticateByName`. Roles stored in `pelicula.db` (SQLite, `roles` table). Jellyfin admins automatically get `admin` role. No passwords stored by Pelicula — Jellyfin is the authority. (On first startup after upgrade, existing `roles.json` is auto-migrated into SQLite and renamed to `roles.json.migrated`.)

**Sessions:** In-memory `map[token]session`, `pelicula_session` HttpOnly cookie, `SameSite=Lax`. 24-hour session lifetime; 10-minute cleanup goroutine removes expired sessions and stale rate-limit entries.

**Login rate limiter:** 5 failed attempts per IP in a 5-minute sliding window → HTTP 429. In-memory; resets on restart.

**Guards:** `Guard` (viewer+), `GuardManager` (manager+), `GuardAdmin` (admin). Wired per-route in `main.go`.

### Loopback Auto-Session (`middleware/internal/peligrosa/loopback.go`)

Requests from the host machine are granted a transient admin session without a cookie — no login required from the box running the stack. This is the host-machine convenience path; LAN and remote clients must authenticate normally.

The grant is **transient**: no session cookie is set. Every request from the host re-runs the check.

Three gates must all pass:

1. **Trusted upstream CIDR** — `r.RemoteAddr` must fall within `httputil.TrustedUpstreamCIDR` (default `172.16.0.0/12`, the Docker bridge range). A direct connection to `middleware:8181` bypassing nginx would fail here.
2. **Loopback `X-Real-IP`** — nginx sets `X-Real-IP` from `$remote_addr` on every request, overwriting any client-supplied value. Only a client that connected to nginx on `127.0.0.1` / `::1` has a loopback `X-Real-IP`.
3. **Loopback `Host`** — the `Host` header must be `localhost`, `127.0.0.1`, or `::1`. LAN clients connecting via `http://<lan-ip>:7354/` have the LAN IP in `Host`, not loopback.

**Why it's safe:** nginx rewrites `X-Real-IP` from `$remote_addr` unconditionally — a spoofed header from a LAN client is overwritten before it reaches middleware. `Host` must be loopback, which LAN clients never send. Gate 1 ensures the request came through nginx (docker bridge peer), not a direct socket connection to middleware.

**Override the upstream CIDR:** set `PELICULA_UPSTREAM_CIDR` in `.env` if your Docker bridge is outside `172.16.0.0/12` (uncommon).

**Remote role capping (defense-in-depth):** The remote nginx vhost injects `X-Pelicula-Remote: true` on all proxy blocks. The middleware's `effectiveRole()` caps any session to `viewer` when this header is present, regardless of the stored role. This prevents a compromised admin credential from escalating via the remote vhost. The LAN vhost strips the header (`proxy_set_header X-Pelicula-Remote ""`) to prevent spoofing. `HandleCheck` returns `"remote": true/false` so the dashboard can adapt.

### Open Registration (`middleware/internal/peligrosa/register.go`)

Optional LAN-only public registration without invite tokens. Controlled by `PELICULA_OPEN_REGISTRATION` in `.env` (default `false`), toggleable via settings UI.

When enabled, `/register` without a `?t=` token shows a registration form. `POST /api/pelicula/register` creates a Jellyfin user and assigns `viewer` role. Rate-limited by IP (reuses auth limiter). LAN-only via `requireLocalOriginStrict` — not exposed on the remote vhost.

### CSRF Origin Guard (`middleware/internal/peligrosa/auth.go`)

`isLocalOrigin(origin string) bool` — returns true if the `Origin` header is an RFC1918/localhost address. Parses as URL to prevent substring-match bypasses. Returns false for empty strings.

Two middleware wrappers enforce the policy per-route in `main.go`:
- **`requireLocalOriginStrict`** — rejects state-mutating requests (POST/PUT/PATCH/DELETE) with empty or non-local Origin. Used for admin-only endpoints where only a LAN browser should ever mutate state: `/settings`, `/settings/reset`, `/setup`.
- **`requireLocalOriginSoft`** — allows empty Origin (API/curl callers) but rejects non-empty non-local Origins. Used for endpoints where programmatic callers are valid: `/users`, `/users/`, `/invites`, `/invites/`.

Safe methods (GET/HEAD) always pass through. `admin_ops.go` uses `requireLocalOriginStrict` for all state-mutating requests — these paths require both a valid admin session and a local Origin.

### Users and User Management (`middleware/internal/app/jellyfin/`)

Pelicula manages users through Jellyfin. `CreateJellyfinUser` creates the Jellyfin account and maps the Jellyfin user ID to a Pelicula role in `pelicula.db`. Delete removes both.

Roles: `viewer` (read + request), `manager` (search + add + pause downloads), `admin` (full access + user management).

### Invites (`middleware/internal/peligrosa/invites.go`)

32-byte crypto/rand token, base64url-encoded (43 chars), stored in `pelicula.db` (SQLite, `invites` table). Not HMAC-signed — a future Peligrosa upgrade will add HMAC-signing so tokens are verifiable without a database lookup. See [roadmap](#roadmap). (On first startup after upgrade, existing `invites.json` is auto-migrated into SQLite and renamed to `invites.json.migrated`.)

`Invite{Token, Label, ExpiresAt, MaxUses, Uses, Revoked, RedeemedBy[]}`. States: active / revoked / expired / exhausted.

Redemption creates a Jellyfin user via the bridge. The `/api/pelicula/invites/:token/redeem` endpoint is public but invite-gated; admin management endpoints are admin-only.

### Request Queue (`middleware/internal/peligrosa/requests.go`)

Viewer-created media requests: viewers submit via the dashboard search; admins approve or deny with configurable Radarr/Sonarr quality profiles. Apprise notifies on state change.

The trust split is structural in the route table: `GET/POST /api/pelicula/requests` is `Guard` (any logged-in user); `POST/DELETE /api/pelicula/requests/{id}` is `GuardAdmin`.

### Webhook Secret (`middleware/internal/app/hooks/`)

`WEBHOOK_SECRET` in `.env` — generated by the setup wizard. The auto-wired Radarr/Sonarr webhook URL does not embed the secret; instead, `wireImportWebhook()` injects it as the `X-Webhook-Secret` request header. `HandleImportHook` uses `crypto/subtle.ConstantTimeCompare` to reject mismatched secrets. Missing secret = no check (backward compat for pre-wizard installs).

The import webhook endpoint is also restricted to Docker-internal networks in `nginx.conf` — a second line of defense.

Path allowlist (`isUnderPrefixes` / `isAllowedWebhookPath`): validates that reported file paths in incoming webhook payloads are under known media roots before forwarding to Procula.

### Folder Browser Guard (`middleware/internal/app/library/`)

`handleBrowse` powers the import wizard's server-side directory listing. Resolves symlinks via `filepath.EvalSymlinks`, then re-checks the resolved path against the allowlist roots to prevent path-traversal escape. Callers cannot navigate outside the configured browse roots even through symlinks.

### Remote Jellyfin Vhost

`nginx/remote.conf.template` is rendered to `${CONFIG_DIR}/nginx/remote.conf` on every ``pelicula up`` when `REMOTE_MODE=portforward`.

**Hardening:**
- `return 444` catch-all on both ports drops requests with unknown Host headers (prevents IP scanning)
- `GET/POST /System/Logs` and `/System/Info` hard-deny with 403
- HSTS + TLS 1.2+ enforced (HSTS is full mode only — requires a real hostname)
- Per-IP auth rate-limiting: `limit_req_zone jf_auth` on `AuthenticateByName` (5/min, burst 3)
- `client_max_body_size 1m`
- `Content-Security-Policy` header restricts script/style/media sources on both full and simple mode
- No admin paths (`/sonarr`, `/radarr`, `/api/pelicula`, etc.) are proxied — Jellyfin only

**Simple mode** (no hostname): When `REMOTE_MODE=portforward` and `REMOTE_HOSTNAME` is not set, Peligrosa enters simple mode. A self-signed cert is auto-generated (`CN=pelicula-remote`). nginx listens on `REMOTE_HTTPS_PORT` (default 8920) with `server_name _` — no HTTP port, no ACME, no certbot. Clients connect via the host's LAN IP (or port-forwarded external IP) at `https://<ip>:8920/`. TV apps and native Jellyfin clients accept self-signed certs; browsers will show a certificate warning.

Security isolation is identical to full mode: Jellyfin-only proxy, rate-limited auth, `/System/Logs` and `/System/Info` hard-denied, `X-Pelicula-Remote: true` injected (role capping applies). Only difference: no HSTS (requires a real hostname).

Enable via Settings UI → Remote access. Leave the hostname field blank.

**Ports:**

| Port | Purpose |
|------|---------|
| `REMOTE_HTTPS_PORT` (default 8920) | Hardened Jellyfin HTTPS (both modes) |
| `REMOTE_HTTP_PORT` (default 80) | ACME challenge + 301 redirect to HTTPS (full mode only) |

**DNS:** Let's Encrypt cannot issue certs for raw IPs. A real DNS hostname is required for full mode. Simple mode (no hostname) uses a self-signed cert and needs no DNS. DDNS for full mode is handled externally (router DDNS, ddclient, Cloudflare, etc.).

**Certbot reload:** certbot runs with `pid: service:nginx` (shared PID namespace). After cert renewal, the deploy-hook copies resolved cert files to `${CONFIG_DIR}/certs/remote` and signals nginx master with `kill -HUP $(pgrep -o nginx)` — zero-downtime reload.

**Environment variables:**

| Variable | Default | Notes |
|----------|---------|-------|
| `REMOTE_MODE` | `disabled` | `disabled` \| `portforward` \| `cloudflared` \| `tailscale`. The settings UI toggle controls the `portforward` value; `cloudflared` and `tailscale` are configured manually (see [Alternative Remote Access Modes](#alternative-remote-access-modes)). |
| `REMOTE_HOSTNAME` | — | Optional. Blank = simple mode (self-signed cert, LAN-IP access, no DNS needed). Set for full mode (Let's Encrypt / BYO cert). |
| `REMOTE_HTTP_PORT` | `80` | HTTP port for ACME challenge + redirect |
| `REMOTE_HTTPS_PORT` | `8920` | HTTPS port for Jellyfin |
| `REMOTE_CERT_MODE` | `letsencrypt` | `letsencrypt` \| `byo` \| `self-signed` |
| `REMOTE_LE_EMAIL` | — | Required for `letsencrypt` mode |
| `REMOTE_LE_STAGING` | `false` | Use LE staging CA for testing |

Enable via `the Settings UI → 8) Remote access`.

---

## Alternative Remote Access Modes

The port-forward remote vhost (above) is one option. Two additional tunnel-based modes are available as alternatives, selected via the `REMOTE_MODE` env var:

| `REMOTE_MODE` | Mechanism | Required env vars |
|---------------|-----------|-------------------|
| `portforward` | nginx remote vhost on a published port (settings UI toggle writes this value) | See "Remote Jellyfin Vhost" above |
| `cloudflared` | Cloudflare Tunnel — no open ports required | `CLOUDFLARE_TUNNEL_TOKEN` |
| `tailscale` | Tailscale sidecar — devices on the tailnet reach nginx directly | `TAILSCALE_AUTH_KEY`, optionally `TAILSCALE_HOSTNAME` |

### Cloudflare Tunnel (`REMOTE_MODE=cloudflared`)

1. Create a tunnel in the [Cloudflare Zero Trust dashboard](https://one.dash.cloudflare.com/) and copy the tunnel token.
2. Set `REMOTE_MODE=cloudflared` and `CLOUDFLARE_TUNNEL_TOKEN=<token>` in `.env`.
3. Run `pelicula up` — a `cloudflared` container joins the `pelicula` Docker network and connects outbound to Cloudflare's edge.

**Routing:** The `cloudflared` container proxies all of nginx (`:7354`). Configure the public hostname ingress rules in the Cloudflare dashboard to route **only** your desired path (e.g. `https://jellyfin.example.com → http://nginx:7354/jellyfin/`) — no admin routes should be pointed at the tunnel. nginx's existing auth gates still apply for any path that does reach it, but Cloudflare-side routing is the primary exposure control here.

**No open ports required.** The tunnel is an outbound-only connection from `cloudflared` to Cloudflare's network. No ports need to be forwarded on your router.

### Tailscale (`REMOTE_MODE=tailscale`)

1. Generate an auth key in the [Tailscale admin console](https://login.tailscale.com/admin/settings/keys) (reusable key recommended for restarts).
2. Set `REMOTE_MODE=tailscale`, `TAILSCALE_AUTH_KEY=<key>`, and optionally `TAILSCALE_HOSTNAME=pelicula` in `.env`.
3. Run `pelicula up` — a `tailscale` sidecar joins the `pelicula` Docker network and registers with your tailnet.

**Routing:** The Tailscale node exposes nginx on its tailnet IP. Family members with the Tailscale app installed can reach `http://<tailnet-ip>:7354/jellyfin/` from any device. Access is restricted to devices enrolled in your tailnet — Tailscale's ACL policies provide the security boundary.

**Security note:** Tailscale exposes all of nginx to tailnet members (including `/api/pelicula/`). If your tailnet has members you don't fully trust, configure Tailscale ACL policies to restrict access to port 7354, or rely on Pelicula's own auth layer (every path is auth-gated for non-loopback, non-LAN clients).

### Security surface comparison

All three modes ultimately proxy the same nginx instance. The difference is which paths are reachable from the internet:

- **Port-forward mode:** nginx's remote vhost is a separate hardened listener that exposes only `/jellyfin/`. Admin routes are unreachable by design.
- **Cloudflare Tunnel:** The tunnel proxies all of nginx. Cloudflare dashboard ingress rules are the exposure gate — configure them to expose only `/jellyfin/`.
- **Tailscale:** All of nginx is reachable on the tailnet. Tailscale enrollment and ACL policies are the exposure gate. All Pelicula routes remain auth-gated.

For the strictest isolation from the public internet, port-forward mode with the hardened remote vhost remains the recommended approach. For simplicity (no port forwarding, no DNS setup), Cloudflare Tunnel or Tailscale are good alternatives when you control who is on the tailnet / tunnel.

---

## Deferred hardening backlog

These items are tracked but deferred — low risk for a home LAN stack, but worth picking up
if the stack is exposed to the public internet.

- **fail2ban / nginx IP banning** — ban IPs after repeated 401s on the remote vhost.
  Trigger condition: adding a second authenticated public surface.
- **GeoIP country allowlist** — reject requests from outside allowed countries at nginx.
  Trigger condition: repeated unwanted traffic from outside the owner's country.
- **CSP audit** — review and tighten Content-Security-Policy headers across all vhosts.
  Trigger condition: before shipping any user-generated content rendering.

---

## Known Limitations

- **WireGuard private key** and API keys are stored in `.env` in plaintext on the host. ``pelicula up` (first-run setup)` sets `chmod 600`, but anyone with host access can read it.
- **Auth rate limiter** is in-memory, per-IP, and resets on middleware restart. Protects against online brute force.
- **Invite tokens** are random (32 bytes, 256-bit entropy) — token validity requires a database lookup. This is intentional: brute-force enumeration is infeasible, and revocation/exhaustion cannot be made stateless without a blocklist anyway.
- **Self-signed HTTPS** breaks Chrome on the LAN (Chrome blocks JS). Default LAN setup uses HTTP; use Peligrosa remote vhost for TLS.
- **`WEBHOOK_SECRET`** is optional for backward compatibility. Fresh installs get a random secret from setup; it is delivered via the `X-Webhook-Secret` request header (not a URL query parameter). nginx additionally restricts the endpoint to Docker-internal networks.
- **CSRF guards** use `requireLocalOriginStrict` / `requireLocalOriginSoft` wrappers wired per-route in `main.go`. `admin_ops.go` uses `requireLocalOriginStrict` — state-mutating admin ops require both a valid admin session and a local Origin.
- **Remote role capping** relies on the `X-Pelicula-Remote` header injected by the remote nginx vhost. The LAN vhost strips it. If nginx is bypassed and the middleware is accessed directly, the header won't be present and the cap won't apply (defense-in-depth only — middleware is not directly exposed).

---

## Reading the Code

| File | What it owns |
|------|-------------|
| `middleware/internal/peligrosa/auth.go` | Sessions, login rate limiter, `isLocalOrigin` CSRF guard, `Guard`/`GuardManager`/`GuardAdmin`, remote role capping (`effectiveRole`) |
| `middleware/internal/peligrosa/loopback.go` | Loopback auto-session (3-gate check: trusted CIDR + loopback X-Real-IP + loopback Host) |
| `middleware/internal/peligrosa/register.go` | Open LAN registration (optional, `PELICULA_OPEN_REGISTRATION`) |
| `middleware/internal/peligrosa/invites.go` | Invite token lifecycle, redemption |
| `middleware/internal/peligrosa/requests.go` | Viewer request queue, approval/denial flow |
| `middleware/internal/peligrosa/routes.go` | `peligrosa.RegisterRoutes` — the subpackage's public API surface |
| `middleware/internal/app/jellyfin/` | `HandleUsers`, `HandleUsersWithID`, `CreateJellyfinUser` |
| `middleware/internal/app/hooks/` | Webhook secret validation (`X-Webhook-Secret`), path allowlist, Procula forwarding |
| `middleware/internal/app/library/` | `HandleBrowse` folder browser + symlink escape prevention |
| `middleware/internal/app/settings/` | Settings read/write (strict local-origin CSRF guard) |
| `middleware/internal/app/setup/` | Setup wizard |
| `middleware/internal/app/adminops/` | Container restart/rebuild ops |
| `middleware/internal/app/router/router.go` | Route table — trust level wired per endpoint |
| `nginx/remote.conf.template` | Full-mode remote vhost nginx config (envsubst template; rendered to `${CONFIG_DIR}/nginx/remote.conf` on startup when `REMOTE_HOSTNAME` is set) |
| `nginx/remote-simple.conf.template` | Simple-mode remote vhost nginx config (static file; written verbatim when `REMOTE_HOSTNAME` is not set) |

---

## Roadmap

See [ROADMAP.md — Shipped](ROADMAP.md#shipped) for the full backlog. Active items:

- **HMAC invite tokens** — sign tokens so validity is verifiable without a DB read
- ~~**Central CSRF middleware**~~ — shipped: `requireLocalOriginStrict` / `requireLocalOriginSoft` wired per-route in `main.go`
- ~~**`middleware/internal/peligrosa/` subpackage**~~ — shipped: auth, invites, requests, and webhook validation extracted into `middleware/internal/peligrosa/` with an explicit API surface (`peligrosa.RegisterRoutes`)
