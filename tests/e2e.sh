#!/usr/bin/env bash
# End-to-end integration test for the Pelicula stack.
# Spins an isolated stack on port 7399, no VPN needed.
#
# Usage: bash tests/e2e.sh [--keep]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
COMPOSE_FILE="$SCRIPT_DIR/compose/docker-compose.yml"

# ── Colors ──────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

VERBOSE=0

pass() { [[ $VERBOSE -eq 1 ]] && echo -e "  ${GREEN}✓${NC} $1"; return 0; }
fail() { echo -e "  ${RED}✗${NC} $1"; }
info() { [[ $VERBOSE -eq 1 ]] && echo -e "${CYAN}→${NC} $1"; return 0; }
warn() { echo -e "${YELLOW}!${NC} $1"; }

# ── Platform Detection ──────────────────────────────

NEEDS_SUDO=""

detect_platform() {
    # Auto-detect whether docker needs sudo
    if ! docker info &>/dev/null; then
        if sudo docker info &>/dev/null; then
            NEEDS_SUDO="sudo"
        fi
    fi
}

detect_platform

# ── Helpers ─────────────────────────────────────────

docker_cmd() {
    $NEEDS_SUDO docker "$@"
}

seed_config() {
    local file="$1" content="$2"
    if [[ ! -f "$file" ]]; then
        echo "$content" > "$file"
    fi
}

setup_dirs() {
    local config_dir="$1" library_dir="$2" work_dir="$3"
    mkdir -p \
        "$config_dir/gluetun" \
        "$config_dir/qbittorrent" \
        "$config_dir/prowlarr" \
        "$config_dir/sonarr" \
        "$config_dir/radarr" \
        "$config_dir/jellyfin" \
        "$config_dir/bazarr" \
        "$config_dir/procula/jobs" \
        "$config_dir/procula/profiles" \
        "$config_dir/pelicula" \
        "$config_dir/certs" \
        "$library_dir/movies" \
        "$library_dir/tv" \
        "$work_dir/downloads" \
        "$work_dir/downloads/incomplete" \
        "$work_dir/downloads/radarr" \
        "$work_dir/downloads/tv-sonarr" \
        "$work_dir/processing"
}

# ── Parse Flags ─────────────────────────────────────

_ARGS=()
for _arg in "$@"; do
    case "$_arg" in
        -v|--verbose) VERBOSE=1 ;;
        *) _ARGS+=("$_arg") ;;
    esac
done
set -- ${_ARGS[@]+"${_ARGS[@]}"}

# ── End-to-End Test ─────────────────────────────────

