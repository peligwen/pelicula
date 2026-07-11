#!/usr/bin/env bash
# tests/sweep-search-seasons.sh — HTTP smoke tests for season-level
# granularity on series add/request (Phase 2.1: seasons backend).
#
# Covers:
#   A. POST /api/pelicula/search/add {"type":"movie","tmdbId":0,"seasons":[1]}
#      → 400 (seasons is only valid for series).
#   B. POST /api/pelicula/search/add {"type":"series","tvdbId":0,"seasons":[]}
#      → 400 (non-nil empty seasons array — there is no "monitor nothing" add).
#   C. POST /api/pelicula/search/add
#      {"type":"series","tvdbId":0,"seasons":[1000]} → 400. The shape check
#      (0-999 range) fires before any Sonarr lookup, so tvdbId=0 — not a real
#      series — never reaches upstream; a 400 here (not 502) proves the shape
#      check runs first.
#   D. POST /api/pelicula/requests
#      {"type":"movie","tmdb_id":1,"title":"x","seasons":[1]} → 400 (mirrors
#      search/add's movie+seasons rule at the request-create endpoint).
#   E. POST /api/pelicula/requests with type=series, a unique sentinel
#      tvdb_id (990000000+RANDOM, to dodge the findActive dedup against any
#      real request), title, and seasons [1,2] → 201 with .seasons == [1,2];
#      then DELETE /api/pelicula/requests/{id} to leave no residue behind
#      (the shared verify.sh session is admin, so the cleanup delete is
#      always authorized).
#
# Tests A-D are side-effect-free (tmdbId=0/tvdbId=0 are not real *arr ids,
# and every one of them is rejected before any Sonarr/Radarr call is made).
# Test E is the only one that writes a row, and it always cleans up after
# itself — see its own comment below for the failure-path caveat.
#
# All checks require pelicula session auth.
# Run auth-free portions only:
#   SKIP_AUTH_CHECKS=1 bash tests/sweep-search-seasons.sh
#
# Auth-required checks must be run manually with PELICULA_TEST_JELLYFIN_PASSWORD set.
#
# Usage:
#   PELICULA_TEST_JELLYFIN_PASSWORD=<pw> PELICULA_TEST_JELLYFIN_USER=gwen \
#     bash tests/sweep-search-seasons.sh [--target HOST:PORT] [--skip-auth]

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

# All checks in this sweep require pelicula session auth (search/add and
# requests are both gated — there is no unauthenticated surface here).
if [[ -n "$SKIP_AUTH_CHECKS" ]]; then
    echo "sweep-search-seasons: all checks skipped (auth required)" >&2
    exit 0
fi

export PELICULA_TEST_JELLYFIN_PASSWORD="${PELICULA_TEST_JELLYFIN_PASSWORD:-}"
peli_load_env "$ENV_FILE" "$TARGET_HOST"

if [[ -n "${_OVERRIDE_PORT:-}" ]]; then
    PELI_BASE_URL="http://${TARGET_HOST}:${_OVERRIDE_PORT}"
    _peli_log "Port override: PELI_BASE_URL=$PELI_BASE_URL"
fi

_peli_log "=== sweep-search-seasons: HTTP smoke tests ==="

# ── Test A: seasons on a movie search/add → 400 ───────────────────────────────
_peli_log "Test A: POST /api/pelicula/search/add rejects seasons on type=movie with 400"

a_resp="$(http_status POST /api/pelicula/search/add \
    '{"type":"movie","tmdbId":0,"seasons":[1]}' --auth pelicula)"
a_code="$(echo "$a_resp" | head -1)"
a_body="$(echo "$a_resp" | tail -n +2)"

if [[ "$a_code" != "400" ]]; then
    _peli_err "Test A FAIL: expected 400 for seasons on a movie, got ${a_code}: ${a_body:0:200}"
    exit 1
fi
_peli_ok "Test A passed: seasons on type=movie rejected with 400"

# ── Test B: non-nil empty seasons array → 400 ─────────────────────────────────
_peli_log "Test B: POST /api/pelicula/search/add rejects a non-nil empty seasons array with 400"

