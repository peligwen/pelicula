#!/usr/bin/env bash
# tests/sweep-journey.sh — HTTP smoke tests for the per-title journey
# aggregation endpoint (Phase 3.1: journey backend).
#
# Covers:
#   A. GET /api/pelicula/journey with no params → 400 (neither of the two
#      accepted query forms — type+tmdb_id/tvdb_id, arr_type+arr_id — is
#      complete).
#   B. GET /api/pelicula/journey?arr_type=radarr&arr_id=999999999 → 404
#      (a syntactically valid query that no *arr item, request row, or
#      catalog row can match).
#   C. Pull a movie from GET /api/pelicula/catalog and, if one exists,
#      fetch its journey by arr_id → 200 with .stages a 6-element array
#      whose .stage names equal the canonical rail in order (requested,
#      approved, searching, downloading, processing, available) and a
#      non-empty .current_stage. Skipped gracefully when the catalog has
#      no movies on this stack — same style as sweep-search-options.sh's
#      test D skip.
#
# All checks are read-only — the journey endpoint aggregates state and
# never writes.
#
# All checks require pelicula session auth.
# Run auth-free portions only:
#   SKIP_AUTH_CHECKS=1 bash tests/sweep-journey.sh
#
# Auth-required checks must be run manually with PELICULA_TEST_JELLYFIN_PASSWORD set.
#
# Usage:
#   PELICULA_TEST_JELLYFIN_PASSWORD=<pw> PELICULA_TEST_JELLYFIN_USER=gwen \
#     bash tests/sweep-journey.sh [--target HOST:PORT] [--skip-auth]

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

# All checks in this sweep require pelicula session auth (journey and catalog
# are both Viewer+ — there is no unauthenticated surface here).
if [[ -n "$SKIP_AUTH_CHECKS" ]]; then
    echo "sweep-journey: all checks skipped (auth required)" >&2
    exit 0
fi

export PELICULA_TEST_JELLYFIN_PASSWORD="${PELICULA_TEST_JELLYFIN_PASSWORD:-}"
peli_load_env "$ENV_FILE" "$TARGET_HOST"

if [[ -n "${_OVERRIDE_PORT:-}" ]]; then
    PELI_BASE_URL="http://${TARGET_HOST}:${_OVERRIDE_PORT}"
    _peli_log "Port override: PELI_BASE_URL=$PELI_BASE_URL"
fi

_peli_log "=== sweep-journey: HTTP smoke tests ==="

# ── Test A: no params → 400 ───────────────────────────────────────────────────
_peli_log "Test A: GET /api/pelicula/journey with no params → 400"

a_resp="$(http_status GET /api/pelicula/journey '' --auth pelicula)"
a_code="$(echo "$a_resp" | head -1)"
a_body="$(echo "$a_resp" | tail -n +2)"

if [[ "$a_code" != "400" ]]; then
    _peli_err "Test A FAIL: expected 400 with no params, got ${a_code}: ${a_body:0:200}"
    exit 1
fi
_peli_ok "Test A passed: no params rejected with 400"

# ── Test B: unknown arr_id → 404 ──────────────────────────────────────────────
_peli_log "Test B: GET /api/pelicula/journey?arr_type=radarr&arr_id=999999999 → 404"

b_resp="$(http_status GET '/api/pelicula/journey?arr_type=radarr&arr_id=999999999' '' --auth pelicula)"
b_code="$(echo "$b_resp" | head -1)"
b_body="$(echo "$b_resp" | tail -n +2)"

if [[ "$b_code" != "404" ]]; then
    _peli_err "Test B FAIL: expected 404 for an unknown arr_id, got ${b_code}: ${b_body:0:200}"
    exit 1
fi
_peli_ok "Test B passed: unknown arr_id rejected with 404"

# ── Test C: catalog-derived movie journey shape ───────────────────────────────
_peli_log "Test C: journey for a real catalog movie → 200 with the canonical 6-stage rail"

catalog_resp="$(http_json GET '/api/pelicula/catalog?type=movie' --auth pelicula)"
c_arr_id="$(echo "$catalog_resp" | jq -r '.movies[0].id // empty' 2>/dev/null)"

if [[ -z "$c_arr_id" ]]; then
    _peli_log "Test C SKIPPED: no movies in the catalog on this stack"
else
    c_resp="$(http_json GET "/api/pelicula/journey?arr_type=radarr&arr_id=${c_arr_id}" --auth pelicula)" || {
        _peli_err "Test C FAIL: journey fetch for arr_id=${c_arr_id} did not return 200"
        exit 1
    }

    c_stage_count="$(echo "$c_resp" | jq -r '.stages | length' 2>/dev/null)"
    if [[ "$c_stage_count" != "6" ]]; then
        _peli_err "Test C FAIL: .stages has ${c_stage_count:-<missing>} entries, want 6: $(echo "$c_resp" | jq -c '.stages' 2>/dev/null | head -c 300)"
        exit 1
    fi

    c_stage_names="$(echo "$c_resp" | jq -c '[.stages[].stage]' 2>/dev/null)"
    c_want='["requested","approved","searching","downloading","processing","available"]'
    if [[ "$c_stage_names" != "$c_want" ]]; then
        _peli_err "Test C FAIL: stage names ${c_stage_names:-<missing>}, want ${c_want}"
        exit 1
    fi

    assert_field_nonempty "$c_resp" '.current_stage' || {
        _peli_err "Test C FAIL: .current_stage is empty"
        exit 1
    }

    _peli_ok "Test C passed: arr_id=${c_arr_id} journey has the canonical rail (current_stage=$(echo "$c_resp" | jq -r '.current_stage'))"
fi

_peli_ok "=== sweep-journey: all checks passed ==="
