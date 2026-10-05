# Contributing to Pelicula

Pelicula is a solo project — one developer, maintained in personal time. The repo is public for transparency and as a reference, not as a call for contributors. Issues and bug reports are welcome; PRs are unlikely to be merged without prior discussion.

If you want to adapt Pelicula for your own setup, the AGPL-3.0 license permits that. If you want to propose a change, open an issue first.

---

Pelicula is a LAN-first, clone-and-run media stack for personal use. It is a hobby project, not an enterprise product. Any contributions should stay simple, self-contained, and easy for a solo maintainer to reason about at 11pm.

## Scope

The project accepts contributions that:
- Fix bugs in the Go CLI, the Go server, the dashboard, or the container configuration
- Bring back an item from the deferred list in [ROADMAP.md](docs/ROADMAP.md), in the shape it describes: behind a compose profile or a flag that is off by default
- Improve documentation accuracy
- Add or improve test coverage

**Out of scope:** third-party service integrations not already in the stack, changes to the threat model, breaking changes to existing CLI flags or `.env` keys, and anything on the deferred list in ROADMAP.md outside the shape described there. The guiding rule is one source of truth per fact: if Sonarr, Radarr, qBittorrent, Jellyfin or `.env` already owns something, read it from there rather than mirroring it.

## Dev Setup

You need: Docker with the Compose v2 plugin, Go 1.25, and bash. A ProtonVPN Plus account is only needed to run the stack with the VPN; the unit tests and the integration test run without one.

```bash
make test        # go test -race ./...            (no Docker needed)
make vet         # go vet ./... and a gofmt check
make lint        # staticcheck
make e2e         # integration test: starts a real stack on port 7399 (needs Docker)
make playwright  # browser specs against a running stack (see tests/playwright/README.md)
```

## Code Conventions

- **Go**: one module, two binaries. `modernc.org/sqlite` is the only permitted external dependency; the CLI in `cmd/pelicula` is stdlib-only and must not import `internal/`. `gofmt -l .`, `go vet ./...` and `go test ./...` must pass clean.
- **Bash**: the `pelicula` wrapper must pass shellcheck (`-S warning`).
- **Tests**: every package has unit tests using `httptest` fakes and `store.OpenMemory()`. Nothing in `go test ./...` may require a running stack. Table-driven tests are preferred.
- **Frontend**: vanilla JS, no build step, no inline scripts, no CDNs.
- **Commit messages**: `type(scope): short description` in imperative form. Types: `feat`, `fix`, `refactor`, `docs`, `test`, `ci`.

## Pull Requests

- One logical change per PR. A PR that adds a feature and refactors unrelated code will be asked to split.
- Include tests for new behaviour.
- Run `make test` and `make vet` before opening a PR. CI also runs staticcheck, shellcheck and the integration test.
- When a change adds code, look for code, a setting, a test or a doc paragraph it makes unnecessary and remove it in the same PR.

## Security

See [SECURITY.md](SECURITY.md) for the vulnerability disclosure policy and the [auth model](docs/ARCHITECTURE.md#auth-model) section of docs/ARCHITECTURE.md for the threat model. Pelicula is LAN-first — do not open issues or PRs that assume an internet-facing threat model.

## License

By contributing, you agree that your contributions are licensed under the [AGPL-3.0 License](LICENSE).
