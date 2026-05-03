#!/usr/bin/env bash
# tests/lib.sh — Sourced test library for Pelicula fixture-driven harness.
#
# Usage:
#   source /path/to/tests/lib.sh
#   peli_load_env /path/to/pelicula/.env
#
# All public functions return 0 on success, non-zero on failure.
# Internal helpers are prefixed with _peli_.
#
# Required env (set before sourcing or after peli_load_env):
#   PELICULA_TEST_JELLYFIN_PASSWORD  — Jellyfin admin password (never in .env)
#
# Optional env overrides:
#   PELICULA_TEST_JELLYFIN_USER  — Jellyfin admin username (overrides JELLYFIN_ADMIN_USER
#                                   from .env; useful when .env has a stale value)
#
# Set by peli_load_env (from .env):
#   PELI_BASE_URL      — http://HOST:PORT (e.g. http://localhost:7354)
#   LIBRARY_DIR        — host-side media library root
#   JELLYFIN_API_KEY   — Jellyfin admin API key
#   PROCULA_API_KEY    — Procula API key
#   JELLYFIN_ADMIN_USER — Jellyfin admin username (default: admin)
#
# Side-effects / cleanup:
#   tear_down_fixtures  — idempotent; call in test EXIT trap
#   All seeded fixture paths are tracked in _PELI_SEEDED_PATHS[]
#   Settings modified by seed_setting are tracked in _PELI_ORIG_SETTINGS{}

set -euo pipefail

# ── Internal state ────────────────────────────────────────────────────────────

_PELI_SESSION_JAR=""         # path to curl cookie jar for pelicula auth
_PELI_SESSION_VALID=0        # 1 once we have a valid session cookie
_PELI_SCAN_TASK_ID=""        # cached Jellyfin "Scan Media Library" task ID
_PELI_JF_USER_ID=""          # cached Jellyfin user ID for item queries
_PELI_SEEDED_PATHS=()        # fixture paths seeded; removed in tear_down_fixtures
_PELI_ORIG_SETTINGS_FILE=""  # temp file: "key\tvalue" pairs stashed by seed_setting

# ── Colors ────────────────────────────────────────────────────────────────────

_PELI_RED='\033[0;31m'
_PELI_GREEN='\033[0;32m'
_PELI_YELLOW='\033[0;33m'
_PELI_CYAN='\033[0;36m'
_PELI_NC='\033[0m'

_peli_log()  { echo -e "${_PELI_CYAN}[lib]${_PELI_NC} $*" >&2; }
_peli_ok()   { echo -e "${_PELI_GREEN}[ok]${_PELI_NC} $*" >&2; }
_peli_warn() { echo -e "${_PELI_YELLOW}[warn]${_PELI_NC} $*" >&2; }
_peli_err()  { echo -e "${_PELI_RED}[err]${_PELI_NC} $*" >&2; }

# ── peli_load_env ─────────────────────────────────────────────────────────────

