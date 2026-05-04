# Cut the Pelicula Remote Vhost Layer

**Status:** Approved (vision); planning + implementation pending.
**Owner:** peligwen
**Date:** 2026-05-04

## Problem

Pelicula carries substantial code, settings UI, and operational machinery to expose itself to the internet through its own nginx + Let's Encrypt certbot pipeline. The intended deployment model has narrowed: only Jellyfin will be exposed externally, on port 8920, via direct Docker port publishing handled by DSM / router port-forward — not by Pelicula.

The remote-vhost layer is therefore paying ongoing cost (maintenance, docs, tests, attack surface, deployment failure modes) for a code path that won't be used. Some NAS deployment friction likely originates here — REMOTE_MODE selection, cert mode dispatch, hostname validation, certbot orchestration — all of which can fail confusingly.

## Goals

- Pelicula becomes **LAN-only** (port 7354). Tailscale is an operator choice, not a Pelicula feature.
- Jellyfin is the **sole internet-exposed surface** on port 8920, via direct Docker port publishing. The operator handles port forwarding via DSM / router. **Jellyfin's built-in HTTPS** terminates TLS.
- ~1,200+ LOC of code, 2 nginx templates, 3 env vars, the certbot service, and most of `docs/PELIGROSA.md` retire as a single coherent group.

## Non-goals

- Not redesigning the dashboard, auth, invites, sessions, or the request queue. Those stay as-is.
- Not changing LAN-side routing through `nginx.conf` on port 7354.
- Not preserving backward compat for existing `REMOTE_MODE=portforward` deployments — single operator. Migration prints a notice and strips the vars.
- Not bundling other pruning candidates (procula transcoding, Apprise) into this cut — they're separate.

## Constraints

- LAN-side `pelicula up` must still work end-to-end without any remote setup.
- LAN viewers must still reach Jellyfin via `/jellyfin/` on port 7354.
- `PELIGROSA.md` threat model must be honestly rewritten — model becomes "trusted LAN + invitees with their own devices."
- CLAUDE.md and ARCHITECTURE.md updated.

## Locked decisions (from open questions)

1. **Jellyfin TLS:** Use Jellyfin's built-in HTTPS for external access. Doc covers the toggle in Jellyfin's admin.
2. **Cert cleanup:** On migration, delete `certs/acme-webroot/`, `certs/letsencrypt/`, `certs/remote/`.
3. **Tailscale hint:** One paragraph in docs — "for invitee dashboard access without exposing 7354, Tailscale is the simplest path." Not a Pelicula feature, just a hint.
4. **External Jellyfin port:** 8920 (Jellyfin's HTTPS default). No port translation.

## What gets cut

**Code (~1,200 LOC):**
- `nginx/remote.conf.template` (89), `nginx/remote-simple.conf.template` (50)
- `cmd/pelicula/remote.go` + `remote_test.go` (~500)
- Certbot compose overlay, ACME webroot handling, LE email/staging plumbing
- `settings/handler.go` chunks: `REMOTE_MODE`, `REMOTE_CERT_MODE`, `REMOTE_HOSTNAME`, `applyRemoteModeChange`, `remoteAccessEnabledFromMode`
- `cmd/pelicula/env.go` migration (lines 199–214) + REMOTE_HOSTNAME entry
- `cmd/pelicula/envfile.go` entries for REMOTE_MODE / REMOTE_HOSTNAME
- `cmd_up.go` remote-vhost orchestration
- Verify suite + e2e bits exercising remote setup

**UI:**
- Settings page tabs / inputs for remote access mode, cert mode, hostname
- JS gated on `REMOTE_MODE`

**Config / state:**
- The 3 env vars deleted at first run via one-time migration
- `certs/acme-webroot/`, `certs/letsencrypt/`, `certs/remote/` cleaned up

**Docs:**
- ~200 of 252 lines from `docs/PELIGROSA.md` (rewritten to a tight LAN+Jellyfin-direct threat model with a Tailscale hint)
- CLAUDE.md / ARCHITECTURE.md updates

## What stays

- The whole `peligrosa` package (5,189 LOC) — auth, users, sessions, invites, roles, request queue. Needed because invitees still log in to the LAN dashboard.
- `WEBHOOK_SECRET` — internal *arr→pelicula-api auth on the docker network.
- CSRF / `IsLocalOrigin` guards — same-LAN browsers can still be tricked into cross-origin requests.
- LAN-side `nginx.conf` on port 7354 — unchanged.
- `remoteconfig/jellyfin_network.go` (114 LOC) — adjusts what URL Jellyfin advertises but the file stays.

## What gets added (small)

- One-time migration in `pelicula up`: if `.env` has REMOTE_MODE / REMOTE_HOSTNAME / REMOTE_CERT_MODE, print a notice, delete them, clean up cert dirs, continue.
- Compose: confirm Jellyfin's port 8920 is published on host 8920.
- Doc section: "To expose Jellyfin: enable Jellyfin's built-in HTTPS, port-forward 8920 to your NAS. Pelicula does not handle remote exposure. (For LAN-quality dashboard access remotely, install Tailscale.)"

## Phased approach

Single coherent cut, ordered within one branch:

1. Migration logic that strips REMOTE_* vars + cleans cert dirs + prints notice (lands first so upgrade path is clean)
2. Delete `cmd/pelicula/remote.go` + tests
3. Delete `nginx/remote*.template`
4. Strip settings handler / env vars / settings UI
5. Update compose overlays (drop certbot service; verify Jellyfin port published on 8920)
6. Rewrite PELIGROSA.md; update CLAUDE.md, ARCHITECTURE.md
7. Run `pelicula verify`; remove now-broken remote tests

## Out of scope but on the radar

Other pruning targets discussed but not part of this cut:

- **Procula transcoding pipeline** — not bulk transcoding; FFmpeg/FFprobe/validate/transcode could retire (storage monitoring + catalog + queue stay).
- **Apprise + notification plumbing** — never used.
- **Request queue** — kept as future-useful even though unused now; revisit if it bitrots.
