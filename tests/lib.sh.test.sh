#!/usr/bin/env bash
# tests/lib.sh.test.sh — Self-tests for tests/lib.sh
#
# Runs against the live stack at PELI_BASE_URL (default: localhost:7354).
# Requires PELICULA_TEST_JELLYFIN_PASSWORD to be exported.
#
# Usage:
#   PELICULA_TEST_JELLYFIN_PASSWORD=<pass> bash tests/lib.sh.test.sh
#   PELICULA_TEST_JELLYFIN_PASSWORD=<pass> bash tests/lib.sh.test.sh --target 192.168.1.143:7354
#
# Exit code: 0 if all tests pass, 1 if any fail.
set -euo pipefail

# ── Resolve paths ─────────────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
LIB="$SCRIPT_DIR/lib.sh"
FIXTURE_A="$SCRIPT_DIR/fixtures/media/fixture-a.mp4"
ENV_FILE="$REPO_ROOT/.env"

# ── Arg parsing ───────────────────────────────────────────────────────────────

TARGET_HOST="localhost"
while (( $# > 0 )); do
    case "$1" in
        --target)
            # Accept host:port (strip port and pass host; port comes from .env)
            # OR just a host. We pass the host to peli_load_env and let it build URL.
            TARGET_HOST="${2%%:*}"
            # If port was given explicitly, override via env
            if [[ "${2}" == *:* ]]; then
                _OVERRIDE_PORT="${2##*:}"
            fi
            shift 2
            ;;
        *) shift ;;
    esac
done

# ── Source lib ────────────────────────────────────────────────────────────────

# shellcheck source=lib.sh
source "$LIB"

# ── Reporting helpers ─────────────────────────────────────────────────────────

_TESTS_RUN=0
_TESTS_PASSED=0
_TESTS_FAILED=0
_FAILURES=()

_t_pass() {
    (( _TESTS_PASSED++ )) || true
    echo -e "  ${_PELI_GREEN}PASS${_PELI_NC} $1"
}

_t_fail() {
    (( _TESTS_FAILED++ )) || true
    _FAILURES+=("$1")
    echo -e "  ${_PELI_RED}FAIL${_PELI_NC} $1"
}

# run_test NAME CMD...
#   Run CMD; report pass/fail. Never exits early regardless of set -e.
run_test() {
    local name="$1"
    shift
    (( _TESTS_RUN++ )) || true
    if "$@" 2>/dev/null; then
        _t_pass "$name"
    else
        _t_fail "$name"
    fi
}

# run_test_expect_fail NAME CMD...
#   Assert CMD fails (returns non-zero). Reports pass when it does fail.
run_test_expect_fail() {
    local name="$1"
    shift
    (( _TESTS_RUN++ )) || true
    if "$@" 2>/dev/null; then
        _t_fail "$name  (expected failure but got success)"
    else
        _t_pass "$name"
    fi
}

# ── Setup ─────────────────────────────────────────────────────────────────────

echo ""
echo -e "${_PELI_CYAN}=== lib.sh self-test ===${_PELI_NC}"
echo ""

trap 'tear_down_fixtures' EXIT

peli_load_env "$ENV_FILE" "$TARGET_HOST"

# If caller overrode the port via --target host:port, rewrite PELI_BASE_URL
if [[ -n "${_OVERRIDE_PORT:-}" ]]; then
    PELI_BASE_URL="http://${TARGET_HOST}:${_OVERRIDE_PORT}"
    _peli_log "Port override: PELI_BASE_URL=$PELI_BASE_URL"
fi

echo ""
echo -e "${_PELI_CYAN}── Test: peli_load_env ──────────────────────────────${_PELI_NC}"

run_test "PELI_BASE_URL is set" \
    test -n "$PELI_BASE_URL"

run_test "LIBRARY_DIR is set" \
    test -n "$LIBRARY_DIR"

run_test "JELLYFIN_API_KEY is set" \
    test -n "$JELLYFIN_API_KEY"

run_test "PROCULA_API_KEY is set" \
    test -n "$PROCULA_API_KEY"

run_test "JELLYFIN_ADMIN_USER is set" \
    test -n "$JELLYFIN_ADMIN_USER"