# peli_load_env ENV_FILE [HOST]
#   Source the .env file and export test globals.
#   HOST defaults to localhost. Uses PELICULA_PORT from .env for PELI_BASE_URL.
#   Fails loudly if PELICULA_TEST_JELLYFIN_PASSWORD is unset.
peli_load_env() {
    local env_file="${1:-}"
    local host="${2:-localhost}"

    if [[ -z "$env_file" || ! -f "$env_file" ]]; then
        _peli_err "peli_load_env: .env file not found: ${env_file:-<empty>}"
        return 1
    fi

    # Parse key=value pairs from .env (handles quoted values, skips comments/blanks)
    local line key val
    while IFS= read -r line; do
        # Skip blanks and comment lines
        [[ -z "$line" || "$line" == \#* ]] && continue
        key="${line%%=*}"
        val="${line#*=}"
        # Strip surrounding double-quotes if present
        val="${val#\"}"
        val="${val%\"}"
        # Export only the keys we need
        case "$key" in
            LIBRARY_DIR)         export LIBRARY_DIR="$val" ;;
            JELLYFIN_API_KEY)    export JELLYFIN_API_KEY="$val" ;;
            PROCULA_API_KEY)     export PROCULA_API_KEY="$val" ;;
            JELLYFIN_ADMIN_USER) export JELLYFIN_ADMIN_USER="$val" ;;
            PELICULA_PORT)       local _port="$val" ;;
        esac
    done < "$env_file"

    # Defaults; PELICULA_TEST_JELLYFIN_USER overrides the .env value when set
    if [[ -n "${PELICULA_TEST_JELLYFIN_USER:-}" ]]; then
        export JELLYFIN_ADMIN_USER="$PELICULA_TEST_JELLYFIN_USER"
    else
        export JELLYFIN_ADMIN_USER="${JELLYFIN_ADMIN_USER:-admin}"
    fi
    local port="${_port:-7354}"
    export PELI_BASE_URL="http://${host}:${port}"

    # Require the password that deliberately lives outside .env
    if [[ -z "${PELICULA_TEST_JELLYFIN_PASSWORD:-}" ]]; then
        _peli_err "PELICULA_TEST_JELLYFIN_PASSWORD is required but not set."
        _peli_err "Export it before sourcing lib.sh or calling peli_load_env."
        return 1
    fi

    # Validate required .env keys were found
    local missing=()
    [[ -z "${LIBRARY_DIR:-}"      ]] && missing+=("LIBRARY_DIR")
    [[ -z "${JELLYFIN_API_KEY:-}" ]] && missing+=("JELLYFIN_API_KEY")
    [[ -z "${PROCULA_API_KEY:-}"  ]] && missing+=("PROCULA_API_KEY")
    if (( ${#missing[@]} > 0 )); then
        _peli_err "peli_load_env: missing required .env keys: ${missing[*]}"
        return 1
    fi

    _PELI_SESSION_JAR="$(mktemp /tmp/peli_session_XXXXXX)"
    _PELI_ORIG_SETTINGS_FILE="$(mktemp /tmp/peli_orig_settings_XXXXXX)"

    _peli_log "Loaded env: BASE_URL=$PELI_BASE_URL LIBRARY_DIR=$LIBRARY_DIR"
    return 0
}

# ── _peli_ensure_session ──────────────────────────────────────────────────────

# Obtain a pelicula session cookie and cache it in _PELI_SESSION_JAR.
# Called lazily by http_json when --auth pelicula is requested.
_peli_ensure_session() {
    if [[ $_PELI_SESSION_VALID -eq 1 ]]; then
        return 0
    fi

    local url="${PELI_BASE_URL}/api/pelicula/auth/login"
    local body
    body="$(printf '{"username":"%s","password":"%s"}' \
        "$JELLYFIN_ADMIN_USER" "$PELICULA_TEST_JELLYFIN_PASSWORD")"

    local http_code
    http_code="$(curl -sf \
        -c "$_PELI_SESSION_JAR" \
        -b "$_PELI_SESSION_JAR" \
        -w "%{http_code}" \
        -o /dev/null \
        -X POST \
        -H "Content-Type: application/json" \
        -H "Origin: ${PELI_BASE_URL}" \
        -d "$body" \
        "$url" 2>/dev/null)"

    if [[ "$http_code" != "200" ]]; then
        _peli_err "_peli_ensure_session: login failed (HTTP $http_code)"
        _peli_err "  URL: $url  user: $JELLYFIN_ADMIN_USER"
        return 1
    fi

    _PELI_SESSION_VALID=1
    return 0
}

# ── http_json ─────────────────────────────────────────────────────────────────

# http_json METHOD URL [BODY] [--auth jellyfin|pelicula|procula]
#
#   Sends an HTTP request and prints the response body on stdout.
#   On non-2xx status or non-JSON response, prints an error to stderr and returns 1.
#   BODY may be omitted (or empty string) for GET/DELETE.
#   --auth flag can appear anywhere in the argument list.
#
# Example:
#   body="$(http_json GET /api/pelicula/settings --auth pelicula)"
#   http_json POST /api/pelicula/catalog/backfill '' --auth pelicula
http_json() {
    local method="${1:-GET}"
    local path="${2:-}"
    shift 2 || true

    local body=""
    local auth_mode=""

    # Parse remaining args: optional body then optional --auth flag
    while (( $# > 0 )); do
        case "$1" in
            --auth)
                auth_mode="${2:-}"
                shift 2
                ;;
            *)
                body="$1"
                shift
                ;;
        esac
    done

    # Build full URL (allow callers to pass a full URL or just a path)
    local url
    if [[ "$path" == http://* || "$path" == https://* ]]; then
        url="$path"
    else
        url="${PELI_BASE_URL}${path}"
    fi

    # Build curl args array
    local -a curl_args=(
        -s
        -w "\n%{http_code}"
        -X "$method"
    )

    # Auth injection
    case "$auth_mode" in
        jellyfin)
            curl_args+=(-H "X-MediaBrowser-Token: ${JELLYFIN_API_KEY}")
            ;;
        procula)
            curl_args+=(-H "X-API-Key: ${PROCULA_API_KEY}")
            ;;
        pelicula)
            _peli_ensure_session || return 1
            curl_args+=(
                -c "$_PELI_SESSION_JAR"
                -b "$_PELI_SESSION_JAR"
                -H "Origin: ${PELI_BASE_URL}"
            )
            ;;
        "")
            # No auth
            ;;
        *)
            _peli_err "http_json: unknown --auth mode: $auth_mode"
            return 1
            ;;
    esac

    # Body
    if [[ -n "$body" ]]; then
        curl_args+=(-H "Content-Type: application/json" -d "$body")
    fi

    curl_args+=("$url")

    # Execute
    local raw
    raw="$(curl "${curl_args[@]}" 2>/dev/null)"

    # Split body and status code (last line after \n separator)
    local resp_body http_code
    http_code="${raw##*$'\n'}"
    resp_body="${raw%$'\n'*}"

    # Check 2xx
    if [[ "$http_code" != 2* ]]; then
        _peli_err "http_json: $method $url → HTTP $http_code"
        _peli_err "  body: ${resp_body:0:200}"
        return 1
    fi

    # Validate JSON (best-effort; empty body is ok for 204)
    if [[ -n "$resp_body" ]]; then
        if ! echo "$resp_body" | jq empty 2>/dev/null; then
            _peli_err "http_json: $method $url → non-JSON response body"
            _peli_err "  body: ${resp_body:0:200}"
            return 1
        fi
    fi

    echo "$resp_body"
    return 0
}