cmd_test() {
    local keep=0 test_port=7399
    [[ "${1:-}" == "--keep" ]] && keep=1

    # Pre-flight: warn if port 6881 is already bound (production stack running)
    if lsof -i :6881 -sTCP:LISTEN -t >/dev/null 2>&1 || ss -tlnp 2>/dev/null | grep -q ':6881 '; then
        warn "Port 6881 appears to be in use — the production stack may be running."
        warn "Consider running ${BOLD}pelicula down${NC} first to avoid container name conflicts."
        echo ""
    fi

    local test_dir
    test_dir="$(mktemp -d)"
    local test_config_dir="$test_dir/config"
    local test_library_dir="$test_dir/library"
    local test_work_dir="$test_dir/work"
    # Write test config to the standard .env path so bind-mounts inside containers work.
    # Back up any existing .env and restore it on cleanup.
    local test_env="$SCRIPT_DIR/.env"
    local test_env_backup=""
    if [[ -f "$test_env" ]]; then
        test_env_backup="${test_env}.test-bak-$$"
        cp "$test_env" "$test_env_backup"
    fi
    local test_passes=0 test_failures=0

    # Local pass/fail wrappers that track counts
    t_pass() { pass "$1"; test_passes=$((test_passes + 1)); }
    t_fail() { fail "$1"; test_failures=$((test_failures + 1)); }

    # Compose wrapper: isolated project, test env, test overlay.
    # --profile vpn starts gluetun/qbittorrent/prowlarr, which the overlay
    # replaces with safe stubs (alpine for gluetun, real images with test names).
    test_compose() {
        $NEEDS_SUDO docker compose \
            --project-directory "$SCRIPT_DIR" \
            --env-file "${test_env:-$SCRIPT_DIR/.env}" \
            -f "$COMPOSE_FILE" \
            -f "$SCRIPT_DIR/compose/docker-compose.test.yml" \
            -p pelicula-test \
            --profile vpn \
            "$@"
    }

    cleanup_test() {
        # Always restore the original .env — the test containers have env vars baked in
        # from the initial `up -d` and are unaffected by the file on disk. Leaving the
        # test .env in place (dummy WireGuard key etc.) breaks `pelicula up` for prod.
        if [[ -n "${test_env_backup:-}" ]] && [[ -f "${test_env_backup:-}" ]]; then
            mv "$test_env_backup" "${test_env:-$SCRIPT_DIR/.env}"
        elif [[ -f "${test_env:-$SCRIPT_DIR/.env}" ]]; then
            rm -f "${test_env:-$SCRIPT_DIR/.env}"
        fi

        if [[ ${keep:-0} -eq 0 ]]; then
            info "Cleaning up test stack..."
            $NEEDS_SUDO docker compose \
                --project-directory "$SCRIPT_DIR" \
                --env-file "${test_env:-$SCRIPT_DIR/.env}" \
                -f "$COMPOSE_FILE" \
                -f "$SCRIPT_DIR/compose/docker-compose.test.yml" \
                -p pelicula-test \
                down -v --remove-orphans 2>/dev/null || true
            rm -rf "${test_dir:-}"
        else
            echo ""
            warn "Test stack left running (--keep is set)."
            warn "Original .env has been restored — prod stack is safe."
            warn "Clean up test stack: docker compose -p pelicula-test down -v"
            warn "Temp dirs: ${test_dir:-<unknown>}"
        fi
    }
    trap cleanup_test EXIT

    echo ""
    echo -e "${BOLD}pelicula end-to-end test${NC}"
    echo ""

    # ── Stage 0: Init ─────────────────────────────────

    local test_api_key
    test_api_key="$(LC_ALL=C tr -dc 'a-zA-Z0-9' < /dev/urandom | head -c 32 2>/dev/null \
        || openssl rand -base64 24 | tr -d '/+=')"

    local test_tz="UTC"
    if [[ -L /etc/localtime ]]; then
        test_tz="$(readlink /etc/localtime | sed 's|.*/zoneinfo/||')" || test_tz="UTC"
    elif [[ -f /etc/timezone ]]; then
        test_tz="$(cat /etc/timezone)" || test_tz="UTC"
    fi

    cat > "$test_env" <<EOF
CONFIG_DIR="${test_config_dir}"
LIBRARY_DIR="${test_library_dir}"
WORK_DIR="${test_work_dir}"
PUID="$(id -u)"
PGID="$(id -g)"
TZ="${test_tz}"
WIREGUARD_PRIVATE_KEY="dGVzdGtleXRlc3RrZXl0ZXN0a2V5dGVzdGtleTE="
SERVER_COUNTRIES="Netherlands"
PELICULA_PORT="${test_port}"
PELICULA_AUTH="off"
JELLYFIN_ADMIN_USER="admin"
JELLYFIN_PASSWORD="test-jellyfin-pw"
JELLYFIN_PUBLISHED_URL="http://127.0.0.1:${test_port}/jellyfin"
PROCULA_API_KEY="${test_api_key}"
TRANSCODING_ENABLED=false
NOTIFICATIONS_ENABLED=false
NOTIFICATIONS_MODE=internal
EOF
    chmod 600 "$test_env"

    setup_dirs "$test_config_dir" "$test_library_dir" "$test_work_dir"

    # Seed *arr + Jellyfin configs (same as cmd_up)
    seed_config "$test_config_dir/sonarr/config.xml" \
        '<Config><UrlBase>/sonarr</UrlBase><AuthenticationMethod>External</AuthenticationMethod><AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired></Config>'
    seed_config "$test_config_dir/radarr/config.xml" \
        '<Config><UrlBase>/radarr</UrlBase><AuthenticationMethod>External</AuthenticationMethod><AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired></Config>'
    seed_config "$test_config_dir/prowlarr/config.xml" \
        '<Config><UrlBase>/prowlarr</UrlBase><AuthenticationMethod>External</AuthenticationMethod><AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired></Config>'
    seed_config "$test_config_dir/jellyfin/network.xml" \
        "<?xml version=\"1.0\" encoding=\"utf-8\"?><NetworkConfiguration xmlns:xsi=\"http://www.w3.org/2001/XMLSchema-instance\" xmlns:xsd=\"http://www.w3.org/2001/XMLSchema\"><BaseUrl>/jellyfin</BaseUrl><PublishedServerUrl>http://127.0.0.1:${test_port}/jellyfin</PublishedServerUrl></NetworkConfiguration>"
    mkdir -p "$test_config_dir/bazarr/config"
    seed_config "$test_config_dir/bazarr/config/config.ini" \
'[general]
base_url=/bazarr
'
    mkdir -p "$test_config_dir/qbittorrent/qBittorrent"
    seed_config "$test_config_dir/qbittorrent/qBittorrent/qBittorrent.conf" \
'[Preferences]
WebUI\AuthSubnetWhitelistEnabled=true
WebUI\AuthSubnetWhitelist=172.16.0.0/12
WebUI\LocalHostAuth=false
WebUI\CSRFProtection=false

[BitTorrent]
Session\DefaultSavePath=/downloads/'

    # nginx bind-mounts this file; without it Docker creates a directory and nginx fails to start
    mkdir -p "$test_config_dir/nginx"
    echo "# Remote access disabled" > "$test_config_dir/nginx/remote.conf"

    t_pass "Environment initialized"

    # ── Stage 1: Start Stack ──────────────────────────

    info "Building and starting test stack (this may take a minute)..."
    if ! test_compose up -d --build 2>&1; then
        t_fail "Stack failed to start"
        echo ""
        warn "Check Docker logs for details. Run with --keep to investigate."
        exit 1
    fi

    info "Waiting for middleware to be ready..."
    local wait=0
    while [[ $wait -lt 60 ]]; do
        if curl -sf --max-time 3 "http://localhost:${test_port}/api/pelicula/health" >/dev/null 2>&1; then
            break
        fi
        sleep 2
        wait=$((wait + 1))
    done

    if [[ $wait -ge 60 ]]; then
        t_fail "Stack did not become healthy within 120s"
        echo ""
        warn "Container logs:"
        test_compose logs --tail 30 pelicula-api 2>/dev/null || true
        exit 1
    fi

    t_pass "Stack started"

    # ── Stage 2: Wait for Auto-Wire ───────────────────

    info "Waiting for auto-wire to complete (Jellyfin wizard + library setup)..."
    wait=0
    local wired=false
    while [[ $wait -lt 60 ]]; do
        local status_resp
        status_resp="$(curl -sf --max-time 5 "http://localhost:${test_port}/api/pelicula/status" 2>/dev/null || echo "")"
        if echo "$status_resp" | grep -q '"wired":true'; then
            wired=true
            break
        fi
        sleep 3
        wait=$((wait + 1))
    done

    if [[ "$wired" != "true" ]]; then
        t_fail "Auto-wire did not complete within 180s"
        echo ""
        warn "Last status response:"
        echo "$status_resp" | head -c 500
        echo ""
        warn "Middleware logs:"
        test_compose logs --tail 40 pelicula-api 2>/dev/null || true
        exit 1
    fi

    t_pass "Auto-wire complete"

    # ── Stage 3: Configure Procula + Generate Media ───

    # Disable validation (tiny test file fails the 50MB sample floor).
    # Enable transcoding with a test profile that downscales to 180p.
    local settings_resp
    settings_resp="$(curl -sf --max-time 5 \
        -X POST "http://localhost:${test_port}/api/procula/settings" \
        -H "Content-Type: application/json" \
        -H "X-API-Key: ${test_api_key}" \
        -d '{"validation_enabled":false,"transcoding_enabled":true,"catalog_enabled":true,"notification_mode":"internal","storage_warning_pct":85,"storage_critical_pct":95}' \
        2>/dev/null || echo "error")"
    if [[ "$settings_resp" == "error" ]]; then
        warn "Could not configure Procula settings (non-fatal, defaults will apply)"
    fi

    # Write a transcoding profile that matches h264 and downscales to 180p.
    # The test video is 320x240 h264, so this profile will match and transcode.
    local profiles_dir="$test_config_dir/procula/profiles"
    mkdir -p "$profiles_dir"
    cat > "$profiles_dir/test-downscale.json" <<'EOPROFILE'
{
  "name": "test-downscale",
  "enabled": true,
  "description": "E2E test profile — downscale h264 to 180p",
  "conditions": {
    "codecs_include": ["h264"]
  },
  "output": {
    "video_codec": "libx264",
    "video_preset": "ultrafast",
    "video_crf": 28,
    "max_height": 180,
    "audio_codec": "aac",
    "audio_channels": 2,
    "suffix": ".test"
  }
}
EOPROFILE

    local movie_dir="$test_library_dir/movies/Test Movie (2024)"
    local movie_file="$movie_dir/Test.Movie.2024.mkv"
    mkdir -p "$movie_dir"

    info "Generating test media file..."
    local ffmpeg_ok=false
    if command -v ffmpeg &>/dev/null; then
        if ffmpeg -y \
            -f lavfi -i "color=c=blue:s=320x240:d=10:r=24" \
            -f lavfi -i "sine=frequency=1000:duration=10:sample_rate=44100" \
            -c:v libx264 -preset ultrafast -crf 28 \
            -c:a aac -b:a 64k \
            "$movie_file" 2>/dev/null; then
            ffmpeg_ok=true
        fi
    fi

    if [[ "$ffmpeg_ok" != "true" ]]; then
        # Fall back: run FFmpeg inside the procula container (which has it)
        if $NEEDS_SUDO docker exec pelicula-test-procula ffmpeg -y \
            -f lavfi -i "color=c=blue:s=320x240:d=10:r=24" \
            -f lavfi -i "sine=frequency=1000:duration=10:sample_rate=44100" \
            -c:v libx264 -preset ultrafast -crf 28 \
            -c:a aac -b:a 64k \
            "/movies/Test Movie (2024)/Test.Movie.2024.mkv" 2>/dev/null; then
            ffmpeg_ok=true
        fi
    fi

    if [[ "$ffmpeg_ok" != "true" ]] || [[ ! -f "$movie_file" ]]; then
        t_fail "Test media generation failed (FFmpeg not available on host or in container)"
        exit 1
    fi

    local file_size
    if [[ "$(uname)" == "Darwin" ]]; then
        file_size="$(stat -f%z "$movie_file" 2>/dev/null || echo 0)"
    else
        file_size="$(stat -c%s "$movie_file" 2>/dev/null || echo 0)"
    fi
    t_pass "Test media generated ($(numfmt --to=iec "$file_size" 2>/dev/null || echo "${file_size} B"))"

    # ── Stage 4: Trigger Import Webhook ──────────────

    info "Triggering import webhook..."
    local webhook_resp
    # Send from inside the Docker network so nginx's RFC1918 allow-list passes.
    # (On macOS Docker Desktop, host→published-port traffic is not 127.0.0.1 to nginx.)
    webhook_resp="$($NEEDS_SUDO docker exec pelicula-test-api \
        wget -qO- --timeout=10 \
        --header="Content-Type: application/json" \
        --post-data="{
            \"eventType\": \"Download\",
            \"movie\": {
                \"id\": 1,
                \"title\": \"Test Movie\",
                \"year\": 2024,
                \"folderPath\": \"/movies/Test Movie (2024)\"
            },
            \"movieFile\": {
                \"path\": \"/movies/Test Movie (2024)/Test.Movie.2024.mkv\",
                \"relativePath\": \"Test.Movie.2024.mkv\",
                \"size\": ${file_size},
                \"mediaInfo\": { \"runTimeSeconds\": 10 }
            },
            \"downloadId\": \"test-e2e-$(date +%s)\"
        }" \
        "http://localhost:8181/api/pelicula/hooks/import" 2>/dev/null || echo "")"

    if echo "$webhook_resp" | grep -q '"status":"queued"'; then
        t_pass "Import webhook accepted"
    else
        t_fail "Import webhook rejected or unreachable"
        echo ""
        warn "Response: ${webhook_resp:-<no response>}"
        exit 1
    fi

    # ── Stage 5: Wait for Processing ─────────────────

    info "Waiting for Procula to finish processing..."
    wait=0
    local job_state="" job_json=""
    while [[ $wait -lt 60 ]]; do
        local jobs_resp
        jobs_resp="$(curl -sf --max-time 5 "http://localhost:${test_port}/api/procula/jobs" 2>/dev/null || echo "[]")"
        job_state="$(echo "$jobs_resp" | python3 -c "
