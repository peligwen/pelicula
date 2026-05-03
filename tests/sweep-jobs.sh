#!/usr/bin/env bash
# tests/sweep-jobs.sh — HTTP smoke tests for the Jobs tab.
#
# Covers:
#   A. GET /api/pelicula/jobs        → {groups: {...}, total: N} — all group keys present
#   B. GET /api/pelicula/processing  → non-null response (proxies procula)
#   C. GET /api/procula/jobs         → flat array (via nginx auth gate, --auth pelicula)
#
# Note: /api/procula/ goes through nginx's auth_gate — all three checks require
# pelicula session auth even though procula's own route has no API key guard.
#
# All checks require pelicula session auth.
# Run auth-free portions only:
#   SKIP_AUTH_CHECKS=1 bash tests/sweep-jobs.sh
#
# Auth-required checks must be run manually with PELICULA_TEST_JELLYFIN_PASSWORD set.
#
# Usage:
#   PELICULA_TEST_JELLYFIN_PASSWORD=<pw> PELICULA_TEST_JELLYFIN_USER=gwen \
#     bash tests/sweep-jobs.sh [--target HOST:PORT] [--skip-auth]

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

# All checks require pelicula session auth (nginx auth_gate covers /api/procula/).
if [[ -n "$SKIP_AUTH_CHECKS" ]]; then
    echo "sweep-jobs: all checks skipped (auth required)" >&2
    exit 0
fi

export PELICULA_TEST_JELLYFIN_PASSWORD="${PELICULA_TEST_JELLYFIN_PASSWORD:-}"
peli_load_env "$ENV_FILE" "$TARGET_HOST"

if [[ -n "${_OVERRIDE_PORT:-}" ]]; then
    PELI_BASE_URL="http://${TARGET_HOST}:${_OVERRIDE_PORT}"
    _peli_log "Port override: PELI_BASE_URL=$PELI_BASE_URL"
fi

trap 'tear_down_fixtures' EXIT

_peli_log "=== sweep-jobs: HTTP smoke tests ==="

# ── Test A: GET /api/pelicula/jobs → {groups: {...}, total: N} ────────────────
_peli_log "Test A: GET /api/pelicula/jobs → {groups, total}"

jobs_resp="$(http_json GET /api/pelicula/jobs --auth pelicula)"

# Must have a 'groups' object and a 'total' field
groups_type="$(echo "$jobs_resp" | jq -r '.groups | type' 2>/dev/null)"
if [[ "$groups_type" != "object" ]]; then
    _peli_err "Test A FAIL: .groups is '${groups_type}' (expected 'object')"
    exit 1
fi

# All five canonical state buckets must be present
for state in queued processing completed failed cancelled; do
    bucket_type="$(echo "$jobs_resp" | jq -r --arg s "$state" '.groups[$s] | type' 2>/dev/null)"
    if [[ "$bucket_type" != "array" ]]; then
        _peli_err "Test A FAIL: .groups.${state} is '${bucket_type}' (expected 'array')"
        exit 1
    fi
done

total="$(echo "$jobs_resp" | jq -r '.total' 2>/dev/null)"
if [[ -z "$total" || "$total" == "null" ]]; then
    _peli_err "Test A FAIL: .total is missing or null"
    exit 1
fi
_peli_ok "Test A passed: /api/pelicula/jobs has groups+total (total=${total})"

# ── Test B: GET /api/pelicula/processing → non-null ───────────────────────────
_peli_log "Test B: GET /api/pelicula/processing → non-null"

processing_resp="$(http_json GET /api/pelicula/processing --auth pelicula)"

if [[ -z "$processing_resp" ]]; then
    _peli_err "Test B FAIL: /api/pelicula/processing returned empty body"
    exit 1
fi
_peli_ok "Test B passed: /api/pelicula/processing returned non-empty response"

# ── Test C: GET /api/procula/jobs → flat array (via nginx auth gate) ──────────
_peli_log "Test C: GET /api/procula/jobs (--auth pelicula, via nginx) → flat array"

procula_jobs_resp="$(http_json GET /api/procula/jobs --auth pelicula)"

procula_jobs_type="$(echo "$procula_jobs_resp" | jq -r 'type' 2>/dev/null)"
if [[ "$procula_jobs_type" != "array" ]]; then
    _peli_err "Test C FAIL: /api/procula/jobs is '${procula_jobs_type}' (expected 'array')"
    exit 1
fi
_peli_ok "Test C passed: /api/procula/jobs returns an array (length=$(echo "$procula_jobs_resp" | jq 'length' 2>/dev/null))"

_peli_ok "=== sweep-jobs: all checks passed ==="