# ── peli_url_encode ───────────────────────────────────────────────────────────

# peli_url_encode STRING
#   Percent-encode STRING using jq's @uri filter and print the result on stdout.
#   Falls back to a minimal sed pass if jq is unavailable (should not happen in
#   practice — jq is a hard dependency of the test harness).
peli_url_encode() {
    local input="${1:-}"
    printf '%s' "$input" | jq -sRr @uri 2>/dev/null \
        || printf '%s' "$input" | sed 's| |%20|g; s|/|%2F|g; s|&|%26|g'
}

# ── peli_container_path ───────────────────────────────────────────────────────

# peli_container_path HOST_PATH
#   Translate HOST_PATH to the container-internal equivalent path by replacing
#   the host LIBRARY_DIR prefix with /media.
#   Requires LIBRARY_DIR to be set (via peli_load_env).
#   Example:
#     HOST_PATH=/Users/gwen/media/movies/Foo/Foo.mp4
#     → /media/movies/Foo/Foo.mp4
peli_container_path() {
    local host_path="${1:-}"
    if [[ -z "$host_path" ]]; then
        _peli_err "peli_container_path: HOST_PATH required"
        return 1
    fi
    echo "${host_path/#${LIBRARY_DIR}//media}"
}

# ── assert_static_asset ───────────────────────────────────────────────────────