import json, sys
try:
    jobs = json.loads(sys.stdin.read())
    for j in jobs:
        if 'Test Movie' in (j.get('source') or {}).get('title', ''):
            print(j.get('state', ''))
            break
except Exception:
    pass
" 2>/dev/null || echo "")"
        job_json="$jobs_resp"
        if [[ "$job_state" == "completed" ]] || [[ "$job_state" == "failed" ]] || [[ "$job_state" == "cancelled" ]]; then
            break
        fi
        sleep 2
        wait=$((wait + 1))
    done

    if [[ "$job_state" == "completed" ]]; then
        t_pass "Processing completed"
    elif [[ "$job_state" == "failed" ]] || [[ "$job_state" == "cancelled" ]]; then
        t_fail "Processing ${job_state}"
        echo ""
        warn "Job details:"
        echo "$job_json" | python3 -c "
import json, sys
try:
    jobs = json.loads(sys.stdin.read())
    for j in jobs:
        if 'Test Movie' in (j.get('source') or {}).get('title', ''):
            print(json.dumps(j, indent=2))
            break
except Exception as e:
    print(f'(parse error: {e})')
" 2>/dev/null || echo "$job_json"
        echo ""
        warn "Procula logs:"
        test_compose logs --tail 40 procula 2>/dev/null || true
        exit 1
    else
        t_fail "Processing did not complete within 120s (state: ${job_state:-unknown})"
        echo ""
        warn "Procula logs:"
        test_compose logs --tail 40 procula 2>/dev/null || true
        exit 1
    fi

    # ── Sidecar verification ──────────────────────────
    # The test-downscale profile has suffix ".test", so Procula should have
    # written a sidecar alongside the original file as a Jellyfin alt version.
    local sidecar_file="$movie_dir/Test.Movie.2024.test.mkv"
    if [[ -f "$sidecar_file" ]]; then
        t_pass "Transcoded sidecar created (Jellyfin alternate version)"
    else
        # Non-fatal: sidecar may be inside the container volume only
        local container_sidecar="/movies/Test Movie (2024)/Test.Movie.2024.test.mkv"
        if $NEEDS_SUDO docker exec pelicula-test-procula test -f "$container_sidecar" 2>/dev/null; then
            t_pass "Transcoded sidecar created (inside container volume)"
        else
            warn "Sidecar not found at ${sidecar_file} — transcoding may have been skipped (passthrough or profile mismatch)"
        fi
    fi
    # Original file must still exist (sidecar mode never deletes the source)
    if [[ -f "$movie_file" ]]; then
        t_pass "Original file preserved after transcoding"
    else
        t_fail "Original file was deleted — sidecar mode must not remove the source"
        exit 1
    fi

    # ── Stage 6: Verify in Jellyfin ──────────────────

    info "Verifying movie appears in Jellyfin library..."

    # Authenticate with Jellyfin using the password set in the test env
    local jf_auth_resp jf_token=""
    jf_auth_resp="$(curl -sf --max-time 10 \
        -X POST "http://localhost:${test_port}/jellyfin/Users/AuthenticateByName" \
        -H "Content-Type: application/json" \
        -H 'X-Emby-Authorization: MediaBrowser Client="PeliculaTest", Device="e2e", DeviceId="pelicula-e2e-test", Version="1.0"' \
        -d '{"Username":"admin","Pw":"test-jellyfin-pw"}' 2>/dev/null || echo "")"
    jf_token="$(echo "$jf_auth_resp" | python3 -c "
