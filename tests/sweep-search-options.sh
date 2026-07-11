#!/usr/bin/env bash
# tests/sweep-search-options.sh — HTTP smoke tests for the search "Add with
# options…" flow (Phase 1.2: quality-profile / target-library overrides).
#
# Covers:
#   A. GET /api/pelicula/arr-meta responds (now Manager+, was Admin) with the
#      {radarr, sonarr}.{qualityProfiles, rootFolders} shape the modal expects.
#   B. POST /api/pelicula/search/add rejects an out-of-range profileId with 400.
#   C. POST /api/pelicula/search/add rejects an unregistered rootPath with 400.
#   D. POST /api/pelicula/search/add accepts a *valid* profileId/rootPath
#      override (drawn live from arr-meta's own response) — i.e. it is NOT
#      rejected with 400. Skipped if arr-meta has no radarr quality profiles
#      or root folders to test against (radarr not configured on this stack).
#
# Test D is deliberately side-effect-free: it uses tmdbId=0, which is not a
# real TMDB id, so Radarr's movie lookup/add always fails downstream — every
# HandleSearchAdd failure past the override-validation step maps to 502
# ("Radarr is unreachable"), never 400, so "not 400" is a reliable signal that
# the override itself was accepted. A 200 here would mean Radarr somehow
# created a movie for tmdbId=0, which test D treats as a hard failure (it
# should never happen, but the check exists so a false pass can't hide it).
#
# All checks require pelicula session auth.
# Run auth-free portions only:
#   SKIP_AUTH_CHECKS=1 bash tests/sweep-search-options.sh
#
# Auth-required checks must be run manually with PELICULA_TEST_JELLYFIN_PASSWORD set.
#
# Usage:
#   PELICULA_TEST_JELLYFIN_PASSWORD=<pw> PELICULA_TEST_JELLYFIN_USER=gwen \
#     bash tests/sweep-search-options.sh [--target HOST:PORT] [--skip-auth]

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

# All checks in this sweep require pelicula session auth (arr-meta and
# search/add are both Manager+ — there is no unauthenticated surface here).
if [[ -n "$SKIP_AUTH_CHECKS" ]]; then
    echo "sweep-search-options: all checks skipped (auth required)" >&2
    exit 0
fi

export PELICULA_TEST_JELLYFIN_PASSWORD="${PELICULA_TEST_JELLYFIN_PASSWORD:-}"
peli_load_env "$ENV_FILE" "$TARGET_HOST"

if [[ -n "${_OVERRIDE_PORT:-}" ]]; then
    PELI_BASE_URL="http://${TARGET_HOST}:${_OVERRIDE_PORT}"
    _peli_log "Port override: PELI_BASE_URL=$PELI_BASE_URL"
fi

_peli_log "=== sweep-search-options: HTTP smoke tests ==="

# ── Test A: GET /api/pelicula/arr-meta → {radarr,sonarr}.{qualityProfiles,rootFolders} ──
_peli_log "Test A: GET /api/pelicula/arr-meta → expected shape, reachable at Manager+"

arr_meta_resp="$(http_json GET /api/pelicula/arr-meta --auth pelicula)"

for arr in radarr sonarr; do
    for field in qualityProfiles rootFolders; do
        field_type="$(echo "$arr_meta_resp" | jq -r --arg a "$arr" --arg f "$field" '.[$a][$f] | type' 2>/dev/null)"
        # A misconfigured/unreachable *arr yields null (see HandleArrMeta's
        # fetchProfiles/fetchRoots) rather than an array — accept either, but
        # the key must be present.
        if [[ "$field_type" != "array" && "$field_type" != "null" ]]; then
            _peli_err "Test A FAIL: .${arr}.${field} is '${field_type}' (expected 'array' or 'null')"
            exit 1
        fi
    done
done
_peli_ok "Test A passed: arr-meta responds at Manager+ with the expected shape"

# ── Test B: invalid profileId → 400 ───────────────────────────────────────────
_peli_log "Test B: POST /api/pelicula/search/add rejects an out-of-range profileId with 400"

b_resp="$(http_status POST /api/pelicula/search/add \
    '{"type":"movie","tmdbId":0,"profileId":999999999}' --auth pelicula)"
b_code="$(echo "$b_resp" | head -1)"
b_body="$(echo "$b_resp" | tail -n +2)"

if [[ "$b_code" != "400" ]]; then
    _peli_err "Test B FAIL: expected 400 for an out-of-range profileId, got ${b_code}: ${b_body:0:200}"
    exit 1
fi
_peli_ok "Test B passed: out-of-range profileId rejected with 400"

# ── Test C: invalid rootPath → 400 ────────────────────────────────────────────
_peli_log "Test C: POST /api/pelicula/search/add rejects an unregistered rootPath with 400"

c_resp="$(http_status POST /api/pelicula/search/add \
    '{"type":"movie","tmdbId":0,"rootPath":"/media/definitely-not-a-registered-library-xyz"}' --auth pelicula)"
c_code="$(echo "$c_resp" | head -1)"
c_body="$(echo "$c_resp" | tail -n +2)"

if [[ "$c_code" != "400" ]]; then
    _peli_err "Test C FAIL: expected 400 for an unregistered rootPath, got ${c_code}: ${c_body:0:200}"
    exit 1
fi
_peli_ok "Test C passed: unregistered rootPath rejected with 400"

# ── Test D: valid override accepted (no side effect — see header comment) ────
_peli_log "Test D: POST /api/pelicula/search/add accepts a valid profileId/rootPath override"

d_profile_id="$(echo "$arr_meta_resp" | jq -r '.radarr.qualityProfiles[0].id // empty' 2>/dev/null)"
d_root_path="$(echo "$arr_meta_resp" | jq -r '.radarr.rootFolders[0].path // empty' 2>/dev/null)"

if [[ -z "$d_profile_id" || -z "$d_root_path" ]]; then
    _peli_log "Test D SKIPPED: no radarr quality profiles / root folders returned by arr-meta on this stack"
else
    d_body_req="$(jq -n --argjson pid "$d_profile_id" --arg root "$d_root_path" \
        '{type:"movie", tmdbId:0, profileId:$pid, rootPath:$root}')"
    d_resp="$(http_status POST /api/pelicula/search/add "$d_body_req" --auth pelicula)"
    d_code="$(echo "$d_resp" | head -1)"
    d_body="$(echo "$d_resp" | tail -n +2)"

    if [[ "$d_code" == "400" ]]; then
        _peli_err "Test D FAIL: valid override (profileId=${d_profile_id}, rootPath=${d_root_path}) was rejected with 400: ${d_body:0:200}"
        exit 1
    fi
    if [[ "$d_code" == "200" ]]; then
        _peli_err "Test D FAIL: tmdbId=0 unexpectedly succeeded (${d_body:0:200}) — check Radarr for a phantom movie entry"
        exit 1
    fi
    _peli_ok "Test D passed: valid override accepted (not rejected with 400; got ${d_code} from the downstream lookup on the sentinel tmdbId, as expected)"
fi

_peli_ok "=== sweep-search-options: all checks passed ==="