run_test "PELICULA_TEST_JELLYFIN_PASSWORD is set" \
    test -n "$PELICULA_TEST_JELLYFIN_PASSWORD"

# peli_load_env should fail without PELICULA_TEST_JELLYFIN_PASSWORD
run_test_expect_fail "peli_load_env fails without PELICULA_TEST_JELLYFIN_PASSWORD" \
    bash -c "unset PELICULA_TEST_JELLYFIN_PASSWORD; source '$LIB'; peli_load_env '$ENV_FILE'"

echo ""
echo -e "${_PELI_CYAN}── Test: http_json ─────────────────────────────────${_PELI_NC}"

run_test "http_json GET stack health endpoint returns 200" bash -c "
    source '$LIB'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    http_json GET '${PELI_BASE_URL}/api/pelicula/health' > /dev/null
"

run_test "http_json GET jellyfin with --auth jellyfin returns 200" bash -c "
    source '$LIB'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    http_json GET '${PELI_BASE_URL}/jellyfin/Users' --auth jellyfin > /dev/null
"

run_test "http_json GET settings with --auth pelicula returns 200" bash -c "
    source '$LIB'
    export PELICULA_TEST_JELLYFIN_PASSWORD='${PELICULA_TEST_JELLYFIN_PASSWORD}'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    http_json GET '${PELI_BASE_URL}/api/pelicula/settings' --auth pelicula > /dev/null
"

run_test "http_json fails on bad endpoint (404)" bash -c "
    source '$LIB'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    if http_json GET '${PELI_BASE_URL}/api/pelicula/does_not_exist_xyz' 2>/dev/null; then
        exit 1
    fi
    exit 0
"

echo ""
echo -e "${_PELI_CYAN}── Test: jellyfin_resolve_scan_task_id ─────────────${_PELI_NC}"

# Use lib helpers directly (already sourced; peli_load_env already called)
run_test "jellyfin_resolve_scan_task_id returns a non-empty ID" bash -c "
    source '$LIB'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    task_id=\"\$(jellyfin_resolve_scan_task_id)\"
    test -n \"\$task_id\"
"

run_test "jellyfin_resolve_scan_task_id is idempotent (cached)" bash -c "
    source '$LIB'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    id1=\"\$(jellyfin_resolve_scan_task_id)\"
    id2=\"\$(jellyfin_resolve_scan_task_id)\"
    test \"\$id1\" = \"\$id2\"
"

echo ""
echo -e "${_PELI_CYAN}── Test: jellyfin_resolve_user_id ──────────────────${_PELI_NC}"

run_test "jellyfin_resolve_user_id returns a non-empty ID" bash -c "
    source '$LIB'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    uid=\"\$(jellyfin_resolve_user_id)\"
    test -n \"\$uid\"
"

run_test "jellyfin_resolve_user_id is idempotent (cached)" bash -c "
    source '$LIB'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    id1=\"\$(jellyfin_resolve_user_id)\"
    id2=\"\$(jellyfin_resolve_user_id)\"
    test \"\$id1\" = \"\$id2\"
"

echo ""
echo -e "${_PELI_CYAN}── Test: seed_setting + assert_setting_roundtrip ───${_PELI_NC}"

# Use a safe field: sub_langs. Original value is "en, es" per .env
ORIG_SUB_LANGS="$(http_json GET "${PELI_BASE_URL}/api/pelicula/settings" --auth pelicula 2>/dev/null | jq -r '.sub_langs' 2>/dev/null || echo "")"

run_test "seed_setting sets sub_langs to test value" bash -c "
    source '$LIB'
    export PELICULA_TEST_JELLYFIN_PASSWORD='${PELICULA_TEST_JELLYFIN_PASSWORD}'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    seed_setting sub_langs 'en' > /dev/null
"

run_test "assert_setting_roundtrip passes when value matches" bash -c "
    source '$LIB'
    export PELICULA_TEST_JELLYFIN_PASSWORD='${PELICULA_TEST_JELLYFIN_PASSWORD}'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    # Ensure the setting is at 'en' first
    body='{\"sub_langs\":\"en\"}'
    http_json POST '${PELI_BASE_URL}/api/pelicula/settings' \"\$body\" --auth pelicula > /dev/null
    assert_setting_roundtrip sub_langs 'en'
