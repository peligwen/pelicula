#!/usr/bin/env bash
# tests/sweep-settings.sh — HTTP smoke tests for the Settings tab.
#
# Covers:
#   A. GET /api/pelicula/settings       → non-empty top-level keys; print count
#   B. Round-trip 4 safe settings keys:
#        sub_langs, notifications_mode, search_mode, transcoding_enabled
#      These are display/toggle settings that have no side effects on the
#      running stack (no container restart, no VPN change, no port remapping).
#      Avoids open_registration (exercised by bug4), port, remote_*, and
#      wireguard_key.
#
# All checks require pelicula admin session auth.
# Run auth-free portions only:
#   SKIP_AUTH_CHECKS=1 bash tests/sweep-settings.sh
#
# Auth-required checks must be run manually with PELICULA_TEST_JELLYFIN_PASSWORD set.
#
# Usage:
#   PELICULA_TEST_JELLYFIN_PASSWORD=<pw> PELICULA_TEST_JELLYFIN_USER=gwen \
#     bash tests/sweep-settings.sh [--target HOST:PORT] [--skip-auth]

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
    echo "sweep-settings: all checks skipped (auth required)" >&2
    exit 0
fi

export PELICULA_TEST_JELLYFIN_PASSWORD="${PELICULA_TEST_JELLYFIN_PASSWORD:-}"
peli_load_env "$ENV_FILE" "$TARGET_HOST"

if [[ -n "${_OVERRIDE_PORT:-}" ]]; then
    PELI_BASE_URL="http://${TARGET_HOST}:${_OVERRIDE_PORT}"
    _peli_log "Port override: PELI_BASE_URL=$PELI_BASE_URL"
fi

# seed_setting stashes original values; tear_down_fixtures restores them all.
trap 'tear_down_fixtures' EXIT

_peli_log "=== sweep-settings: HTTP smoke tests ==="

# ── Test A: GET /api/pelicula/settings → non-empty top-level keys ─────────────
_peli_log "Test A: GET /api/pelicula/settings → non-empty key set"

settings_resp="$(http_json GET /api/pelicula/settings --auth pelicula)"

key_count="$(echo "$settings_resp" | jq 'keys | length' 2>/dev/null)"
if [[ -z "$key_count" || "$key_count" == "0" ]]; then
    _peli_err "Test A FAIL: /api/pelicula/settings returned no keys"
    exit 1
fi

_peli_log "  Settings has ${key_count} keys: $(echo "$settings_resp" | jq -r 'keys | join(", ")' 2>/dev/null)"

# A handful of required keys that must always be present
for required_key in .port .config_dir .library_dir .sub_langs; do
    assert_field_nonempty "$settings_resp" "$required_key" || {
        _peli_err "Test A FAIL: required key $required_key is missing or empty"
        exit 1
    }
done
_peli_ok "Test A passed: /api/pelicula/settings has ${key_count} keys with required fields"

# ── Test B: Round-trip 4 safe settings keys ───────────────────────────────────
_peli_log "Test B: settings round-trip for sub_langs, notifications_mode, search_mode, transcoding_enabled"

# sub_langs: safe to change to a different valid language code temporarily
_peli_log "  B1: sub_langs round-trip"
seed_setting sub_langs "en, fr"
assert_setting_roundtrip sub_langs "en, fr"
_peli_ok "  B1 passed: sub_langs round-trip"

# notifications_mode: "internal" or "apprise" — only affects notification routing
_peli_log "  B2: notifications_mode round-trip"
seed_setting notifications_mode "internal"
assert_setting_roundtrip notifications_mode "internal"
_peli_ok "  B2 passed: notifications_mode round-trip"

# search_mode: "tmdb" or "indexer" — only changes which search backend the UI queries
_peli_log "  B3: search_mode round-trip"
seed_setting search_mode "tmdb"
assert_setting_roundtrip search_mode "tmdb"
_peli_ok "  B3 passed: search_mode round-trip"

# transcoding_enabled: "true"/"false" — controls procula's transcoding pipeline flag
_peli_log "  B4: transcoding_enabled round-trip"
seed_setting transcoding_enabled "false"
assert_setting_roundtrip transcoding_enabled "false"
_peli_ok "  B4 passed: transcoding_enabled round-trip"

_peli_ok "=== sweep-settings: all checks passed ==="
# tear_down_fixtures called by EXIT trap — restores all seeded settings
