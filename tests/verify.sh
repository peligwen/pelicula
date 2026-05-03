#!/usr/bin/env bash
# tests/verify.sh — Pelicula regression verifier.
#
# Runs the automated HTTP-level smoke tests against a running Pelicula stack.
# Designed for post-deploy verification, either locally or against a remote NAS.
#
# Usage:
#   bash tests/verify.sh [--target HOST:PORT] [--skip-auth] [--suite SUITE,...]
#
#   # Local stack (default localhost:7354):
#   PELICULA_TEST_JELLYFIN_PASSWORD=<pw> PELICULA_TEST_JELLYFIN_USER=gwen \
#     bash tests/verify.sh
#
#   # Remote NAS:
#   PELICULA_TEST_JELLYFIN_PASSWORD=<pw> PELICULA_TEST_JELLYFIN_USER=gwen \
#     bash tests/verify.sh --target 192.168.1.143:7354
#
#   # Static-asset checks only (no Jellyfin auth required):
#   bash tests/verify.sh --skip-auth --suite bug2
#
# Suites (--suite is a comma-separated list; default: all):
#   bug2-storage      Storage tab static-asset + API smoke (tests/bug2-storage.sh)
#                     Supports --skip-auth (skips the authenticated storage endpoint check).
#   bug4-registration Open registration toggle round-trip  (tests/bug4-registration.sh)
#                     Requires PELICULA_TEST_JELLYFIN_PASSWORD; skipped under --skip-auth.
#   sweep-catalog     Catalog tab HTTP smoke: catalog list shape, items array, quality profiles
#                     Requires PELICULA_TEST_JELLYFIN_PASSWORD; skipped under --skip-auth.
#   sweep-jobs        Jobs tab HTTP smoke: pelicula/jobs groups+total, processing proxy,
#                     procula/jobs direct. Auth-free test C runs even under --skip-auth.
#                     Full run requires PELICULA_TEST_JELLYFIN_PASSWORD.
#   sweep-users       Users tab HTTP smoke: users array, auth/check fields, invites array.
#                     Auth-free test B (auth/check) runs even under --skip-auth.
#                     Full run requires PELICULA_TEST_JELLYFIN_PASSWORD.
#   sweep-settings    Settings tab HTTP smoke: key count, 4 safe round-trip checks.
#                     Requires PELICULA_TEST_JELLYFIN_PASSWORD; skipped under --skip-auth.
#
# Tests that require a running Jellyfin session need
#   PELICULA_TEST_JELLYFIN_PASSWORD + PELICULA_TEST_JELLYFIN_USER.
# Pass --skip-auth to skip those checks (or SKIP_AUTH_CHECKS=1).
#
# Exit codes:
#   0   all selected suites passed
#   1   one or more suites failed (failure details printed inline)
#   2   usage / arg error

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ── Defaults ──────────────────────────────────────────────────────────────────

TARGET=""           # empty → tests use their own defaults (localhost:7354)
SKIP_AUTH=""        # empty → tests perform auth checks if password is present
SUITES="bug2-storage,bug4-registration,sweep-catalog,sweep-jobs,sweep-users,sweep-settings"  # comma-separated list of suites to run

# ── Arg parsing ───────────────────────────────────────────────────────────────

while (( $# > 0 )); do
    case "$1" in
        --target)    TARGET="$2";     shift 2 ;;
        --skip-auth) SKIP_AUTH=1;     shift ;;
        --suite)     SUITES="$2";     shift 2 ;;
        -h|--help)
            sed -n '2,/^set -euo pipefail/p' "${BASH_SOURCE[0]}" | grep '^#' | sed 's/^# \?//'
            exit 0
            ;;
        *) echo "Unknown option: $1" >&2; exit 2 ;;
    esac
done

# ── Helpers ───────────────────────────────────────────────────────────────────

_log()  { printf '\033[36m→\033[0m %s\n' "$*"; }
_ok()   { printf '\033[32m✓\033[0m %s\n' "$*"; }
_fail() { printf '\033[31m✗\033[0m %s\n' "$*" >&2; }

# AUTH_REQUIRED_SUITES: suites that need PELICULA_TEST_JELLYFIN_PASSWORD.
# When --skip-auth is set these suites are skipped (not counted as failures).
AUTH_REQUIRED_SUITES="bug4-registration,sweep-catalog,sweep-jobs,sweep-users,sweep-settings"

run_suite() {
    local name="$1" script="$1"
    local script_path="${SCRIPT_DIR}/${script}.sh"

    if [[ ! -f "$script_path" ]]; then
        _fail "Suite '${name}': script not found: ${script_path}"
        return 1
    fi

    # Skip auth-required suites when --skip-auth is in effect.
    if [[ -n "$SKIP_AUTH" ]] && [[ ",${AUTH_REQUIRED_SUITES}," == *",${name},"* ]]; then
        printf '\033[33m-\033[0m Suite '"'"'%s'"'"' SKIPPED (requires auth)\n' "$name"
        return 0
    fi

    local extra_args=()
    [[ -n "$TARGET" ]]      && extra_args+=(--target "$TARGET")
    [[ -n "$SKIP_AUTH" ]]   && extra_args+=(--skip-auth)

    _log "Running suite: ${name} (${script_path})"

    if SKIP_AUTH_CHECKS="${SKIP_AUTH:-}" bash "$script_path" "${extra_args[@]}"; then
        _ok "Suite '${name}' PASSED"
        return 0
    else
        local exit_code=$?
        _fail "Suite '${name}' FAILED (exit ${exit_code})"
        return 1
    fi
}

# ── Main ──────────────────────────────────────────────────────────────────────

_log "=== Pelicula verify ==="
[[ -n "$TARGET" ]]    && _log "Target: ${TARGET}"
[[ -n "$SKIP_AUTH" ]] && _log "Auth checks: skipped"

IFS=',' read -ra selected_suites <<< "$SUITES"

failed=0
passed=0

for suite in "${selected_suites[@]}"; do
    suite="${suite// /}"  # trim whitespace
    [[ -z "$suite" ]] && continue
    if run_suite "$suite"; then
        (( passed++ )) || true
    else
        (( failed++ )) || true
    fi
done

echo ""
_log "Results: ${passed} passed, ${failed} failed"

if (( failed > 0 )); then
    _fail "verify: ${failed} suite(s) FAILED"
    exit 1
fi

_ok "All ${passed} suite(s) passed."