import json, sys
try:
    print(json.loads(sys.stdin.read()).get('AccessToken',''))
except Exception:
    pass
" 2>/dev/null || echo "")"

    if [[ -z "$jf_token" ]]; then
        t_fail "Jellyfin authentication failed"
        echo ""
        warn "Auth response: ${jf_auth_resp:-<no response>}"
        warn "Jellyfin logs:"
        test_compose logs --tail 30 jellyfin 2>/dev/null || true
        exit 1
    fi

    # Jellyfin library scan is async — retry a few times
    local found=false
    local item_count=0
    for _ in 1 2 3 4 5; do
        sleep 5
        local search_resp
        search_resp="$(curl -sf --max-time 10 \
            "http://localhost:${test_port}/jellyfin/Items?SearchTerm=Test+Movie&IncludeItemTypes=Movie&Recursive=true" \
            -H "X-Emby-Authorization: MediaBrowser Client=\"PeliculaTest\", Device=\"e2e\", DeviceId=\"pelicula-e2e-test\", Version=\"1.0\", Token=\"${jf_token}\"" \
            2>/dev/null || echo "")"
        item_count="$(echo "$search_resp" | python3 -c "
import json, sys
try:
    print(json.loads(sys.stdin.read()).get('TotalRecordCount', 0))
