#!/usr/bin/env bash
# tests/sweep-catalog.sh — HTTP smoke tests for the Catalog tab.
#
# Covers:
#   A. GET /api/pelicula/catalog?type=movie    → .movies is an array (may be empty)
#   B. GET /api/pelicula/catalog/items          → flat array (may be empty)
#   C. GET /api/pelicula/catalog/qualityprofiles → {radarr, sonarr} keys non-null
#
# All endpoints require pelicula session auth.
# Run auth-free portions only:
#   SKIP_AUTH_CHECKS=1 bash tests/sweep-catalog.sh
#
# Auth-required checks must be run manually with PELICULA_TEST_JELLYFIN_PASSWORD set.
#
# Usage:
#   PELICULA_TEST_JELLYFIN_PASSWORD=<pw> PELICULA_TEST_JELLYFIN_USER=gwen \
#     bash tests/sweep-catalog.sh [--target HOST:PORT] [--skip-auth]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${SCRIPT_DIR}/../.env"

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

# All checks in this sweep require pelicula session auth.
if [[ -n "$SKIP_AUTH_CHECKS" ]]; then
    echo "sweep-catalog: all checks skipped (auth required)" >&2
    exit 0
fi

export PELICULA_TEST_JELLYFIN_PASSWORD="${PELICULA_TEST_JELLYFIN_PASSWORD:-}"
peli_load_env "$ENV_FILE" "$TARGET_HOST"

if [[ -n "${_OVERRIDE_PORT:-}" ]]; then
    PELI_BASE_URL="http://${TARGET_HOST}:${_OVERRIDE_PORT}"
    _peli_log "Port override: PELI_BASE_URL=$PELI_BASE_URL"
fi

trap 'tear_down_fixtures' EXIT

_peli_log "=== sweep-catalog: HTTP smoke tests ==="

# ── Test A: GET /api/pelicula/catalog?type=movie → .movies is an array ────────
_peli_log "Test A: GET /api/pelicula/catalog?type=movie → .movies is an array"

catalog_resp="$(http_json GET /api/pelicula/catalog?type=movie --auth pelicula)"

# .movies must be an array (length may be 0 on an empty stack — assert type, not length)
movies_type="$(echo "$catalog_resp" | jq -r '.movies | type' 2>/dev/null)"
if [[ "$movies_type" != "array" ]]; then
    _peli_err "Test A FAIL: .movies is '${movies_type}' (expected 'array')"
    exit 1
fi
_peli_ok "Test A passed: .movies is an array (length=$(echo "$catalog_resp" | jq '.movies | length' 2>/dev/null))"

# ── Test B: GET /api/pelicula/catalog/items → flat array ─────────────────────
_peli_log "Test B: GET /api/pelicula/catalog/items → flat array"

items_resp="$(http_json GET /api/pelicula/catalog/items --auth pelicula)"

# HandleCatalogItems returns a flat JSON array (not a paginated object)
items_type="$(echo "$items_resp" | jq -r 'type' 2>/dev/null)"
if [[ "$items_type" != "array" ]]; then
    _peli_err "Test B FAIL: /catalog/items response is '${items_type}' (expected 'array')"
    exit 1
fi
_peli_ok "Test B passed: /catalog/items returns an array (length=$(echo "$items_resp" | jq 'length' 2>/dev/null))"

# ── Test C: GET /api/pelicula/catalog/qualityprofiles → radarr and sonarr keys ─
_peli_log "Test C: GET /api/pelicula/catalog/qualityprofiles → {radarr, sonarr} non-null"

qp_resp="$(http_json GET /api/pelicula/catalog/qualityprofiles --auth pelicula)"

# Response must be an object with radarr and sonarr keys (each a sub-object, possibly empty)
radarr_type="$(echo "$qp_resp" | jq -r '.radarr | type' 2>/dev/null)"
sonarr_type="$(echo "$qp_resp" | jq -r '.sonarr | type' 2>/dev/null)"

if [[ "$radarr_type" != "object" ]]; then
    _peli_err "Test C FAIL: .radarr is '${radarr_type}' (expected 'object')"
    exit 1
fi
if [[ "$sonarr_type" != "object" ]]; then
    _peli_err "Test C FAIL: .sonarr is '${sonarr_type}' (expected 'object')"
    exit 1
fi
_peli_ok "Test C passed: /catalog/qualityprofiles has radarr and sonarr objects"

_peli_ok "=== sweep-catalog: all checks passed ==="
