#!/usr/bin/env bash
# tests/bug4-registration.sh — Bug 4: open registration toggle disagreement
#
# Symptom: toggling open_registration ON in Settings still returns
#   open_registration=false from GET /api/pelicula/register/check (the public
#   endpoint register.js polls). The settings write updates .env but the
#   middleware's in-memory peligrosa.OpenRegistration global is never updated,
#   so HandleOpenRegCheck returns the stale boot-time value.
#
# Test:
#   A. POST settings open_registration=true, GET settings round-trip → true
#   B. GET /api/pelicula/register/check (public, no auth) → open_registration=true
#
# Step B fails before the fix; passes after.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib.sh"

peli_load_env "${SCRIPT_DIR}/../.env"

trap tear_down_fixtures EXIT

# ── Step A: settings round-trip ───────────────────────────────────────────────
_peli_log "Step A: POST open_registration=true, assert round-trip"

seed_setting open_registration true
assert_setting_roundtrip open_registration true

_peli_ok "Step A passed: settings round-trip open_registration=true"

# ── Step B: public /register/check endpoint reflects open state ───────────────
_peli_log "Step B: GET /api/pelicula/register/check (no auth) → open_registration=true"

check_resp="$(curl -sf "${PELI_BASE_URL}/api/pelicula/register/check" 2>/dev/null)" || {
    _peli_err "GET /api/pelicula/register/check failed"
    exit 1
}

_peli_log "register/check response: ${check_resp}"

actual_open="$(echo "${check_resp}" | jq -r '.open_registration // empty' 2>/dev/null)"

if [[ "${actual_open}" != "true" ]]; then
    _peli_err "Bug 4 confirmed: open_registration in register/check is '${actual_open}' (expected 'true')"
    _peli_err "  Settings wrote .env correctly but the in-memory peligrosa.OpenRegistration"
    _peli_err "  global was never updated — it still holds the boot-time value."
    exit 1
fi

_peli_ok "Step B passed: register/check reflects open_registration=true"

_peli_ok "All bug4 checks passed."