"

run_test_expect_fail "assert_setting_roundtrip fails when value mismatches" bash -c "
    source '$LIB'
    export PELICULA_TEST_JELLYFIN_PASSWORD='${PELICULA_TEST_JELLYFIN_PASSWORD}'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    # Ensure known state
    body='{\"sub_langs\":\"en\"}'
    http_json POST '${PELI_BASE_URL}/api/pelicula/settings' \"\$body\" --auth pelicula > /dev/null
    assert_setting_roundtrip sub_langs 'definitely_not_this_value_xyz'
"

# Restore original sub_langs before continuing
http_json POST "${PELI_BASE_URL}/api/pelicula/settings" \
    "$(printf '{"sub_langs":"%s"}' "$ORIG_SUB_LANGS")" --auth pelicula > /dev/null 2>/dev/null || true

echo ""
echo -e "${_PELI_CYAN}── Test: tear_down_fixtures restores settings ──────${_PELI_NC}"

run_test "tear_down_fixtures restores mutated sub_langs" bash -c "
    source '$LIB'
    export PELICULA_TEST_JELLYFIN_PASSWORD='${PELICULA_TEST_JELLYFIN_PASSWORD}'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null

    orig=\"\$(http_json GET '${PELI_BASE_URL}/api/pelicula/settings' --auth pelicula | jq -r '.sub_langs')\"
    seed_setting sub_langs 'fr' > /dev/null
    tear_down_fixtures

    # Need a fresh session for verify (jar was cleaned up)
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    restored=\"\$(http_json GET '${PELI_BASE_URL}/api/pelicula/settings' --auth pelicula | jq -r '.sub_langs')\"
    test \"\$orig\" = \"\$restored\"
"

echo ""
echo -e "${_PELI_CYAN}── Test: assert_field_nonempty ─────────────────────${_PELI_NC}"

run_test "assert_field_nonempty passes on non-empty string" bash -c "
    source '$LIB'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    assert_field_nonempty '{\"status\":\"ok\"}' '.status'
"

run_test "assert_field_nonempty passes on number > 0" bash -c "
    source '$LIB'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    assert_field_nonempty '{\"count\":5}' '.count'
"

run_test_expect_fail "assert_field_nonempty fails on null field" bash -c "
    source '$LIB'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    assert_field_nonempty '{\"foo\":null}' '.foo'
"

run_test_expect_fail "assert_field_nonempty fails on empty string" bash -c "
    source '$LIB'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    assert_field_nonempty '{\"foo\":\"\"}' '.foo'
"

run_test_expect_fail "assert_field_nonempty fails on missing field" bash -c "
    source '$LIB'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    assert_field_nonempty '{\"bar\":\"baz\"}' '.missing_key'
"

echo ""
echo -e "${_PELI_CYAN}── Test: seed_library ──────────────────────────────${_PELI_NC}"

# This test exercises the full Jellyfin scan + poll loop.
# Fixture: fixture-a.mp4 (1s 64x64 H.264, confirmed-indexable)
SEEDED_PATH=""

run_test "seed_library copies fixture and indexes it in Jellyfin" bash -c "
    source '$LIB'
    export PELICULA_TEST_JELLYFIN_PASSWORD='${PELICULA_TEST_JELLYFIN_PASSWORD}'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    dest=\"\$(seed_library '$FIXTURE_A' 'LibTest Fixture' '2000')\"
    test -f \"\$dest\"
"

# If seed succeeded, capture the path for assert_orphaned below
if [[ -d "${LIBRARY_DIR}/movies/LibTest Fixture (2000)" ]]; then
    SEEDED_PATH="${LIBRARY_DIR}/movies/LibTest Fixture (2000)/LibTest Fixture (2000).mp4"
fi

run_test "seed_library creates the expected file path" \
    test -f "${LIBRARY_DIR}/movies/LibTest Fixture (2000)/LibTest Fixture (2000).mp4"

echo ""
echo -e "${_PELI_CYAN}── Test: assert_orphaned (fixture dropped outside *arr) ─${_PELI_NC}"

