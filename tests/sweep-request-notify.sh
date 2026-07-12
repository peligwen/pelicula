#!/usr/bin/env bash
# tests/sweep-request-notify.sh — HTTP smoke tests for the in-app viewer
# availability-notification endpoints (flow-improvements Phase 5).
#
# Covers:
#   A. GET /api/pelicula/requests/unseen (Viewer+) → 200 with a JSON body
#      carrying .count (a number) and .items (an array).
#   B. POST /api/pelicula/requests/acknowledge (Viewer+) → 200 with a JSON
#      body carrying .acknowledged (a number). An empty body means
#      "acknowledge all of the caller's unseen-available requests" — on this
#      harness's admin session that's typically zero rows, so this is a
#      safe, idempotent call (acknowledging zero rows twice is a no-op).
#
# Cross-viewer ownership scoping (a foreign id in the acknowledge body must
# not mutate another user's row) is NOT re-tested here: this harness
# (tests/lib.sh) only provisions a single authenticated session, so there is
# no second viewer identity to exercise that boundary against a live stack.
# That security-critical path is covered exhaustively at the Go unit level
# by middleware/internal/peligrosa/requests_test.go's
# TestHandleRequestAcknowledge_ForeignIDDoesNotMutateOtherUser, and the
# route-precedence guarantee (these two endpoints must not be swallowed by
# the admin-gated "/api/pelicula/requests/" subtree) is covered by
# routes_test.go's TestRequest{Unseen,Acknowledge}Route_ViewerNotBlockedByAdminSubtree.
#
# All checks require pelicula session auth (both endpoints are Viewer+ but
# still require a session — there is no unauthenticated surface here worth
# checking on its own).
# Run auth-free portions only:
#   SKIP_AUTH_CHECKS=1 bash tests/sweep-request-notify.sh
#
# Auth-required checks must be run manually with PELICULA_TEST_JELLYFIN_PASSWORD set.
#
# Usage:
#   PELICULA_TEST_JELLYFIN_PASSWORD=<pw> PELICULA_TEST_JELLYFIN_USER=gwen \
#     bash tests/sweep-request-notify.sh [--target HOST:PORT] [--skip-auth]

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

# Both checks in this sweep require pelicula session auth — there is no
# unauthenticated surface here (mirrors sweep-journey.sh).
if [[ -n "$SKIP_AUTH_CHECKS" ]]; then
    echo "sweep-request-notify: all checks skipped (auth required)" >&2
    exit 0
fi

export PELICULA_TEST_JELLYFIN_PASSWORD="${PELICULA_TEST_JELLYFIN_PASSWORD:-}"
peli_load_env "$ENV_FILE" "$TARGET_HOST"

if [[ -n "${_OVERRIDE_PORT:-}" ]]; then
    PELI_BASE_URL="http://${TARGET_HOST}:${_OVERRIDE_PORT}"
    _peli_log "Port override: PELI_BASE_URL=$PELI_BASE_URL"
fi

_peli_log "=== sweep-request-notify: HTTP smoke tests ==="

# ── Test A: GET /api/pelicula/requests/unseen → 200 + {count, items} ─────────
_peli_log "Test A: GET /api/pelicula/requests/unseen → 200 with count + items"

a_resp="$(http_json GET /api/pelicula/requests/unseen --auth pelicula)"

a_count_type="$(echo "$a_resp" | jq -r '.count | type' 2>/dev/null)"
if [[ "$a_count_type" != "number" ]]; then
    _peli_err "Test A FAIL: .count is '${a_count_type}' (expected 'number'): $(echo "$a_resp" | jq -c '.' 2>/dev/null | head -c 200)"
    exit 1
fi

a_items_type="$(echo "$a_resp" | jq -r '.items | type' 2>/dev/null)"
if [[ "$a_items_type" != "array" ]]; then
    _peli_err "Test A FAIL: .items is '${a_items_type}' (expected 'array'): $(echo "$a_resp" | jq -c '.' 2>/dev/null | head -c 200)"
    exit 1
fi

_peli_ok "Test A passed: /requests/unseen returned count=$(echo "$a_resp" | jq -r '.count') with an items array"

# ── Test B: POST /api/pelicula/requests/acknowledge → 200 + {acknowledged} ───
_peli_log "Test B: POST /api/pelicula/requests/acknowledge (empty body) → 200 with acknowledged"

# Empty body = "acknowledge all of the caller's unseen-available requests".
# Idempotent and side-effect-free from this suite's perspective: the admin
# session used by this harness typically has zero such rows, and even if it
# has some, acknowledging them here is the same effect the dashboard's own
# users-tab switch already triggers in normal use.
b_resp="$(http_json POST /api/pelicula/requests/acknowledge '{}' --auth pelicula)"

b_ack_type="$(echo "$b_resp" | jq -r '.acknowledged | type' 2>/dev/null)"
if [[ "$b_ack_type" != "number" ]]; then
    _peli_err "Test B FAIL: .acknowledged is '${b_ack_type}' (expected 'number'): $(echo "$b_resp" | jq -c '.' 2>/dev/null | head -c 200)"
    exit 1
fi

_peli_ok "Test B passed: /requests/acknowledge returned acknowledged=$(echo "$b_resp" | jq -r '.acknowledged')"

_peli_ok "=== sweep-request-notify: all checks passed ==="
