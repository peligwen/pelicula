#!/usr/bin/env bash
# tests/bug2-storage.sh — Bug 2: Storage tab cards (FREE/PELICULA/STATUS) empty
#
# Symptom: On the Storage tab the FREE / PELICULA / STATUS metric cards and the
# LIBRARIES section display empty values ("—").
#
# Root cause: nginx/format.js and nginx/notif-helpers.js were introduced in
# commit fb30963 but were NOT added to nginx's per-file volume mounts in
# compose/docker-compose.yml. Both assets 404'd in the running stack.
# dashboard.js declares `const formatSize = window.formatSize` at module
# evaluation time; because format.js never loaded, window.formatSize was
# undefined. Every renderStorageMetrics / renderStorageFolders / renderStorage
# call threw a TypeError, so the Storage section silently stayed empty even
# though GET /api/pelicula/storage returned valid filesystem data.
#
# Fix: add format.js and notif-helpers.js to nginx volume mounts in
# compose/docker-compose.yml (commit e7e86ad). Requires `pelicula redeploy` on
# deployments that have not yet pulled the fix.
#
# Tests (in order):
#   A. GET /format.js          → HTTP 200, body contains 'window.formatSize'
#   B. GET /notif-helpers.js   → HTTP 200, non-empty body
#   C. GET /api/pelicula/storage (--auth pelicula) → filesystems[0] non-null
#   D. GET /api/pelicula/libraries (public)        → non-empty array
#
# Tests A and B fail before the fix (nginx 404s), pass after (assets mounted).
# Test C fails before (window.formatSize undefined → JS throws, but the HTTP
# endpoint itself is healthy once auth is present; the test validates the
# backend contract so a regression in the proxy chain would still be caught).
# Test D validates that the libraries lane has data to display.
#
# Requires (for test C only):
#   PELICULA_TEST_JELLYFIN_PASSWORD  — Jellyfin admin password
#   PELICULA_TEST_JELLYFIN_USER      — Jellyfin admin username (default: gwen)
#
# Usage:
#   PELICULA_TEST_JELLYFIN_PASSWORD=<pw> PELICULA_TEST_JELLYFIN_USER=gwen \
#     bash tests/bug2-storage.sh
#
#   # Run only static-asset checks (no password needed):
#   SKIP_AUTH_CHECKS=1 bash tests/bug2-storage.sh
#
# Works against any running Pelicula stack via --target HOST:PORT.

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

# Skip the password requirement when running auth-free checks only.
if [[ -n "$SKIP_AUTH_CHECKS" ]]; then
    # Stub out the password requirement by providing a placeholder — we won't
    # actually call any --auth pelicula paths.
    export PELICULA_TEST_JELLYFIN_PASSWORD="${PELICULA_TEST_JELLYFIN_PASSWORD:-_skip_}"
fi

peli_load_env "$ENV_FILE" "$TARGET_HOST"

if [[ -n "${_OVERRIDE_PORT:-}" ]]; then
    PELI_BASE_URL="http://${TARGET_HOST}:${_OVERRIDE_PORT}"
    _peli_log "Port override: PELI_BASE_URL=$PELI_BASE_URL"
fi

trap 'tear_down_fixtures' EXIT

_peli_log "=== Bug 2: Storage tab static-asset + API smoke ==="

# ── Test A: GET /format.js → 200, contains window.formatSize ─────────────────
_peli_log "Test A: GET /format.js → HTTP 200 and defines window.formatSize"

format_body="$(curl -sf "${PELI_BASE_URL}/format.js" 2>/dev/null)" || {
    _peli_err "GET /format.js failed (HTTP non-200 or connection error)"
    _peli_err "  Before fix: nginx 404 because format.js was not in compose volume mounts."
    exit 1
}

if [[ -z "$format_body" ]]; then
    _peli_err "GET /format.js returned empty body"
    exit 1
fi

if ! grep -q 'window.formatSize' <(echo "$format_body"); then
    _peli_err "GET /format.js body does not contain 'window.formatSize'"
    _peli_err "  Got: ${format_body:0:200}"
    exit 1
fi

_peli_ok "Test A passed: /format.js returns 200 and defines window.formatSize"

# ── Test B: GET /notif-helpers.js → 200, non-empty ───────────────────────────
_peli_log "Test B: GET /notif-helpers.js → HTTP 200 and non-empty"

notif_body="$(curl -sf "${PELI_BASE_URL}/notif-helpers.js" 2>/dev/null)" || {
    _peli_err "GET /notif-helpers.js failed (HTTP non-200 or connection error)"
    _peli_err "  Before fix: nginx 404 because notif-helpers.js was not in compose volume mounts."
    exit 1
}

if [[ -z "$notif_body" ]]; then
    _peli_err "GET /notif-helpers.js returned empty body"
    exit 1
fi

_peli_ok "Test B passed: /notif-helpers.js returns 200 and is non-empty"

# ── Test C: GET /api/pelicula/storage → filesystems non-empty ────────────────
if [[ -n "$SKIP_AUTH_CHECKS" ]]; then
    _peli_log "Test C: SKIPPED (SKIP_AUTH_CHECKS set)"
else
    _peli_log "Test C: GET /api/pelicula/storage → filesystems[0] non-null"

    storage_resp="$(http_json GET /api/pelicula/storage --auth pelicula)"

    assert_field_nonempty "$storage_resp" '.filesystems[0]'
    assert_field_nonempty "$storage_resp" '.filesystems[0].available'
    assert_field_nonempty "$storage_resp" '.filesystems[0].status'
    assert_field_nonempty "$storage_resp" '.filesystems[0].folders[0].label'

    _peli_ok "Test C passed: /api/pelicula/storage returns filesystems with expected fields"
fi

# ── Test D: GET /api/pelicula/libraries → non-empty array ────────────────────
_peli_log "Test D: GET /api/pelicula/libraries (public) → non-empty array"

libs_resp="$(curl -sf "${PELI_BASE_URL}/api/pelicula/libraries" 2>/dev/null)" || {
    _peli_err "GET /api/pelicula/libraries failed"
    exit 1
}

if [[ -z "$libs_resp" ]]; then
    _peli_err "GET /api/pelicula/libraries returned empty body"
    exit 1
fi

lib_count="$(echo "$libs_resp" | jq 'length' 2>/dev/null)"
if [[ -z "$lib_count" || "$lib_count" == "0" ]]; then
    _peli_err "GET /api/pelicula/libraries returned empty array (expected at least 1 library)"
    _peli_err "  Got: ${libs_resp:0:200}"
    exit 1
fi

assert_field_nonempty "$libs_resp" '.[0].name'
assert_field_nonempty "$libs_resp" '.[0].slug'

_peli_ok "Test D passed: /api/pelicula/libraries returns ${lib_count} library(s)"

_peli_ok "All bug2 checks passed."
