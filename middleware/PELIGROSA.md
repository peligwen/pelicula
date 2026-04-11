# Peligrosa — Trust Boundary Files

These are the files in this directory that form the Peligrosa trust boundary: every place where input from outside the operator trust zone (remote users, invite redeemers, viewer requesters, external webhooks) crosses into the system.

For the full narrative, threat model, and known limitations, see [../docs/PELIGROSA.md](../docs/PELIGROSA.md).

## Files

| File | Surface |
|------|---------|
| `auth.go` | Sessions, password hashing, login rate limiter, `isLocalOrigin` CSRF guard, `Guard`/`GuardManager`/`GuardAdmin` |
| `invites.go` | Invite token lifecycle, redemption → Jellyfin user creation |
| `requests.go` | Viewer request queue; viewer/admin trust split |
| `jellyfin.go` | `handleUsers`, `handleUsersWithID`, `CreateJellyfinUser` dual-write bridge (user CRUD only — library/setup-wizard code in the same file is not part of this surface) |
| `hooks.go` | Webhook secret validation (`WEBHOOK_SECRET`), payload path allowlist |
| `library.go` | `handleBrowse` — symlink resolve + prefix re-check for folder browser |
| `settings.go` | Uses `isLocalOrigin` (strict pattern) for admin settings POSTs |
| `setup.go` | Uses `isLocalOrigin` (strict pattern) for setup wizard POST |
| `admin_ops.go` | Uses `isLocalOrigin` (strict pattern) for container restart/rebuild ops |
| `main.go` | Route table — trust level wired per endpoint via `Guard*` |

## What is NOT part of this surface

- `autowire.go` — internal service-to-service wiring, no user input
- `jellyfin.go` (library/setup sections) — library scan, Jellyfin setup wizard completion
- `procula.go`, `storage.go`, `search.go` — proxies to internal services
- Missing content watcher — no external input