# assert_static_asset URL [GREP_PATTERN]
#   Fetch URL (prepended with PELI_BASE_URL if it starts with /).
#   Assert HTTP 200 and non-empty body.
#   If GREP_PATTERN is provided, also assert body contains the pattern (grep -q).
#   Returns 0 on success, 1 on failure.
assert_static_asset() {
    local url="${1:-}"
    local pattern="${2:-}"

    if [[ -z "$url" ]]; then
        _peli_err "assert_static_asset: URL required"
        return 1
    fi

    # Allow caller to pass full URL or just a path
    local full_url
    if [[ "$url" == http://* || "$url" == https://* ]]; then
        full_url="$url"
    else
        full_url="${PELI_BASE_URL}${url}"
    fi

    local body
    body="$(curl -sf "$full_url" 2>/dev/null)" || {
        _peli_err "assert_static_asset: GET $full_url failed (non-200 or connection error)"
        return 1
    }

    if [[ -z "$body" ]]; then
        _peli_err "assert_static_asset: GET $full_url returned empty body"
        return 1
    fi

    if [[ -n "$pattern" ]]; then
        if ! echo "$body" | grep -q "$pattern" 2>/dev/null; then
            _peli_err "assert_static_asset: GET $full_url body does not match pattern: $pattern"
            _peli_err "  Got: ${body:0:200}"
            return 1
        fi
    fi

    return 0
}

# ── assert_public_json_nonempty ───────────────────────────────────────────────

# assert_public_json_nonempty URL JQ_FIELD [JQ_FIELD ...]
#   Fetch URL (no auth) via http_json; assert 200 + valid JSON.
#   Assert each JQ_FIELD expression is non-null and non-empty.
#   URL may be a full URL or a path (prepended with PELI_BASE_URL).
#   Returns 0 on success, 1 on first assertion failure.
assert_public_json_nonempty() {
    local url="${1:-}"
    shift || true

    if [[ -z "$url" ]]; then
        _peli_err "assert_public_json_nonempty: URL required"
        return 1
    fi

    local body
    body="$(http_json GET "$url")" || {
        _peli_err "assert_public_json_nonempty: GET $url failed"
        return 1
    }

    local field
    for field in "$@"; do
        assert_field_nonempty "$body" "$field" || return 1
    done

    return 0
}

# ── assert_authed_json_nonempty ───────────────────────────────────────────────

# assert_authed_json_nonempty AUTH PATH JQ_FIELD [JQ_FIELD ...]
#   Fetch PATH with the given AUTH mode (pelicula|jellyfin|procula) via http_json.
#   Assert each JQ_FIELD expression is non-null and non-empty.
#   PATH may be a full URL or a path (prepended with PELI_BASE_URL).
#   Returns 0 on success, 1 on first assertion failure.
assert_authed_json_nonempty() {
    local auth_mode="${1:-}"
    local path="${2:-}"
    shift 2 || true

    if [[ -z "$auth_mode" || -z "$path" ]]; then
        _peli_err "assert_authed_json_nonempty: AUTH and PATH required"
        return 1
    fi

    local body
    body="$(http_json GET "$path" --auth "$auth_mode")" || {
        _peli_err "assert_authed_json_nonempty: GET $path (--auth $auth_mode) failed"
        return 1
    }

    local field
    for field in "$@"; do
        assert_field_nonempty "$body" "$field" || return 1
    done

    return 0
}

# ── jellyfin_resolve_scan_task_id ─────────────────────────────────────────────

# jellyfin_resolve_scan_task_id
#   Prints the Jellyfin scheduled task ID for "Scan Media Library".
#   Result is cached in _PELI_SCAN_TASK_ID for the process lifetime.
jellyfin_resolve_scan_task_id() {
    if [[ -n "$_PELI_SCAN_TASK_ID" ]]; then
        echo "$_PELI_SCAN_TASK_ID"
        return 0
    fi

    local tasks
    tasks="$(http_json GET "${PELI_BASE_URL}/jellyfin/ScheduledTasks" --auth jellyfin)" || {
        _peli_err "jellyfin_resolve_scan_task_id: failed to fetch scheduled tasks"
        return 1
    }

    local task_id
    task_id="$(echo "$tasks" | jq -r '.[] | select(.Name == "Scan Media Library") | .Id' 2>/dev/null | head -1)"

    if [[ -z "$task_id" || "$task_id" == "null" ]]; then
        _peli_err "jellyfin_resolve_scan_task_id: 'Scan Media Library' task not found"
        return 1
    fi

    _PELI_SCAN_TASK_ID="$task_id"
    echo "$task_id"
    return 0
}

# ── jellyfin_resolve_user_id ──────────────────────────────────────────────────

# jellyfin_resolve_user_id
#   Prints the first Jellyfin user's Id (used for item queries).
#   Result is cached in _PELI_JF_USER_ID for the process lifetime.
jellyfin_resolve_user_id() {
    if [[ -n "$_PELI_JF_USER_ID" ]]; then
        echo "$_PELI_JF_USER_ID"
        return 0
    fi

    local users
    users="$(http_json GET "${PELI_BASE_URL}/jellyfin/Users" --auth jellyfin)" || {
        _peli_err "jellyfin_resolve_user_id: failed to fetch users"
        return 1
    }

    local uid
    uid="$(echo "$users" | jq -r '.[0].Id' 2>/dev/null)"

    if [[ -z "$uid" || "$uid" == "null" ]]; then
        _peli_err "jellyfin_resolve_user_id: no Jellyfin users found"
        return 1
    fi

    _PELI_JF_USER_ID="$uid"
    echo "$uid"
    return 0
}

# ── seed_library ──────────────────────────────────────────────────────────────

# seed_library FIXTURE_PATH TITLE YEAR
#   Copies FIXTURE_PATH into ${LIBRARY_DIR}/movies/TITLE (YEAR)/TITLE (YEAR).mp4,
#   triggers a Jellyfin scan, and polls until the item appears (≤10s).
#   On success, prints the destination path.
#   Tracks the destination in _PELI_SEEDED_PATHS[] for tear_down_fixtures.
seed_library() {
    local fixture="${1:-}"
    local title="${2:-}"
    local year="${3:-}"

    if [[ -z "$fixture" || -z "$title" || -z "$year" ]]; then
        _peli_err "seed_library: FIXTURE TITLE YEAR required"
        return 1
    fi
    if [[ ! -f "$fixture" ]]; then
        _peli_err "seed_library: fixture file not found: $fixture"
        return 1
    fi

    local dest_dir="${LIBRARY_DIR}/movies/${title} (${year})"
    local dest_file="${dest_dir}/${title} (${year}).mp4"

    mkdir -p "$dest_dir"
    cp "$fixture" "$dest_file"
    _PELI_SEEDED_PATHS+=("$dest_dir")
    _peli_log "seed_library: copied fixture → $dest_file"

    # Trigger Jellyfin scan
    local uid
    uid="$(jellyfin_resolve_user_id)" || return 1

    local encoded_title
    encoded_title="$(peli_url_encode "$title")"

    # _peli_jf_poll_title UID ENCODED_TITLE TIMEOUT_SECS
    # Returns 0 and sets found=1 if the item appears within timeout.
    local found=0
    local count

    _peli_jf_poll() {
        local _uid="$1" _enc="$2" _secs="$3"
        local _deadline=$(( $(date +%s) + _secs ))
        local _resp _cnt
        while (( $(date +%s) < _deadline )); do
            _resp="$(curl -sf \
                -H "X-MediaBrowser-Token: ${JELLYFIN_API_KEY}" \
                "${PELI_BASE_URL}/jellyfin/Users/${_uid}/Items?searchTerm=${_enc}&IncludeItemTypes=Movie&Recursive=true" \
                2>/dev/null)" || true
            if [[ -n "$_resp" ]]; then
                _cnt="$(echo "$_resp" | jq -r '.TotalRecordCount // 0' 2>/dev/null || echo 0)"
                if (( _cnt > 0 )); then
                    return 0
                fi
            fi
            sleep 0.2
        done
        return 1
    }

    # Check if item is already in Jellyfin before triggering a scan (idempotent re-run)
    if _peli_jf_poll "$uid" "$encoded_title" 1; then
        found=1
        _peli_log "seed_library: '$title ($year)' already in Jellyfin"
    fi

    if (( found == 0 )); then
        # Trigger Jellyfin scan
        local task_id
        task_id="$(jellyfin_resolve_scan_task_id)" || return 1
        http_json POST "${PELI_BASE_URL}/jellyfin/ScheduledTasks/Running/${task_id}" '' --auth jellyfin \
            > /dev/null || {
            _peli_err "seed_library: failed to trigger Jellyfin scan"
            return 1
        }

        # Poll for item appearance (200ms intervals, 15s timeout)
        if _peli_jf_poll "$uid" "$encoded_title" 15; then
            found=1
        else
            # Retry: trigger one more scan and poll again (handles concurrent scan interference)
            _peli_warn "seed_library: first poll timed out; retrying scan"
            http_json POST "${PELI_BASE_URL}/jellyfin/ScheduledTasks/Running/${task_id}" '' \
                --auth jellyfin > /dev/null 2>/dev/null || true
            if _peli_jf_poll "$uid" "$encoded_title" 15; then
                found=1
            fi
        fi
    fi

    if (( found == 0 )); then
        _peli_err "seed_library: item '$title' did not appear in Jellyfin within 30s"
        return 1
    fi

    _peli_log "seed_library: '$title ($year)' indexed in Jellyfin"
    echo "$dest_file"
    return 0
}

# ── seed_setting ──────────────────────────────────────────────────────────────

# seed_setting KEY VALUE
#   POST a settings update with {KEY: VALUE}, saving the original value
#   for restoration by tear_down_fixtures.
#   KEY is the JSON field name from settingsResponse (e.g. "sub_langs").
seed_setting() {
    local key="${1:-}"
    local value="${2:-}"

    if [[ -z "$key" ]]; then
        _peli_err "seed_setting: KEY required"
        return 1
    fi

    # Fetch current value to stash for restore
    local current_settings
    current_settings="$(http_json GET "${PELI_BASE_URL}/api/pelicula/settings" --auth pelicula)" || {
        _peli_err "seed_setting: failed to fetch current settings"
        return 1
    }

    local orig_val
    orig_val="$(echo "$current_settings" | jq -r --arg k "$key" '.[$k] // empty' 2>/dev/null)"

    # Only stash once per key (don't overwrite original on repeated calls).
    # Use tab-separated key<TAB>value in the temp file; grep for exact key match.
    if [[ -n "$_PELI_ORIG_SETTINGS_FILE" && -f "$_PELI_ORIG_SETTINGS_FILE" ]]; then
        if ! grep -q "^${key}	" "$_PELI_ORIG_SETTINGS_FILE" 2>/dev/null; then
            printf '%s\t%s\n' "$key" "$orig_val" >> "$_PELI_ORIG_SETTINGS_FILE"
        fi
    fi

    # POST the new value — use jq for safe JSON serialisation
    local body
    body="$(jq -n --arg k "$key" --arg v "$value" '{($k):$v}')"
    http_json POST "${PELI_BASE_URL}/api/pelicula/settings" "$body" --auth pelicula > /dev/null || {
        _peli_err "seed_setting: failed to POST settings update"
        return 1
    }

    _peli_log "seed_setting: set $key=$value (was: ${orig_val:-<empty>})"
    return 0
}

# ── tear_down_fixtures ────────────────────────────────────────────────────────

# tear_down_fixtures
#   Best-effort idempotent cleanup:
#   - Removes all paths tracked in _PELI_SEEDED_PATHS[]
#   - Restores all settings stashed in _PELI_ORIG_SETTINGS{}
#   - Cleans up session cookie jar
#   Always returns 0 (safe to call in EXIT trap).
tear_down_fixtures() {
    local failed=0

    # Remove seeded media paths
    local p
    for p in "${_PELI_SEEDED_PATHS[@]+"${_PELI_SEEDED_PATHS[@]}"}"; do
        if [[ -e "$p" ]]; then
            rm -rf "$p" 2>/dev/null || {
                _peli_warn "tear_down_fixtures: failed to remove $p"
                failed=1
            }
        fi
    done
    _PELI_SEEDED_PATHS=()

    # Restore mutated settings from the temp file (tab-separated key<TAB>value)
    if [[ -n "$_PELI_ORIG_SETTINGS_FILE" && -f "$_PELI_ORIG_SETTINGS_FILE" ]]; then
        local k v body
        while IFS=$'\t' read -r k v; do
            [[ -z "$k" ]] && continue
            if [[ -n "$v" ]]; then
                body="$(jq -n --arg k "$k" --arg v "$v" '{($k):$v}')"
                http_json POST "${PELI_BASE_URL}/api/pelicula/settings" "$body" --auth pelicula \
                    > /dev/null 2>/dev/null || {
                    _peli_warn "tear_down_fixtures: failed to restore $k=$v"
                    failed=1
                }
            fi
        done < "$_PELI_ORIG_SETTINGS_FILE"
        rm -f "$_PELI_ORIG_SETTINGS_FILE" 2>/dev/null || true
        _PELI_ORIG_SETTINGS_FILE=""
    fi

    # Clean up session jar
    if [[ -n "$_PELI_SESSION_JAR" && -f "$_PELI_SESSION_JAR" ]]; then
        rm -f "$_PELI_SESSION_JAR" 2>/dev/null || true
        _PELI_SESSION_JAR=""
        _PELI_SESSION_VALID=0
    fi

    return 0
}

# ── assert_field_nonempty ─────────────────────────────────────────────────────

# assert_field_nonempty JSON JQ_PATH
#   Assert that JSON | JQ_PATH is non-null and non-empty.
#   Returns 0 on success, 1 on failure (prints error message).
assert_field_nonempty() {
    local json="${1:-}"
    local jq_path="${2:-}"

    if [[ -z "$json" || -z "$jq_path" ]]; then
        _peli_err "assert_field_nonempty: JSON and JQ_PATH required"
        return 1
    fi

    local val
    val="$(echo "$json" | jq -r "$jq_path" 2>/dev/null)"

    if [[ -z "$val" || "$val" == "null" ]]; then
        _peli_err "assert_field_nonempty: $jq_path is null or empty"
        _peli_err "  JSON fragment: $(echo "$json" | jq -c '.' 2>/dev/null | head -c 300)"
        return 1
    fi

    return 0
}

# ── assert_setting_roundtrip ──────────────────────────────────────────────────

# assert_setting_roundtrip KEY EXPECTED_VALUE
#   GET /api/pelicula/settings and assert KEY == EXPECTED_VALUE.
assert_setting_roundtrip() {
    local key="${1:-}"
    local expected="${2:-}"

    if [[ -z "$key" ]]; then
        _peli_err "assert_setting_roundtrip: KEY required"
        return 1
    fi

    local settings
    settings="$(http_json GET "${PELI_BASE_URL}/api/pelicula/settings" --auth pelicula)" || {
        _peli_err "assert_setting_roundtrip: failed to fetch settings"
        return 1
    }

    local actual
    actual="$(echo "$settings" | jq -r --arg k "$key" '.[$k] // empty' 2>/dev/null)"

    if [[ "$actual" != "$expected" ]]; then
        _peli_err "assert_setting_roundtrip: $key expected='$expected' actual='$actual'"
        return 1
    fi

    return 0
}

# ── assert_arr_traceable ──────────────────────────────────────────────────────

# assert_arr_traceable FILE_PATH
#   Assert that FILE_PATH is *arr-traceable: either
#   (a) /api/pelicula/catalog/detail?path=FILE_PATH → in_catalog=true, OR
#   (b) GET /api/pelicula/catalog?type=movie contains a movie with
#       movieFile.path == FILE_PATH
#   Returns 0 if traceable, 1 if orphaned.
assert_arr_traceable() {
    local file_path="${1:-}"

    if [[ -z "$file_path" ]]; then
        _peli_err "assert_arr_traceable: FILE_PATH required"
        return 1
    fi

    # Check (a): catalog.db via detail endpoint
    local encoded_path
    encoded_path="$(peli_url_encode "$file_path")"

    local detail
    detail="$(http_json GET "${PELI_BASE_URL}/api/pelicula/catalog/detail?path=${encoded_path}" --auth pelicula 2>/dev/null)" || true

    if [[ -n "$detail" ]]; then
        local in_catalog
        in_catalog="$(echo "$detail" | jq -r '.in_catalog // false' 2>/dev/null)"
        if [[ "$in_catalog" == "true" ]]; then
            return 0
        fi
    fi

    # Check (b): Radarr proxy — scan movieFile.path across all movies
    local catalog
    catalog="$(http_json GET "${PELI_BASE_URL}/api/pelicula/catalog?type=movie" --auth pelicula 2>/dev/null)" || true

    if [[ -n "$catalog" ]]; then
        local match
        match="$(echo "$catalog" | jq -r --arg p "$file_path" \
            '.movies[] | select(.hasFile == true and .movieFile.path == $p) | .id' \
            2>/dev/null | head -1)"
        if [[ -n "$match" && "$match" != "null" ]]; then
            return 0
        fi
    fi

    _peli_err "assert_arr_traceable: '$file_path' is NOT *arr-traceable (orphaned)"
    return 1
}

# ── assert_orphaned ───────────────────────────────────────────────────────────

# assert_orphaned FILE_PATH
#   Assert that FILE_PATH is orphaned: in Jellyfin but NOT in catalog.db and
#   NOT *arr-traceable via Radarr.
#   Returns 0 if orphaned (the expected failure state for bug-1 tests).
assert_orphaned() {
    local file_path="${1:-}"

    if [[ -z "$file_path" ]]; then
        _peli_err "assert_orphaned: FILE_PATH required"
        return 1
    fi

    # Check catalog.db
    local encoded_path
    encoded_path="$(peli_url_encode "$file_path")"

    local detail
    detail="$(http_json GET "${PELI_BASE_URL}/api/pelicula/catalog/detail?path=${encoded_path}" --auth pelicula 2>/dev/null)" || true

    if [[ -n "$detail" ]]; then
        local in_catalog
        in_catalog="$(echo "$detail" | jq -r '.in_catalog // false' 2>/dev/null)"
        if [[ "$in_catalog" == "true" ]]; then
            _peli_err "assert_orphaned: '$file_path' is IN catalog.db (expected orphaned)"
            return 1
        fi
    fi

    # Check Radarr *arr-traceability
    local catalog
    catalog="$(http_json GET "${PELI_BASE_URL}/api/pelicula/catalog?type=movie" --auth pelicula 2>/dev/null)" || true

    if [[ -n "$catalog" ]]; then
        local match
        match="$(echo "$catalog" | jq -r --arg p "$file_path" \
            '.movies[] | select(.hasFile == true and .movieFile.path == $p) | .id' \
            2>/dev/null | head -1)"
        if [[ -n "$match" && "$match" != "null" ]]; then
            _peli_err "assert_orphaned: '$file_path' IS *arr-traceable via Radarr (expected orphaned)"
            return 1
        fi
    fi

    return 0
}
