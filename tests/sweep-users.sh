#!/usr/bin/env bash
# tests/sweep-users.sh — HTTP smoke tests for the Users tab.
#
# Covers:
#   A. GET /api/pelicula/users       → array, .[0].Name non-empty (Jellyfin user shape)
#   B. GET /api/pelicula/auth/check  → {auth, valid} fields present (public)
#   C. GET /api/pelicula/invites     → array shape
#
# Test B is auth-free; A and C require pelicula admin session auth.
# Run auth-free portions only:
#   SKIP_AUTH_CHECKS=1 bash tests/sweep-users.sh
#
# Auth-required checks must be run manually with PELICULA_TEST_JELLYFIN_PASSWORD set.
#
# Usage:
#   PELICULA_TEST_JELLYFIN_PASSWORD=<pw> PELICULA_TEST_JELLYFIN_USER=gwen \
#     bash tests/sweep-users.sh [--target HOST:PORT] [--skip-auth]

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

# Provide a placeholder password so peli_load_env won't abort on test B path.
if [[ -n "$SKIP_AUTH_CHECKS" ]]; then
    export PELICULA_TEST_JELLYFIN_PASSWORD="${PELICULA_TEST_JELLYFIN_PASSWORD:-_skip_}"
fi

peli_load_env "$ENV_FILE" "$TARGET_HOST"

if [[ -n "${_OVERRIDE_PORT:-}" ]]; then
    PELI_BASE_URL="http://${TARGET_HOST}:${_OVERRIDE_PORT}"
    _peli_log "Port override: PELI_BASE_URL=$PELI_BASE_URL"
fi

trap 'tear_down_fixtures' EXIT

_peli_log "=== sweep-users: HTTP smoke tests ==="

# ── Test B (auth-free): GET /api/pelicula/auth/check → {auth, valid} ─────────
_peli_log "Test B: GET /api/pelicula/auth/check (public) → {auth, valid} fields"

check_resp="$(curl -sf "${PELI_BASE_URL}/api/pelicula/auth/check" 2>/dev/null)" || {
    _peli_err "Test B FAIL: GET /api/pelicula/auth/check failed"
    exit 1
}

if [[ -z "$check_resp" ]]; then
    _peli_err "Test B FAIL: /api/pelicula/auth/check returned empty body"
    exit 1
fi

# Must have .auth field (boolean — not null, not missing)
auth_val="$(echo "$check_resp" | jq -r '.auth' 2>/dev/null)"
if [[ -z "$auth_val" || "$auth_val" == "null" ]]; then
    _peli_err "Test B FAIL: .auth field is missing or null in auth/check response"
    _peli_err "  Got: ${check_resp:0:200}"
    exit 1
fi

# Must have .valid field
valid_val="$(echo "$check_resp" | jq -r '.valid' 2>/dev/null)"
if [[ -z "$valid_val" || "$valid_val" == "null" ]]; then
    _peli_err "Test B FAIL: .valid field is missing or null in auth/check response"
    exit 1
fi

_peli_ok "Test B passed: /api/pelicula/auth/check returns auth=${auth_val} valid=${valid_val}"

# ── Auth checks ───────────────────────────────────────────────────────────────

if [[ -n "$SKIP_AUTH_CHECKS" ]]; then
    _peli_log "Tests A+C: SKIPPED (SKIP_AUTH_CHECKS set)"
    _peli_ok "=== sweep-users: auth-free checks passed; auth-required checks skipped ==="
    exit 0
fi

# ── Test A: GET /api/pelicula/users → array, .[0].Name non-empty ──────────────
_peli_log "Test A: GET /api/pelicula/users → array with at least one user"

users_resp="$(http_json GET /api/pelicula/users --auth pelicula)"

users_type="$(echo "$users_resp" | jq -r 'type' 2>/dev/null)"
if [[ "$users_type" != "array" ]]; then
    _peli_err "Test A FAIL: /api/pelicula/users is '${users_type}' (expected 'array')"
    exit 1
fi

user_count="$(echo "$users_resp" | jq 'length' 2>/dev/null)"
if [[ -z "$user_count" || "$user_count" == "0" ]]; then
    _peli_err "Test A FAIL: /api/pelicula/users returned empty array (expected at least 1 user)"
    exit 1
fi

# Jellyfin user objects have a Name field
assert_field_nonempty "$users_resp" '.[0].Name' || {
    _peli_err "Test A FAIL: .[0].Name is empty or null"
    exit 1
}
_peli_ok "Test A passed: /api/pelicula/users returned ${user_count} user(s)"

# ── Test C: GET /api/pelicula/invites → array shape ───────────────────────────
_peli_log "Test C: GET /api/pelicula/invites → array shape"

invites_resp="$(http_json GET /api/pelicula/invites --auth pelicula)"

invites_type="$(echo "$invites_resp" | jq -r 'type' 2>/dev/null)"
if [[ "$invites_type" != "array" ]]; then
    _peli_err "Test C FAIL: /api/pelicula/invites is '${invites_type}' (expected 'array')"
    exit 1
fi
_peli_ok "Test C passed: /api/pelicula/invites returns an array (length=$(echo "$invites_resp" | jq 'length' 2>/dev/null))"

_peli_ok "=== sweep-users: all checks passed ==="
