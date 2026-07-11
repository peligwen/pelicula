#!/usr/bin/env bash
# tests/sweep-remove-action.sh — HTTP smoke tests for the whole-title
# "Remove from library…" action (flow-improvements Phase 4.1/4.2).
#
# Covers:
#   A. GET /api/pelicula/actions/registry (Viewer+) includes a "remove" entry
#      whose applies_to contains both "movie" and "series".
#   B. POST /api/pelicula/actions (the dispatch endpoint) is Admin-gated:
#      an unauthenticated request is rejected with 401.
#
# This suite deliberately does NOT perform an actual removal — dispatching
# "remove" for real deletes files from disk and removes the title from
# Sonarr/Radarr, which is destructive and not something a regression sweep
# should ever risk on a real library. Test B's body targets an
# implausible arr_id (999999999) as belt-and-suspenders even though the
# request never reaches the handler: it's rejected by auth.GuardAdmin
# before HandleCreate runs.
#
# Note on role coverage: this harness (tests/lib.sh) only provisions a
# single authenticated session, mapped from the Jellyfin admin account —
# there is no fixture here (or in any sibling suite) for a second,
# lower-privileged live session, so a "non-admin session rejected with 403"
# case isn't exercised at this layer. That half of the Admin gate is
# covered at the Go unit level by middleware/internal/peligrosa/auth_test.go's
# table-driven GuardAdmin tests (Viewer/Manager both rejected) and by
# router_test.go's TestRouterAuthGates_AdminPaths pattern for other
# GuardAdmin-wrapped routes.
#
# All checks require pelicula session auth (test A reads the registry as an
# authenticated Viewer+; test B's whole point is to send zero credentials).
# Run auth-free portions only:
#   SKIP_AUTH_CHECKS=1 bash tests/sweep-remove-action.sh
#
# Auth-required checks must be run manually with PELICULA_TEST_JELLYFIN_PASSWORD set.
#
# Usage:
#   PELICULA_TEST_JELLYFIN_PASSWORD=<pw> PELICULA_TEST_JELLYFIN_USER=gwen \
#     bash tests/sweep-remove-action.sh [--target HOST:PORT] [--skip-auth]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# PELICULA_ENV_FILE overrides the default when set (e.g. by tests/e2e.sh,
# which points every suite at its isolated .env — see tests/lib.sh's
# peli_load_env doc comment).
ENV_FILE="${PELICULA_ENV_FILE:-${SCRIPT_DIR}/../.env}"

# ── Arg parsing ───────────────────────────────────────────────────────────────

TARGET_HOST="localhost"
SKIP_AUTH_CHECKS="${SKIP_AUTH_CHECKS:-}"

while (( $# > 0 )); do
    case "$1" in
        --target)
            TARGET_HOST="${2%%:*}"
            if [[ "${2}" == *:* ]]; then
                _OVERRIDE_PORT="${2##*:}"
            fi
            shift 2
            ;;
        --skip-auth)
            SKIP_AUTH_CHECKS=1
            shift
            ;;
        *) shift ;;
    esac
done

# ── Source lib ────────────────────────────────────────────────────────────────

# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

# ── Setup ─────────────────────────────────────────────────────────────────────

# Test A needs an authenticated session; there is no unauthenticated surface
# worth checking on its own here, so — like sweep-search-options.sh — the
# whole suite is skipped under --skip-auth rather than partially run.
if [[ -n "$SKIP_AUTH_CHECKS" ]]; then
    echo "sweep-remove-action: all checks skipped (auth required)" >&2
    exit 0
fi

export PELICULA_TEST_JELLYFIN_PASSWORD="${PELICULA_TEST_JELLYFIN_PASSWORD:-}"
peli_load_env "$ENV_FILE" "$TARGET_HOST"

if [[ -n "${_OVERRIDE_PORT:-}" ]]; then
    PELI_BASE_URL="http://${TARGET_HOST}:${_OVERRIDE_PORT}"
    _peli_log "Port override: PELI_BASE_URL=$PELI_BASE_URL"
fi

_peli_log "=== sweep-remove-action: HTTP smoke tests ==="

# ── Test A: registry includes "remove" (movie + series) ──────────────────────
_peli_log "Test A: GET /api/pelicula/actions/registry includes remove (movie+series)"

registry_resp="$(http_json GET /api/pelicula/actions/registry --auth pelicula)"

registry_type="$(echo "$registry_resp" | jq -r 'type' 2>/dev/null)"
if [[ "$registry_type" != "array" ]]; then
    _peli_err "Test A FAIL: /api/pelicula/actions/registry is '${registry_type}' (expected 'array')"
    exit 1
fi

remove_applies_to="$(echo "$registry_resp" | jq -r '.[] | select(.name == "remove") | .applies_to | @json' 2>/dev/null)"
if [[ -z "$remove_applies_to" ]]; then
    _peli_err "Test A FAIL: no entry with name==\"remove\" found in the action registry"
    _peli_err "  Registry names: $(echo "$registry_resp" | jq -c '[.[].name]' 2>/dev/null)"
    exit 1
fi

has_movie="$(echo "$registry_resp" | jq -r '.[] | select(.name == "remove") | .applies_to | index("movie") != null' 2>/dev/null)"
has_series="$(echo "$registry_resp" | jq -r '.[] | select(.name == "remove") | .applies_to | index("series") != null' 2>/dev/null)"

if [[ "$has_movie" != "true" || "$has_series" != "true" ]]; then
    _peli_err "Test A FAIL: remove.applies_to is ${remove_applies_to} (expected to contain both \"movie\" and \"series\")"
    exit 1
fi

_peli_ok "Test A passed: registry exposes remove with applies_to=${remove_applies_to}"

# ── Test B: POST /api/pelicula/actions is Admin-gated (unauthenticated → 401) ─
_peli_log "Test B: POST /api/pelicula/actions (no session) is rejected with 401"

# No --auth flag: sent with zero credentials. arr_id is a deliberately
# implausible sentinel — belt-and-suspenders, since the request is expected
# to be rejected by auth.GuardAdmin before HandleCreate ever inspects the body.
b_body='{"action":"remove","target":{"arr_type":"radarr","arr_id":999999999},"params":{}}'
b_resp="$(http_status POST /api/pelicula/actions "$b_body")"
b_code="$(echo "$b_resp" | head -1)"
b_respbody="$(echo "$b_resp" | tail -n +2)"

if [[ "$b_code" != "401" ]]; then
    _peli_err "Test B FAIL: expected 401 for an unauthenticated dispatch, got ${b_code}: ${b_respbody:0:200}"
    exit 1
fi

_peli_ok "Test B passed: unauthenticated POST /api/pelicula/actions rejected with 401"

_peli_ok "=== sweep-remove-action: all checks passed ==="
