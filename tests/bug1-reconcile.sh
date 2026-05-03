#!/usr/bin/env bash
# tests/bug1-reconcile.sh — Bug 1: catalog orphan reconciler test.
#
# Requires:
#   PELICULA_TEST_JELLYFIN_PASSWORD  — Jellyfin admin password (Phase 1A hard requirement)
#   PELICULA_TEST_JELLYFIN_USER      — Jellyfin admin username (default: gwen; .env may have stale 'admin')
#
# Usage:
#   PELICULA_TEST_JELLYFIN_PASSWORD=<pw> PELICULA_TEST_JELLYFIN_USER=gwen \
#     bash tests/bug1-reconcile.sh
#
# Runs against the stack on localhost:7354 by default.
# The stack must be up and indexed before running.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib.sh"

ENV_FILE="${SCRIPT_DIR}/../.env"
peli_load_env "$ENV_FILE"

trap tear_down_fixtures EXIT

FIXTURE="${SCRIPT_DIR}/fixtures/media/fixture-a.mp4"
TITLE="Reconcile Test A"
YEAR="2099"

_peli_log "=== Bug 1: catalog orphan reconciler test ==="

# ── Step 1: seed fixture into library (bypasses *arr, bypasses catalog write paths) ──
_peli_log "Step 1: seed fixture into Jellyfin library"
DEST_PATH="$(seed_library "$FIXTURE" "$TITLE" "$YEAR")"
_peli_ok "Seeded: $DEST_PATH"

# Compute the container-internal path that Jellyfin (and catalog.db) use.
CONTAINER_PATH="$(peli_container_path "$DEST_PATH")"

# ── Step 2: assert item is orphaned (in Jellyfin, absent from catalog.db and Radarr) ──
_peli_log "Step 2: assert item is orphaned (pre-fix failure state)"
assert_orphaned "$CONTAINER_PATH"
_peli_ok "Confirmed orphaned: $CONTAINER_PATH"

# ── Step 3: POST to reconcile endpoint, assert added >= 1 ────────────────────
_peli_log "Step 3: POST /api/pelicula/catalog/reconcile"
RECONCILE_RESP="$(http_json POST /api/pelicula/catalog/reconcile '' --auth pelicula)"
_peli_ok "Reconcile response: $RECONCILE_RESP"

ADDED="$(echo "$RECONCILE_RESP" | jq -r '.added // 0')"
if (( ADDED < 1 )); then
    _peli_err "Expected added >= 1, got added=$ADDED"
    exit 1
fi
_peli_ok "Reconciler added $ADDED item(s)"

# ── Step 4: assert item is now in catalog.db (detail returns in_catalog=true) ──
_peli_log "Step 4: assert item is now in catalog.db"
ENCODED_PATH="$(printf '%s' "$CONTAINER_PATH" | jq -sRr @uri)"
DETAIL="$(http_json GET "/api/pelicula/catalog/detail?path=${ENCODED_PATH}" --auth pelicula)"
_peli_ok "Detail response: $(echo "$DETAIL" | jq -c '.')"

IN_CATALOG="$(echo "$DETAIL" | jq -r '.in_catalog // false')"
if [[ "$IN_CATALOG" != "true" ]]; then
    _peli_err "Expected in_catalog=true after reconcile, got: $IN_CATALOG"
    exit 1
fi
_peli_ok "Item is now in catalog.db (in_catalog=true)"

CATALOG_PATH="$(echo "$DETAIL" | jq -r '.path // empty')"
if [[ "$CATALOG_PATH" != "$CONTAINER_PATH" ]]; then
    _peli_err "Expected path=$CONTAINER_PATH, got: $CATALOG_PATH"
    exit 1
fi
_peli_ok "Catalog path matches: $CATALOG_PATH"

# Assert source == "reconcile" if the detail endpoint exposes it
SOURCE="$(echo "$DETAIL" | jq -r '.source // empty')"
if [[ -n "$SOURCE" && "$SOURCE" != "null" ]]; then
    if [[ "$SOURCE" != "reconcile" ]]; then
        _peli_err "Expected source=reconcile, got: $SOURCE"
        exit 1
    fi
    _peli_ok "source=reconcile confirmed"
fi

# ── Step 5: idempotency — second reconcile must return added == 0 ─────────────
_peli_log "Step 5: idempotency check — second reconcile"
RECONCILE_RESP2="$(http_json POST /api/pelicula/catalog/reconcile '' --auth pelicula)"
_peli_ok "Second reconcile response: $RECONCILE_RESP2"

ADDED2="$(echo "$RECONCILE_RESP2" | jq -r '.added // -1')"
if [[ "$ADDED2" != "0" ]]; then
    _peli_err "Expected added=0 on second run (idempotency), got added=$ADDED2"
    exit 1
fi
_peli_ok "Idempotency confirmed: added=0 on second run"

# Re-fetch detail to ensure row unchanged
DETAIL2="$(http_json GET "/api/pelicula/catalog/detail?path=${ENCODED_PATH}" --auth pelicula)"
IN_CATALOG2="$(echo "$DETAIL2" | jq -r '.in_catalog // false')"
if [[ "$IN_CATALOG2" != "true" ]]; then
    _peli_err "Expected in_catalog=true after second reconcile, got: $IN_CATALOG2"
    exit 1
fi
_peli_ok "Row unchanged after idempotency run"

_peli_ok "=== Bug 1 reconciler test PASSED ==="