except Exception:
    print(0)
" 2>/dev/null || echo "0")"
        if [[ "$item_count" -gt 0 ]]; then
            found=true
            break
        fi
    done

    if [[ "$found" == "true" ]]; then
        t_pass "Movie found in Jellyfin library"
    else
        t_fail "Movie not found in Jellyfin library after 25s"
        echo ""
        warn "Library scan may still be in progress. Jellyfin logs:"
        test_compose logs --tail 30 jellyfin 2>/dev/null || true
    fi

    # ── Stage 8: Auth & Nginx Routing ────────────────

    info "Enabling jellyfin auth and testing routing..."

    # Update .env to enable jellyfin auth and recreate pelicula-api to pick up new env vars.
    # "restart" does not re-read env changes; "up -d" recreates the container when config changes.
    sed -i.bak 's/PELICULA_AUTH="off"/PELICULA_AUTH="jellyfin"/' "$test_env"
    test_compose up -d pelicula-api >/dev/null 2>&1

    # Wait for pelicula-api to come back up
    local auth_ready=false
    for i in $(seq 1 30); do
        if curl -sf --max-time 2 "http://localhost:${test_port}/api/pelicula/health" >/dev/null 2>&1; then
            auth_ready=true
            break
        fi
        sleep 1
    done
    if [[ "$auth_ready" != "true" ]]; then
        t_fail "pelicula-api did not restart after auth change"
    fi

    # Helper: check HTTP status code
    assert_http() {
        local expected="$1" url="$2" cookie_file="${3:-}"
        local curl_opts="-s -o /dev/null -w %{http_code} --max-time 5"
        local actual
        if [[ -n "$cookie_file" ]]; then
            actual="$(curl $curl_opts -b "$cookie_file" "$url" 2>/dev/null)"
        else
            actual="$(curl $curl_opts "$url" 2>/dev/null)"
        fi
        if [[ "$actual" == "$expected" ]]; then
            t_pass "HTTP $expected $url"
        else
            t_fail "HTTP $actual (expected $expected) $url"
        fi
    }

    # Unprotected routes should still return 200
    assert_http 200 "http://localhost:${test_port}/"
    assert_http 200 "http://localhost:${test_port}/api/health/"

    # Protected routes without a session should return 302 (redirect to /?login=1)
    assert_http 302 "http://localhost:${test_port}/settings"
    assert_http 302 "http://localhost:${test_port}/import"
    assert_http 302 "http://localhost:${test_port}/qbt/"
    assert_http 302 "http://localhost:${test_port}/prowlarr"
    assert_http 302 "http://localhost:${test_port}/sonarr"
    assert_http 302 "http://localhost:${test_port}/radarr"

    # Login with wrong password should fail (verified against Jellyfin)
    local login_fail
    login_fail="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
        -X POST "http://localhost:${test_port}/api/pelicula/auth/login" \
        -H "Content-Type: application/json" \
        -d '{"username":"admin","password":"wrong"}' 2>/dev/null)"
    if [[ "$login_fail" == "401" ]]; then
        t_pass "Login rejected with wrong password"
    else
        t_fail "Wrong password returned $login_fail (expected 401)"
    fi

    # Login with Jellyfin credentials should succeed and set cookie
    local cookie_file="$test_dir/cookies.txt"
    local login_resp
    login_resp="$(curl -s -w '\n%{http_code}' --max-time 5 \
        -c "$cookie_file" \
        -X POST "http://localhost:${test_port}/api/pelicula/auth/login" \
        -H "Content-Type: application/json" \
        -d '{"username":"admin","password":"test-jellyfin-pw"}' 2>/dev/null)"
    local login_code="${login_resp##*$'\n'}"
    if [[ "$login_code" == "200" ]]; then
        t_pass "Login succeeded with Jellyfin credentials"
    else
        t_fail "Login returned $login_code (expected 200)"
    fi

    # Protected routes should now succeed with session cookie
    assert_http 200 "http://localhost:${test_port}/settings" "$cookie_file"
    assert_http 200 "http://localhost:${test_port}/api/pelicula/status" "$cookie_file"

    # Auth check should return valid:true
    local check_resp
    check_resp="$(curl -sf --max-time 5 -b "$cookie_file" \
        "http://localhost:${test_port}/api/pelicula/auth/check" 2>/dev/null || echo "{}")"
    if echo "$check_resp" | grep -q '"valid":true'; then
        t_pass "Auth check returns valid:true with session"
    else
        t_fail "Auth check did not return valid:true"
    fi

    # Logout should clear the session
    curl -sf --max-time 5 -b "$cookie_file" -c "$cookie_file" \
        -X POST "http://localhost:${test_port}/api/pelicula/auth/logout" >/dev/null 2>&1

    local check_after
    check_after="$(curl -sf --max-time 5 -b "$cookie_file" \
        "http://localhost:${test_port}/api/pelicula/auth/check" 2>/dev/null || echo "{}")"
    if echo "$check_after" | grep -q '"valid":false'; then
        t_pass "Session invalidated after logout"
    else
        t_fail "Session still valid after logout"
    fi

    # Verify no-cache headers on dashboard
    local cache_header
    cache_header="$(curl -sI --max-time 5 "http://localhost:${test_port}/" 2>/dev/null | grep -i 'cache-control' || echo "")"
    if echo "$cache_header" | grep -qi 'no-store'; then
        t_pass "Dashboard has Cache-Control: no-store"
    else
        t_fail "Dashboard missing no-store cache header"
    fi

    # ── Stage 9: Playwright UI Tests ─────────────────

    if command -v npx &>/dev/null && npx playwright --version &>/dev/null 2>&1; then
        info "Seeding Playwright test fixtures..."

        # Fixture 1: valid H.264 file for import-play spec
        local pw_movie_dir="$test_library_dir/movies/Valid H264 Test (2024)"
        local pw_movie_file="$pw_movie_dir/valid-h264-10s.mkv"
        mkdir -p "$pw_movie_dir"

        local pw_ffmpeg_ok=false
        if command -v ffmpeg &>/dev/null; then
            if ffmpeg -y \
                -f lavfi -i "color=c=blue:s=320x240:d=10:r=24" \
                -f lavfi -i "sine=frequency=440:duration=10:sample_rate=44100" \
                -c:v libx264 -preset ultrafast -crf 28 \
                -c:a aac -b:a 64k \
                "$pw_movie_file" 2>/dev/null; then
                pw_ffmpeg_ok=true
            fi
        fi
        if [[ "$pw_ffmpeg_ok" != "true" ]]; then
            if $NEEDS_SUDO docker exec pelicula-test-procula ffmpeg -y \
                -f lavfi -i "color=c=blue:s=320x240:d=10:r=24" \
                -f lavfi -i "sine=frequency=440:duration=10:sample_rate=44100" \
                -c:v libx264 -preset ultrafast -crf 28 \
                -c:a aac -b:a 64k \
                "/movies/Valid H264 Test (2024)/valid-h264-10s.mkv" 2>/dev/null; then
                pw_ffmpeg_ok=true
            fi
        fi

        # Fixture 2: Night of the Living Dead for subtitle-acquisition spec
        local pw_notld_file="$test_work_dir/downloads/Night.of.the.Living.Dead.1968.mkv"
        if [[ "$pw_ffmpeg_ok" == "true" ]]; then
            if command -v ffmpeg &>/dev/null; then
                ffmpeg -y \
                    -f lavfi -i "color=c=black:s=320x240:d=15:r=24" \
                    -f lavfi -i "sine=frequency=220:duration=15:sample_rate=44100" \
                    -c:v libx264 -preset ultrafast -crf 28 \
                    -c:a aac -b:a 64k \
                    -metadata title="Night of the Living Dead" \
                    -metadata year="1968" \
                    "$pw_notld_file" 2>/dev/null || pw_ffmpeg_ok=false
            else
                $NEEDS_SUDO docker exec pelicula-test-procula ffmpeg -y \
                    -f lavfi -i "color=c=black:s=320x240:d=15:r=24" \
                    -f lavfi -i "sine=frequency=220:duration=15:sample_rate=44100" \
                    -c:v libx264 -preset ultrafast -crf 28 \
                    -c:a aac -b:a 64k \
                    -metadata title="Night of the Living Dead" \
                    -metadata year="1968" \
                    "/downloads/Night.of.the.Living.Dead.1968.mkv" 2>/dev/null || pw_ffmpeg_ok=false
            fi
        fi

        if [[ "$pw_ffmpeg_ok" != "true" ]]; then
            warn "Playwright fixture generation failed — skipping UI tests"
        else
            t_pass "Playwright fixtures seeded"

            # Pre-fire Night of the Living Dead import webhook from inside Docker
            # (nginx IP-restricts /api/pelicula/hooks/import to internal networks;
            # Playwright runs on the host and can't call it directly through nginx)
            info "Pre-firing Night of the Living Dead import webhook..."
            $NEEDS_SUDO docker exec pelicula-test-api wget -qO- \
                --post-data='{"eventType":"Download","movie":{"id":1968,"title":"Night of the Living Dead","year":1968,"folderPath":"/downloads"},"movieFile":{"path":"/downloads/Night.of.the.Living.Dead.1968.mkv","relativePath":"Night.of.the.Living.Dead.1968.mkv","size":500000,"mediaInfo":{"runTimeSeconds":5760}},"downloadId":"playwright-notld-test"}' \
                --header='Content-Type: application/json' \
                'http://localhost:8181/api/pelicula/hooks/import' 2>/dev/null || true

            info "Running Playwright UI tests..."

            local pw_exit=0
            (
                cd "$SCRIPT_DIR/tests/playwright"
                PLAYWRIGHT_BASE_URL="http://localhost:${test_port}" \
                    npx playwright test \
                        --config playwright.config.js \
                        --reporter list
            ) 2>&1 || pw_exit=$?

            if [[ $pw_exit -eq 0 ]]; then
                t_pass "Playwright UI tests passed"
            else
                t_fail "Playwright UI tests failed (exit code ${pw_exit})"
                warn "Re-run with: (cd tests/playwright && npm run test:ui:headed)"
                warn "Or: (cd tests/playwright && npx playwright show-report report)"
            fi
        fi
    else
        warn "Node/Playwright not found — skipping UI tests (run: cd tests/playwright && npm install && npx playwright install chromium)"
    fi

    # ── Summary ───────────────────────────────────────

    echo ""
    local total=$((test_passes + test_failures))
    if [[ $test_failures -eq 0 ]]; then
        echo -e "  ${GREEN}${BOLD}ALL TESTS PASSED${NC} (${test_passes}/${total})"
        echo ""
        # Disable the trap and run cleanup directly while locals are still in scope.
        # (On the failure path we use exit 1, which fires the trap inside this function.)
        trap - EXIT
        cleanup_test
    else
        echo -e "  ${RED}${BOLD}${test_failures} FAILED${NC}, ${test_passes} passed (${test_passes}/${total})"
        echo ""
        echo "  Debug commands:"
        echo "    bash tests/e2e.sh --keep        re-run, keep containers up"
        echo "    docker compose -p pelicula-test logs procula"
        echo "    docker compose -p pelicula-test logs pelicula-api"
        echo "    docker compose -p pelicula-test logs jellyfin"
        echo ""
        exit 1
    fi
}

cmd_test "${1:-}"