# A fixture dropped directly into LIBRARY_DIR (bypassing *arr) should be orphaned
# in catalog.db — this is the bug-1 failure mode we're testing against.
if [[ -n "$SEEDED_PATH" ]]; then
    run_test "assert_orphaned: directly-dropped fixture is orphaned" bash -c "
        source '$LIB'
        export PELICULA_TEST_JELLYFIN_PASSWORD='${PELICULA_TEST_JELLYFIN_PASSWORD}'
        peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
        assert_orphaned '$SEEDED_PATH'
    "

    run_test_expect_fail "assert_arr_traceable fails on orphaned fixture" bash -c "
        source '$LIB'
        export PELICULA_TEST_JELLYFIN_PASSWORD='${PELICULA_TEST_JELLYFIN_PASSWORD}'
        peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
        assert_arr_traceable '$SEEDED_PATH'
    "
else
    echo -e "  ${_PELI_YELLOW}SKIP${_PELI_NC} assert_orphaned (seed_library did not produce a path)"
fi

echo ""
echo -e "${_PELI_CYAN}── Test: assert_arr_traceable on *arr-known movie ───${_PELI_NC}"

# Best-effort cleanup of the LibTest Fixture seeded above.
# Subshell EXIT traps don't fire in the outer context, so clean up here explicitly.
rm -rf "${LIBRARY_DIR}/movies/LibTest Fixture (2000)" 2>/dev/null || true

# Check whether Radarr has any movie with hasFile=true and test traceability on it.
# If no such movie exists, skip gracefully.
KNOWN_ARR_PATH="$(http_json GET "${PELI_BASE_URL}/api/pelicula/catalog?type=movie" --auth pelicula \
    2>/dev/null | jq -r '.movies[] | select(.hasFile == true) | .movieFile.path' 2>/dev/null | head -1 || echo "")"

if [[ -n "$KNOWN_ARR_PATH" && "$KNOWN_ARR_PATH" != "null" ]]; then
    run_test "assert_arr_traceable passes on *arr-known movie" bash -c "
        source '$LIB'
        export PELICULA_TEST_JELLYFIN_PASSWORD='${PELICULA_TEST_JELLYFIN_PASSWORD}'
        peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
        assert_arr_traceable '$KNOWN_ARR_PATH'
    "
else
    echo -e "  ${_PELI_YELLOW}SKIP${_PELI_NC} assert_arr_traceable on *arr movie (no hasFile=true movies in Radarr)"
fi

echo ""
echo -e "${_PELI_CYAN}── Test: tear_down_fixtures removes seeded paths ───${_PELI_NC}"

run_test "tear_down_fixtures removes seed_library paths" bash -c "
    source '$LIB'
    export PELICULA_TEST_JELLYFIN_PASSWORD='${PELICULA_TEST_JELLYFIN_PASSWORD}'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    seed_library '$FIXTURE_A' 'TearDown Test' '1999' > /dev/null
    tear_down_fixtures
    # Path should no longer exist
    test ! -d '${LIBRARY_DIR}/movies/TearDown Test (1999)'
"

run_test "tear_down_fixtures is safe to call twice (idempotent)" bash -c "
    source '$LIB'
    peli_load_env '$ENV_FILE' '$TARGET_HOST' 2>/dev/null
    tear_down_fixtures
    tear_down_fixtures
"

# ── Final cleanup ─────────────────────────────────────────────────────────────
# The EXIT trap calls tear_down_fixtures for any remaining paths/settings.

echo ""
echo -e "${_PELI_CYAN}═══════════════════════════════════════════════════${_PELI_NC}"
echo -e "  Tests: $_TESTS_RUN  |  Passed: ${_PELI_GREEN}${_TESTS_PASSED}${_PELI_NC}  |  Failed: ${_PELI_RED}${_TESTS_FAILED}${_PELI_NC}"

if (( _TESTS_FAILED > 0 )); then
    echo ""
    echo -e "${_PELI_RED}Failed:${_PELI_NC}"
    for f in "${_FAILURES[@]}"; do
        echo "  - $f"
    done
    echo ""
    exit 1
fi

echo ""
echo -e "${_PELI_GREEN}All tests passed.${_PELI_NC}"
echo ""
exit 0
