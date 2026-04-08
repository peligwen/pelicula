# Contributing to Pelicula

Pelicula is a LAN-first, clone-and-run media stack for personal use. It is a hobby project, not an enterprise product. Contributions are welcome, but keep that context in mind: changes should stay simple, self-contained, and easy for a solo maintainer to reason about at 11pm.

## Scope

The project accepts contributions that:
- Fix bugs in the bash CLI, Go middleware, Go processing pipeline, or container configuration
- Add features described in [ROADMAP.md](ROADMAP.md) (Active section)
- Improve documentation accuracy
- Add or improve test coverage

**Out of scope:** third-party service integrations not already in the stack, changes to the threat model, breaking changes to existing CLI flags or `.env` keys.

## Dev Setup

You need: Docker, Go 1.23+, bash, and a working ProtonVPN Plus account (for full e2e) or a stub `.env` (for unit tests only).

```bash
# Run Go unit tests for both services
make test

# Run with race detector
make test-race

# Run code coverage report
make test-cover

# Full end-to-end test — spins an isolated stack on port 7399, no VPN needed
./pelicula test
```

Go modules are stdlib-only. Neither `middleware/go.mod` nor `procula/go.mod` has external `require` entries — keep it that way.

## Code Conventions

- **Go**: standard library only. No external dependencies. `go vet ./...` must pass clean.
- **Bash**: `pelicula` is a single-file script. Add subcommands as bash functions; don't split into separate files. Shellcheck (`-S warning`) must pass.
- **Tests**: every new Go function that makes a decision should have a unit test. Table-driven tests are preferred. Do not mock the database (there isn't one — use temp dirs).
- **Commit messages**: `type(scope): short description` in imperative form. Types: `feat`, `fix`, `refactor`, `docs`, `test`, `ci`. Examples from history: `feat(procula): dual-subtitle stacking pipeline stage`, `refactor(cli): reset-config all regenerates .env`.

## Pull Requests

- One logical change per PR. A PR that adds a feature and refactors unrelated code will be asked to split.
- Include tests for new behaviour.
- Run `make test` before opening a PR. CI will also run `go vet`, `go test -race`, and shellcheck.
- Reference the ROADMAP item if your PR implements one.

## Security

See [SECURITY.md](SECURITY.md) for the vulnerability disclosure policy. Pelicula is LAN-first — do not open issues or PRs that assume an internet-facing threat model unless that is explicitly described in PELIGROSA.md.

## License

By contributing, you agree that your contributions are licensed under the [AGPL-3.0 License](LICENSE).
