# Security Policy

## Reporting a Vulnerability

Please report security issues **privately** via GitHub Security Advisories:
<https://github.com/peligwen/pelicula/security/advisories/new>

Do not file public issues for suspected vulnerabilities. I aim to acknowledge
reports within a few days and will coordinate disclosure with you.

## Threat Model

Pelicula is a **LAN-first** personal media stack. The design assumes:

- The admin dashboard (port **7354**) is reachable only from a trusted local
  network. It is not hardened for exposure to the public internet.
- Service-to-service communication between containers relies on Docker's
  private networks and an IP-based auth bypass inside each *arr app. This is
  intentional and is why `./pelicula up` enforces
  `AuthenticationRequired=DisabledForLocalAddresses` on every start.
- Auth (`PELICULA_AUTH=off|password|users`) is an optional convenience layer
  for shared households, not a defense against a determined network attacker.

**Peligrosa — remote Jellyfin access (opt-in):** When
`REMOTE_ACCESS_ENABLED=true`, a second nginx vhost exposes **only Jellyfin**
on a separate port. No admin routes (`/sonarr`, `/radarr`, `/api/pelicula`,
etc.) are reachable from the remote vhost. The remote vhost enforces TLS
1.2+, HSTS, per-IP rate limiting on the auth endpoint, `return 444` on
unknown Host headers, and hard 403 on `/System/Logs` and `/System/Info`. The
admin port 7354 is never exposed by the remote vhost.

## Known Limitations

These are documented here so you can make an informed decision before
enabling auth-sensitive features:

- **Password hashing** uses salted SHA-256 (`middleware/auth.go`). SHA-256 is
  fast on GPUs and is not an ideal password hash. Use a strong, unique
  password. A migration to bcrypt or argon2id is on the roadmap.
- **WireGuard private key** and API keys are stored in `.env` in plaintext on
  the host. `./pelicula setup` `chmod 600`s the file, but anyone with host
  access can read it.
- **`WEBHOOK_SECRET`** (Radarr/Sonarr → Procula import webhook) is optional
  for backward compatibility with installs that predate the setup wizard's
  secret generation. Fresh installs get a random secret automatically;
  nginx additionally restricts the webhook endpoint to Docker-internal
  networks.
- **Auth rate limiter** is in-memory, per-IP, and resets on middleware
  restart. It protects against online brute force, not offline cracking of
  a leaked users file.
- **Self-signed HTTPS** on the LAN breaks Chrome (Chrome blocks JS on
  self-signed cert pages). The default LAN setup is HTTP; enable the
  Peligrosa remote vhost if you need TLS.

## What Is Not in Scope

- Hardening for multi-tenant or public-internet admin exposure.
- Defense against host compromise (anyone with shell access wins).
- Protection of torrent traffic beyond the VPN kill-switch (all torrent
  traffic is forced through Gluetun's Wireguard tunnel; if the tunnel drops,
  qBittorrent loses internet).