b_resp="$(http_status POST /api/pelicula/search/add \
    '{"type":"series","tvdbId":0,"seasons":[]}' --auth pelicula)"
b_code="$(echo "$b_resp" | head -1)"
b_body="$(echo "$b_resp" | tail -n +2)"

if [[ "$b_code" != "400" ]]; then
    _peli_err "Test B FAIL: expected 400 for an empty seasons array, got ${b_code}: ${b_body:0:200}"
    exit 1
fi
_peli_ok "Test B passed: empty seasons array rejected with 400"

# ── Test C: out-of-range season number → 400 before any Sonarr lookup ────────
_peli_log "Test C: POST /api/pelicula/search/add rejects an out-of-range season number with 400 (shape check precedes the Sonarr lookup)"

c_resp="$(http_status POST /api/pelicula/search/add \
    '{"type":"series","tvdbId":0,"seasons":[1000]}' --auth pelicula)"
c_code="$(echo "$c_resp" | head -1)"
c_body="$(echo "$c_resp" | tail -n +2)"

if [[ "$c_code" != "400" ]]; then
    _peli_err "Test C FAIL: expected 400 for an out-of-range season number, got ${c_code}: ${c_body:0:200}"
    exit 1
fi
_peli_ok "Test C passed: out-of-range season number rejected with 400 (shape check fires before the Sonarr lookup)"

# ── Test D: seasons on a movie request-create → 400 ───────────────────────────
_peli_log "Test D: POST /api/pelicula/requests rejects seasons on type=movie with 400"

d_resp="$(http_status POST /api/pelicula/requests \
    '{"type":"movie","tmdb_id":1,"title":"x","seasons":[1]}' --auth pelicula)"
d_code="$(echo "$d_resp" | head -1)"
d_body="$(echo "$d_resp" | tail -n +2)"

if [[ "$d_code" != "400" ]]; then
    _peli_err "Test D FAIL: expected 400 for seasons on a movie request, got ${d_code}: ${d_body:0:200}"
    exit 1
fi
_peli_ok "Test D passed: seasons on a movie request rejected with 400"

# ── Test E: series request with seasons persists, then cleaned up ────────────
_peli_log "Test E: POST /api/pelicula/requests with seasons on a series persists [1,2] and echoes it back"

# Sentinel tvdb_id far outside any real TVDB range, randomized per run to
# dodge findActive's active-request dedup if a prior run's cleanup failed.
e_tvdb_id=$(( 990000000 + RANDOM ))
e_body="$(jq -n --argjson tvdb "$e_tvdb_id" \
    '{type: "series", tvdb_id: $tvdb, title: "sweep-search-seasons sentinel", seasons: [1, 2]}')"

e_resp="$(http_status POST /api/pelicula/requests "$e_body" --auth pelicula)"
e_code="$(echo "$e_resp" | head -1)"
e_respbody="$(echo "$e_resp" | tail -n +2)"

if [[ "$e_code" != "201" ]]; then
    _peli_err "Test E FAIL: expected 201, got ${e_code}: ${e_respbody:0:200}"
    exit 1
fi

e_seasons="$(echo "$e_respbody" | jq -c '.seasons' 2>/dev/null)"
e_id="$(echo "$e_respbody" | jq -r '.id' 2>/dev/null)"

# Clean up first (before asserting), so a failed assertion still leaves no
# residue — the sentinel request is deleted either way.
if [[ -n "$e_id" && "$e_id" != "null" ]]; then
    http_status DELETE "/api/pelicula/requests/${e_id}" --auth pelicula > /dev/null || \
        _peli_warn "Test E: failed to clean up sentinel request ${e_id} — remove it manually"
else
    _peli_warn "Test E: no request id in response body, nothing to clean up: ${e_respbody:0:200}"
fi

if [[ "$e_seasons" != "[1,2]" ]]; then
    _peli_err "Test E FAIL: .seasons = ${e_seasons:-<missing>}, want [1,2]: ${e_respbody:0:200}"
    exit 1
fi
_peli_ok "Test E passed: series request persists and echoes seasons [1,2]; sentinel request cleaned up"

_peli_ok "=== sweep-search-seasons: all checks passed ==="
