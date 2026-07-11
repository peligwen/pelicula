#!/usr/bin/env bash
# End-to-end integration test for the Pelicula stack.
# Spins an isolated stack on port 7399, no VPN needed.
#
# Usage: bash tests/e2e.sh [--keep]

# TODO(phase-4): consider sourcing tests/lib.sh once name collisions are resolved

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

# check_disk_space DIR
#   procula pauses its whole processing pipeline once disk usage crosses its
#   storage-critical threshold (~90-95%, see procula/settings.go's
#   StorageCriticalPct). A run that starts on an already-near-full disk
#   doesn't fail here — it hangs ~8 minutes into Stage 5 waiting for a job
#   that will never complete, and reports a cryptic timeout instead of the
#   real cause (this exact failure mode is in the campaign log). Catch it
#   before doing any work. `df -Pk` forces POSIX-portable, 1024-byte-block
#   output so the column layout (and units) is identical on macOS/BSD and
#   Linux/GNU.
PELI_E2E_DISK_CRITICAL_PCT=90

check_disk_space() {
    local dir="$1"
    local df_line
    df_line="$(df -Pk "$dir" 2>/dev/null | tail -1)"
    if [[ -z "$df_line" ]]; then
        warn "Could not determine disk usage for $dir — skipping pre-flight disk check"
        return 0
    fi

    local used_pct avail_kb
    used_pct="$(echo "$df_line" | awk '{print $5}' | tr -d '%')"
    avail_kb="$(echo "$df_line" | awk '{print $4}')"
    if ! [[ "$used_pct" =~ ^[0-9]+$ ]]; then
        warn "Could not parse disk usage for $dir — skipping pre-flight disk check"
        return 0
    fi

    if [[ "$used_pct" -ge "$PELI_E2E_DISK_CRITICAL_PCT" ]]; then
        local avail_human
        avail_human="$(numfmt --to=iec --from-unit=1024 "$avail_kb" 2>/dev/null || echo "${avail_kb} KB")"
        fail "Disk is ${used_pct}% full on the filesystem hosting $dir (>= ${PELI_E2E_DISK_CRITICAL_PCT}%)"
        warn "Only ${avail_human} free. procula's storage-critical threshold will pause the"
        warn "processing pipeline mid-run — this run would time out ~8 minutes into Stage 5"
        warn "with a cryptic failure instead of failing fast here. Free up space and re-run."
        return 1
    fi

    info "Disk headroom OK (${used_pct}% used on the test filesystem)"
    return 0
}

# build_pelicula_cli builds bin/pelicula exactly like the ./pelicula wrapper
# does (same ldflags, same output path, same Docker-based fallback for
# Go-less hosts) so e2e drives a binary that's provably built from this
# checkout. Unlike the wrapper, there is no staleness skip — e2e always
# rebuilds, trading a few seconds for the guarantee that the binary matches
# HEAD exactly (the wrapper's mtime-based skip is an interactive-CLI speed
# optimization, not something a test harness should trust).
# The binary must live under the repo root ($SCRIPT_DIR/bin/pelicula, never
# under $test_dir): getScriptDir() walks up from the binary's own location
# to find the repo, so a binary built elsewhere can't find compose/, etc.
build_pelicula_cli() {
    local bin="$SCRIPT_DIR/bin/pelicula"
    local git_version
    git_version="$(git -C "$SCRIPT_DIR" describe --tags --always --dirty 2>/dev/null || echo dev)"

    if [[ ! -d "$SCRIPT_DIR/cmd/pelicula" ]]; then
        fail "cmd/pelicula/ not found — are you running from the pelicula repo?"
        return 1
    fi

    mkdir -p "$SCRIPT_DIR/bin"
    if command -v go &>/dev/null; then
        if ! ( cd "$SCRIPT_DIR/cmd/pelicula" && go build -ldflags "-X main.version=$git_version" -o "$bin" . ); then
            fail "go build failed"
            return 1
        fi
    else
        # Synology's Docker Compose fork fails when HOME's parent dir is a symlink
        # (/var/services/homes -> /volume1/@fake_home_link) — same workaround the
        # wrapper uses.
        local docker_home="$HOME"
        if [[ -L "$(dirname "$HOME")" ]]; then
            docker_home="$SCRIPT_DIR"
        fi
        if ! HOME="$docker_home" $NEEDS_SUDO docker run --rm \
                -v "$SCRIPT_DIR:/src" \
                -w /src/cmd/pelicula \
                golang:1.23-alpine \
                go build -ldflags "-X main.version=$git_version" -o /src/bin/pelicula .; then
            fail "Docker-based Go build failed"
            return 1
        fi
    fi

    if [[ ! -x "$bin" ]]; then
        fail "binary not found at $bin after build"
        return 1
    fi
    return 0
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

    # Pre-flight: force-remove any pelicula-test-* survivors from an earlier
    # run whose teardown never fired (kill -9, SIGTERM mid-hook, trap bug).
    # Adopting leftovers instead poisons the fresh run in a subtle way: `up`
    # only recreates containers whose config changed, so survivors keep bind
    # mounts into the DEAD run's (deleted) temp dir and — worse — a surviving
    # nginx resolves upstream container names at ITS startup, so it proxies
    # the fresh run's requests at the old, recycled container IPs and every
    # API call times out. Scoped strictly to the pelicula-test- name prefix;
    # never touches production.
    local survivors
    survivors="$($NEEDS_SUDO docker ps -aq --filter 'name=^pelicula-test-' 2>/dev/null || true)"
    if [[ -n "$survivors" ]]; then
        warn "Found leftover pelicula-test containers from a previous run — removing them first."
        echo "$survivors" | xargs $NEEDS_SUDO docker rm -f >/dev/null 2>&1 || true
        $NEEDS_SUDO docker network rm pelicula-test_default >/dev/null 2>&1 || true
    fi

    local test_dir
    test_dir="$(mktemp -d)"
    local test_config_dir="$test_dir/config"
    local test_library_dir="$test_dir/library"
    local test_work_dir="$test_dir/work"
    # The isolated .env lives entirely under $test_dir — the repo-root .env
    # (production config) is never read, written, backed up, or swapped by
    # e2e. bin/pelicula is pointed at this file via PELICULA_ENV_FILE.
    local test_env="$test_dir/.env"
    local PELICULA_BIN="$SCRIPT_DIR/bin/pelicula"
    local test_passes=0 test_failures=0

    # Local pass/fail wrappers that track counts
    t_pass() { pass "$1"; test_passes=$((test_passes + 1)); }
    t_fail() { fail "$1"; test_failures=$((test_failures + 1)); }

    # Compose wrapper: isolated project, test env, test overlay. Used for
    # ancillary ops (image build, nginx -t, log dumps, docker exec) that
    # don't go through bin/pelicula — bring-up/teardown themselves are
    # driven by the real CLI (see Stage 1 and cleanup_test below).
    # --profile vpn starts gluetun/qbittorrent/prowlarr, which the overlay
    # replaces with safe stubs (alpine for gluetun, real images with test names).
    test_compose() {
        # File list mirrors the CLI's buildArgs assembly: base, then the
        # library-source overlay (the base mounts no /media itself; the test
        # env is bind-mount mode, so local-library), then the test overlay so
        # it wins merges.
        $NEEDS_SUDO docker compose \
            --project-directory "$SCRIPT_DIR" \
            --env-file "$test_env" \
            -f "$COMPOSE_FILE" \
            -f "$SCRIPT_DIR/compose/docker-compose.local-library.yml" \
            -f "$SCRIPT_DIR/compose/docker-compose.test.yml" \
            -p pelicula-test \
            --profile vpn \
            "$@"
    }

    # pelicula_login logs in to the pelicula-api and stores the session cookie.
    # Usage: pelicula_login <base_url> [user] [pass] [cookie_jar]
    pelicula_login() {
        local base="$1" user="${2:-admin}" pass="${3:-test-jellyfin-pw}" jar="${4:-/tmp/pelicula-e2e-cookies.txt}"
        curl -sf --max-time 5 -c "$jar" -b "$jar" \
            -X POST "$base/api/pelicula/auth/login" \
            -H 'Content-Type: application/json' \
            -d "{\"username\":\"$user\",\"password\":\"$pass\"}" >/dev/null
    }

    # fire_import_webhook posts a Radarr-style Download webhook to pelicula-api
    # and echoes the response body. Sent from inside the container so nginx's
    # RFC1918 allow-list on /api/pelicula/hooks/import passes.
    # Usage: fire_import_webhook <movie_id> <title> <year> <filename> <size> <runtime_secs> <download_id>
    fire_import_webhook() {
        local id="$1" title="$2" year="$3" file="$4" size="$5" runtime="$6" dl_id="$7"
        local folder="/media/movies/${title} (${year})"
        $NEEDS_SUDO docker exec pelicula-test-pelicula-api-1 \
            wget -qO- --timeout=10 \
            --header='Content-Type: application/json' \
            --header="X-Webhook-Secret: ${test_webhook_secret}" \
            --post-data="{\"eventType\":\"Download\",\"movie\":{\"id\":${id},\"title\":\"${title}\",\"year\":${year},\"folderPath\":\"${folder}\"},\"movieFile\":{\"path\":\"${folder}/${file}\",\"relativePath\":\"${file}\",\"size\":${size},\"mediaInfo\":{\"runTimeSeconds\":${runtime}}},\"downloadId\":\"${dl_id}\"}" \
            'http://localhost:8181/api/pelicula/hooks/import' 2>/dev/null
    }

    # post_procula_settings posts the standard test settings to procula and
    # echoes the response body. POSTs directly to procula (port 8282) inside
    # its container to bypass nginx auth_request, which gates /api/procula/
    # with the session cookie. Only the validation/transcoding toggles vary
    # between call sites.
    # Usage: post_procula_settings <validation_enabled> <transcoding_enabled>
    post_procula_settings() {
        local validation="$1" transcoding="$2"
        $NEEDS_SUDO docker exec pelicula-test-procula-1 wget -qO- \
            --header='Content-Type: application/json' \
            --header="X-API-Key: ${test_api_key}" \
            --post-data="{\"validation_enabled\":${validation},\"transcoding_enabled\":${transcoding},\"catalog_enabled\":true,\"notification_mode\":\"internal\",\"storage_warning_pct\":85,\"storage_critical_pct\":95,\"dual_sub_enabled\":true,\"dual_sub_pairs\":[\"en-es\"],\"sub_acquire_timeout_min\":1}" \
            'http://localhost:8282/api/procula/settings' 2>/dev/null
    }

    # cleanup_test is registered as an EXIT trap and also called explicitly
    # at the end of a green run (see Summary), so it must be idempotent
    # (safe to run twice) and must never let its own failures set the exit
    # code of an otherwise-green run — every step below is best-effort.
    cleanup_test() {
        # PELICULA_BIN is a local of run_test, and this trap can fire OUTSIDE
        # that scope (a stage's `return 1` unwinds run_test, then the script
        # exits from the top level). Under set -u a bare $PELICULA_BIN then
        # kills the trap itself and the whole teardown silently never runs —
        # this leaked a live test stack on 2026-07-11. Re-derive the
        # deterministic path up front; every use below goes through $peli_bin.
        local peli_bin="${PELICULA_BIN:-$SCRIPT_DIR/bin/pelicula}"
        if [[ ${keep:-0} -eq 0 ]]; then
            info "Cleaning up test stack..."
            # Same PELICULA_ENV_FILE/PELICULA_COMPOSE_OVERLAY exported for
            # bring-up. cmdDown activates the vpn + apprise profiles
            # unconditionally (not just whatever the current env implies),
            # so profile-gated services (gluetun/qbittorrent/prowlarr) are
            # always torn down — the same profile-parity bug class the
            # CLI's restart-acquire hit (CIT-7). down is safe to call even
            # if bring-up never got as far as starting containers.
            #
            # Guard on PELICULA_ENV_FILE actually being set: without it,
            # bin/pelicula falls back to the default "pelicula" project
            # name — production's — which must never be touched by e2e.
            # If we get here before the export (e.g. mktemp itself failed),
            # nothing was ever started, so there's nothing to tear down.
            #
            if [[ -n "${PELICULA_ENV_FILE:-}" && -x "$peli_bin" ]]; then
                "$peli_bin" down || \
                    warn "bin/pelicula down exited non-zero during teardown — checking for survivors"
            elif [[ -z "${PELICULA_ENV_FILE:-}" ]]; then
                warn "PELICULA_ENV_FILE was never set — skipping bin/pelicula down (nothing to tear down)"
            fi

            if docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^pelicula-test-'; then
                warn "SANITY: pelicula-test containers survived teardown — run:"
                warn "  PELICULA_ENV_FILE=${test_env:-<test env>} PELICULA_COMPOSE_OVERLAY=${SCRIPT_DIR}/compose/docker-compose.test.yml ${peli_bin} down"
            fi
            # Container-created files can defeat a plain rm two ways: on
            # macOS, Docker Desktop's VirtioFS stamps a `deny delete` ACL on
            # dirs containers create (strip with chmod -N; the flag doesn't
            # exist on Linux, hence 2>/dev/null); on Linux they're simply
            # root-owned (delete through a container instead). Never let a
            # cleanup failure set the exit code of an otherwise-green run —
            # it previously leaked straight into the script's exit status.
            if [[ -n "${test_dir:-}" ]] && ! rm -rf "${test_dir}" 2>/dev/null; then
                chmod -RN "${test_dir}" 2>/dev/null || true
                if ! rm -rf "${test_dir}" 2>/dev/null; then
                    $NEEDS_SUDO docker run --rm -v "${test_dir}:/target" alpine \
                        sh -c 'rm -rf /target/* /target/.[!.]*' 2>/dev/null || true
                    rmdir "${test_dir}" 2>/dev/null || \
                        warn "cleanup: leftover temp dir (container-owned files): ${test_dir}"
                fi
            fi
        else
            echo ""
            warn "Test stack left running (--keep is set)."
            warn "Drive it yourself with the same isolated env this run used:"
            warn "  export PELICULA_ENV_FILE=${test_env:-<test env>}"
            warn "  export PELICULA_COMPOSE_OVERLAY=${SCRIPT_DIR}/compose/docker-compose.test.yml"
            warn "  ${peli_bin} down    # tear it down when done"
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

    local test_webhook_secret
    test_webhook_secret="$(LC_ALL=C tr -dc 'a-zA-Z0-9' < /dev/urandom | head -c 32 2>/dev/null \
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
PELICULA_PROJECT_NAME="pelicula-test"
JELLYFIN_ADMIN_USER="admin"
JELLYFIN_PASSWORD="test-jellyfin-pw"
JELLYFIN_PUBLISHED_URL="http://127.0.0.1:${test_port}/jellyfin"
PROCULA_API_KEY="${test_api_key}"
WEBHOOK_SECRET="${test_webhook_secret}"
TRANSCODING_ENABLED=false
NOTIFICATIONS_ENABLED=false
NOTIFICATIONS_MODE=internal
EOF
    chmod 600 "$test_env"

    # Point the CLI at the isolated env + test compose overlay for the rest
    # of this run. bin/pelicula up (Stage 1) reads PELICULA_ENV_FILE instead
    # of the repo-root .env, and appends PELICULA_COMPOSE_OVERLAY last in
    # every compose invocation it makes — this is what makes the isolated
    # stack possible without ever touching the production .env.
    #
    # Exported immediately after test_env is written, and before ANY check
    # that could fail and unwind into cleanup_test (the EXIT trap): cmdDown
    # falls back to the default "pelicula" project name — production's —
    # when PELICULA_ENV_FILE is unset or points at a file that doesn't exist
    # yet. Teardown must never be able to run without this pointed at a
    # real, isolated .env, or a failure here could reach for the production
    # stack instead of the (nonexistent) test one.
    export PELICULA_ENV_FILE="$test_env"
    export PELICULA_COMPOSE_OVERLAY="$SCRIPT_DIR/compose/docker-compose.test.yml"

    # Directory tree + *arr/Jellyfin/Bazarr/qBittorrent config seeding is no
    # longer duplicated here — bin/pelicula up's setupDirs()/SeedAllConfigs()
    # (dirs.go, seed.go) do it, exercising the same code path production
    # installs go through.
    t_pass "Environment initialized"

    # Disk pre-flight, before bring-up (Stage 1) does any real work. Runs
    # after the export above so a failure here still tears down through the
    # isolated pelicula-test project, never production's.
    if ! check_disk_space "$test_dir"; then
        t_fail "Disk pre-flight check failed"
        return 1
    fi

    # ── Stage 1: Build CLI + Start Stack ──────────────

    info "Building pelicula CLI from HEAD..."
    if ! build_pelicula_cli; then
        t_fail "Failed to build bin/pelicula"
        return 1
    fi

    info "Building container images (this may take a minute)..."
    if ! test_compose build 2>&1; then
        t_fail "Image build failed"
        echo ""
        warn "Check Docker logs for details. Run with --keep to investigate."
        return 1
    fi

    # bin/pelicula up seeds every service config, creates the directory
    # tree, starts compose with the vpn profile (the dummy WireGuard key
    # above activates it) plus PELICULA_COMPOSE_OVERLAY, waits for gluetun
    # health, and — as its very last step — runs its own auth-free
    # tests/verify.sh --skip-auth smoke against this same stack. That smoke
    # can legitimately report failures here if pelicula-api isn't fully
    # warmed up yet (up doesn't block on it before running it); it's
    # non-fatal to 'up' and is NOT the same run as the full authed verify.sh
    # e2e invokes later in this script — seeing two verify runs in the log
    # is expected, not a duplicate-test bug.
    info "Starting test stack via bin/pelicula up..."
    if ! "$PELICULA_BIN" up; then
        t_fail "bin/pelicula up failed (bring-up gate)"
        echo ""
        warn "Check Docker logs for details. Run with --keep to investigate."
        return 1
    fi

    # e2e-specific readiness polling that 'up' doesn't cover: 'up' doesn't
    # block on pelicula-api's own health before returning (only on gluetun's,
    # when VPN is configured), so wait for it explicitly here before Stage 2
    # (auto-wire) and the nginx -t check below rely on it being reachable.
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
        return 1
    fi

    t_pass "Stack started"

    # Validate nginx config parses cleanly (catches misplaced limit_req zone refs,
    # template substitution errors, and syntax regressions in nginx.conf).
    # Capture output before grepping: piping straight into `grep -q` lets grep
    # exit on first match and SIGPIPE the upstream `docker compose exec`, which
    # under `set -o pipefail` flips a real pass into a spurious failure.
    local nginx_t_out
    nginx_t_out="$(test_compose exec nginx nginx -t 2>&1 || true)"
    if echo "$nginx_t_out" | grep -q "syntax is ok"; then
        t_pass "nginx -t: config syntax OK"
    else
        t_fail "nginx -t: config syntax error"
        echo ""
        warn "nginx -t output:"
        echo "$nginx_t_out"
        return 1
    fi

    # Validate the NFS library overlay renders. This stack runs in bind-mount
    # mode (local-library), so a pure `docker compose config` render with stub
    # NFS vars is the only automated guard against YAML/interpolation rot in
    # docker-compose.nfs.yml. The two overlays must stay in lockstep (same
    # /media target per media service — see their header comments), so the
    # assertion compares mount counts between them instead of hardcoding one.
    local nfs_render local_render nfs_media local_media
    nfs_render="$(NFS_HOST=nfs-render-check NFS_EXPORT=/volume1/render-check \
        $NEEDS_SUDO docker compose \
            --project-directory "$SCRIPT_DIR" \
            --env-file "$test_env" \
            -f "$COMPOSE_FILE" \
            -f "$SCRIPT_DIR/compose/docker-compose.nfs.yml" \
            -p pelicula-nfs-render \
            config 2>&1 || true)"
    local_render="$(test_compose config 2>&1 || true)"
    nfs_media="$(echo "$nfs_render" | grep -c "target: /media" || true)"
    local_media="$(echo "$local_render" | grep -c "target: /media" || true)"
    if [[ "$nfs_media" -ge 1 && "$nfs_media" -eq "$local_media" ]] \
            && echo "$nfs_render" | grep -q "type: nfs"; then
        t_pass "NFS library overlay renders in lockstep (${nfs_media} /media mounts, type=nfs)"
    else
        t_fail "NFS library overlay render mismatch (nfs: ${nfs_media} /media mounts, local: ${local_media})"
        echo "$nfs_render" | tail -15
    fi

    # ── Stage 2: Wait for Auto-Wire ───────────────────

    info "Waiting for auto-wire to complete (Jellyfin wizard + library setup)..."
    wait=0
    local wired=false
    local stage2_cookies="$test_dir/stage2-cookies.txt"
    local status_resp=""
    while [[ $wait -lt 60 ]]; do
        # /api/pelicula/status is auth-gated, and the admin operator account
        # is created mid-autowire (~15s in). Login fails until then, succeeds
        # afterward; once it succeeds, the authed status call exposes
        # "wired":true when autowire is fully complete.
        if pelicula_login "http://localhost:${test_port}" admin test-jellyfin-pw "$stage2_cookies" 2>/dev/null; then
            status_resp="$(curl -sf --max-time 5 -b "$stage2_cookies" \
                "http://localhost:${test_port}/api/pelicula/status" 2>/dev/null || echo "")"
            if echo "$status_resp" | grep -q '"wired":true'; then
                wired=true
                break
            fi
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
        return 1
    fi

    t_pass "Auto-wire complete"

    # ── Stage 3: Configure Procula + Generate Media ───

    # Disable validation (tiny test file fails the 50MB sample floor).
    # Enable transcoding with a test profile that downscales to 180p.
    # POST directly to procula (port 8282) inside its container to bypass
    # nginx auth_request, which gates /api/procula/ with the session cookie.
    local settings_resp
    settings_resp="$(post_procula_settings false true || echo "error")"
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
        if $NEEDS_SUDO docker exec pelicula-test-procula-1 ffmpeg -y \
            -f lavfi -i "color=c=blue:s=320x240:d=10:r=24" \
            -f lavfi -i "sine=frequency=1000:duration=10:sample_rate=44100" \
            -c:v libx264 -preset ultrafast -crf 28 \
            -c:a aac -b:a 64k \
            "/media/movies/Test Movie (2024)/Test.Movie.2024.mkv" 2>/dev/null; then
            ffmpeg_ok=true
        fi
    fi

    if [[ "$ffmpeg_ok" != "true" ]] || [[ ! -f "$movie_file" ]]; then
        t_fail "Test media generation failed (FFmpeg not available on host or in container)"
        return 1
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
    # (On macOS Docker Desktop, host→published-port traffic is not 127.0.0.1 to nginx.)
    webhook_resp="$(fire_import_webhook 1 "Test Movie" 2024 "Test.Movie.2024.mkv" "$file_size" 10 "test-e2e-$(date +%s)" || echo "")"

    if echo "$webhook_resp" | grep -q '"status":"queued"'; then
        t_pass "Import webhook accepted"
    else
        t_fail "Import webhook rejected or unreachable"
        echo ""
        warn "Response: ${webhook_resp:-<no response>}"
        return 1
    fi

    # ── Stage 5: Wait for Processing ─────────────────

    info "Waiting for Procula to finish processing..."
    wait=0
    local job_state="" job_json=""
    while [[ $wait -lt 60 ]]; do
        # GET directly from procula inside its container to bypass nginx
        # auth_request, which gates /api/procula/ with the session cookie.
        local jobs_resp
        jobs_resp="$($NEEDS_SUDO docker exec pelicula-test-procula-1 wget -qO- \
            'http://localhost:8282/api/procula/jobs' 2>/dev/null || echo "[]")"
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
        return 1
    else
        t_fail "Processing did not complete within 120s (state: ${job_state:-unknown})"
        echo ""
        warn "Procula logs:"
        test_compose logs --tail 40 procula 2>/dev/null || true
        return 1
    fi

    # ── Sidecar verification ──────────────────────────
    # The test-downscale profile has suffix ".test", so Procula should have
    # written a sidecar alongside the original file as a Jellyfin alt version.
    local sidecar_file="$movie_dir/Test.Movie.2024.test.mkv"
    if [[ -f "$sidecar_file" ]]; then
        t_pass "Transcoded sidecar created (Jellyfin alternate version)"
    else
        # Non-fatal: sidecar may be inside the container volume only
        local container_sidecar="/media/movies/Test Movie (2024)/Test.Movie.2024.test.mkv"
        if $NEEDS_SUDO docker exec pelicula-test-procula-1 test -f "$container_sidecar" 2>/dev/null; then
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
        return 1
    fi

    # ── Stage 6: Verify in Jellyfin ──────────────────

    # Give Jellyfin's scan (triggered by procula) a moment to finish, then
    # fire an explicit refresh before polling. Procula's refresh fires at job
    # completion; if that refresh races with an in-progress startup scan the
    # file may not be indexed yet by the time polling begins.
    sleep 5
    $NEEDS_SUDO docker exec pelicula-test-pelicula-api-1 wget -qO- \
        --post-data='' \
        --header="X-API-Key: ${test_api_key}" \
        "http://localhost:8181/api/pelicula/jellyfin/refresh" \
        2>/dev/null || true
    sleep 5

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
        return 1
    fi

    # Jellyfin library scan is async. Poll up to 12×5s = 60s.
    # Re-trigger a Jellyfin refresh every 3rd poll in case the initial
    # refresh raced with an in-progress scan and was a no-op.
    local found=false
    local item_count=0
    local poll_n=0
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
        sleep 5
        poll_n=$(( poll_n + 1 ))
        if [[ $(( poll_n % 3 )) -eq 0 ]]; then
            $NEEDS_SUDO docker exec pelicula-test-pelicula-api-1 wget -qO- \
                --post-data='' \
                --header="X-API-Key: ${test_api_key}" \
                "http://localhost:8181/api/pelicula/jellyfin/refresh" \
                2>/dev/null || true
        fi
        local search_resp
        # Query the library directly (no SearchTerm) so we bypass Jellyfin's
        # search-index rebuild delay. For movies with no TMDB match the search
        # index can lag 60-120s behind the library scan; a direct Items query
        # returns items as soon as the filesystem scan detects them.
        search_resp="$(curl -sf --max-time 10 \
            "http://localhost:${test_port}/jellyfin/Items?IncludeItemTypes=Movie&Recursive=true&Limit=10" \
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
        t_fail "Movie not found in Jellyfin library after 60s"
        echo ""
        warn "Library scan may still be in progress. Jellyfin logs:"
        test_compose logs --tail 30 jellyfin 2>/dev/null || true
    fi

    # ── Stage 8: Auth & Nginx Routing ────────────────

    info "Testing auth routing..."

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

    # GET /api/pelicula/network — bandwidth stats endpoint (auth-gated)
    assert_http 200 "http://localhost:${test_port}/api/pelicula/network" "$cookie_file"
    local net_resp
    net_resp="$(curl -sf --max-time 5 -b "$cookie_file" \
        "http://localhost:${test_port}/api/pelicula/network" 2>/dev/null || echo "{}")"
    if echo "$net_resp" | grep -q '"containers"'; then
        t_pass "GET /api/pelicula/network returns JSON with 'containers' key"
    else
        t_fail "GET /api/pelicula/network response missing 'containers' key"
    fi

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

    # ── Stage 9: Library Registry API ────────────────

    info "Testing library registry API..."

    # Re-login: the auth stage above logs out at the end, so we need a fresh
    # session cookie for the admin-only POST/DELETE endpoints.
    local lib_cookies="$test_dir/lib-cookies.txt"
    pelicula_login "http://localhost:${test_port}" admin test-jellyfin-pw "$lib_cookies" 2>/dev/null

    # GET /api/pelicula/libraries — no auth required; should return at least 2
    # built-in libraries (movies + tv) on a fresh stack.
    local libs_resp
    libs_resp="$(curl -sf --max-time 5 \
        "http://localhost:${test_port}/api/pelicula/libraries" 2>/dev/null || echo "[]")"

    local libs_count
    libs_count="$(echo "$libs_resp" | python3 -c "
import json, sys
try:
    print(len(json.loads(sys.stdin.read())))
except Exception:
    print(0)
" 2>/dev/null || echo "0")"

    if [[ "$libs_count" -ge 2 ]]; then
        t_pass "Library registry returns at least 2 libraries on fresh stack"
    else
        t_fail "Library registry returned ${libs_count} libraries (expected ≥ 2)"
    fi

    # Both built-in slugs must be present.
    if echo "$libs_resp" | python3 -c "
import json, sys
libs = json.loads(sys.stdin.read())
slugs = {l['slug'] for l in libs}
assert 'movies' in slugs and 'tv' in slugs
" 2>/dev/null; then
        t_pass "Default libraries include slugs 'movies' and 'tv'"
    else
        t_fail "Default libraries missing 'movies' or 'tv' slug"
    fi

    # POST /api/pelicula/libraries — add a custom library.
    # Requires admin auth + local Origin header (RequireLocalOriginStrict).
    local add_resp add_code
    add_resp="$(curl -s -w '\n%{http_code}' --max-time 5 \
        -b "$lib_cookies" \
        -X POST "http://localhost:${test_port}/api/pelicula/libraries" \
        -H "Content-Type: application/json" \
        -H "Origin: http://localhost:${test_port}" \
        -d '{"name":"Anime","slug":"anime","type":"tvshows","arr":"sonarr","processing":"audit"}' \
        2>/dev/null)"
    add_code="${add_resp##*$'\n'}"
    if [[ "$add_code" == "201" ]]; then
        t_pass "POST /api/pelicula/libraries returns 201 for new library"
    else
        t_fail "POST /api/pelicula/libraries returned $add_code (expected 201)"
    fi

    # GET /api/pelicula/libraries — anime should now be in the list.
    libs_resp="$(curl -sf --max-time 5 \
        "http://localhost:${test_port}/api/pelicula/libraries" 2>/dev/null || echo "[]")"
    if echo "$libs_resp" | python3 -c "
import json, sys
libs = json.loads(sys.stdin.read())
assert any(l['slug'] == 'anime' for l in libs)
" 2>/dev/null; then
        t_pass "Custom library 'anime' appears in GET /api/pelicula/libraries"
    else
        t_fail "Custom library 'anime' not found after POST"
    fi

    # DELETE /api/pelicula/libraries/movies — built-in; should return 409.
    local del_builtin_code
    del_builtin_code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
        -b "$lib_cookies" \
        -X DELETE "http://localhost:${test_port}/api/pelicula/libraries/movies" \
        -H "Origin: http://localhost:${test_port}" \
        2>/dev/null)"
    if [[ "$del_builtin_code" == "409" ]]; then
        t_pass "DELETE built-in library returns 409"
    else
        t_fail "DELETE built-in library returned $del_builtin_code (expected 409)"
    fi

    # DELETE /api/pelicula/libraries/anime — custom library; should return 204.
    local del_code
    del_code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
        -b "$lib_cookies" \
        -X DELETE "http://localhost:${test_port}/api/pelicula/libraries/anime" \
        -H "Origin: http://localhost:${test_port}" \
        2>/dev/null)"
    if [[ "$del_code" == "204" ]]; then
        t_pass "DELETE custom library 'anime' returns 204"
    else
        t_fail "DELETE custom library returned $del_code (expected 204)"
    fi

    # GET /api/pelicula/libraries — anime should be gone.
    libs_resp="$(curl -sf --max-time 5 \
        "http://localhost:${test_port}/api/pelicula/libraries" 2>/dev/null || echo "[]")"
    if echo "$libs_resp" | python3 -c "
import json, sys
libs = json.loads(sys.stdin.read())
assert not any(l['slug'] == 'anime' for l in libs)
" 2>/dev/null; then
        t_pass "Custom library 'anime' removed from registry after DELETE"
    else
        t_fail "Custom library 'anime' still present after DELETE"
    fi

    # Verify Jellyfin library wiring: autowire should have created a "Movies"
    # and a "TV Shows" virtual folder pointing at the /media/movies and /media/tv
    # paths. We can check this via the Jellyfin admin API using the token obtained
    # in Stage 6.
    if [[ -n "$jf_token" ]]; then
        local jf_folders_resp
        jf_folders_resp="$(curl -sf --max-time 10 \
            "http://localhost:${test_port}/jellyfin/Library/VirtualFolders" \
            -H "X-Emby-Authorization: MediaBrowser Client=\"PeliculaTest\", Device=\"e2e\", DeviceId=\"pelicula-e2e-test\", Version=\"1.0\", Token=\"${jf_token}\"" \
            2>/dev/null || echo "[]")"
        if echo "$jf_folders_resp" | python3 -c "
import json, sys
folders = json.loads(sys.stdin.read())
names = {f['Name'] for f in folders}
assert 'Movies' in names and 'TV Shows' in names
" 2>/dev/null; then
            t_pass "Jellyfin has 'Movies' and 'TV Shows' libraries after autowire"
        else
            t_fail "Jellyfin missing 'Movies' or 'TV Shows' library after autowire"
        fi
    else
        warn "Skipping Jellyfin library wiring check (no Jellyfin token — Stage 6 auth failed)"
    fi

    # ── Verify smoke against isolated stack ──────────
    # Must run BEFORE Stage 10: the Playwright suite includes
    # login-debounce.spec.js, which deliberately submits wrong passwords and
    # trips peligrosa's anti-brute-force limiter (5 failed logins / 5 min per
    # IP → 429 for the rest of the window). The verify suites each perform a
    # real login and would all 429 if they ran inside that poisoned window.

    info "Running verify smoke against isolated stack (localhost:${test_port})..."
    # This is the SECOND verify.sh run in this log, not a duplicate: the
    # first ran auth-free inside bin/pelicula up (Stage 1) as its own
    # post-deploy smoke. The isolated stack's own admin credentials (seeded
    # into test_env above) are known here, so this run uses real auth
    # instead of --skip-auth — this unlocks the 9 authenticated suites
    # (bug1-reconcile, bug4-registration, sweep-catalog/jobs/users/settings,
    # sweep-search-options, sweep-remove-action, sweep-search-seasons) instead
    # of silently skipping them. PELICULA_ENV_FILE, exported in
    # Stage 0, is inherited by this subshell and flows through to every
    # suite's own ENV_FILE resolution (see tests/lib.sh's peli_load_env doc).
    if PELICULA_TEST_JELLYFIN_USER=admin PELICULA_TEST_JELLYFIN_PASSWORD=test-jellyfin-pw \
            bash "$SCRIPT_DIR/tests/verify.sh" --target "localhost:${test_port}"; then
        t_pass "verify smoke passed"
    else
        t_fail "verify smoke failed (tests/verify.sh)"
    fi

    # ── Stage 10: Playwright UI Tests ────────────────

    # Check for the project's own pinned Playwright install (tests/playwright/node_modules),
    # not just any `npx`-reachable playwright — `npx playwright --version` run from
    # $SCRIPT_DIR (this script's cwd) can't see node_modules several directories down,
    # so npm would silently auto-install an unpinned "latest" playwright over the network
    # just to answer the version check. Checking the local binary directly avoids that.
    if command -v npx &>/dev/null && [[ -x "$SCRIPT_DIR/tests/playwright/node_modules/.bin/playwright" ]]; then
        info "Seeding Playwright test fixtures..."

        # Fixture 1: Sintel (2010) — real TMDB title so scan produces a match
        local pw_movie_dir="$test_library_dir/movies/Sintel (2010)"
        local pw_movie_file="$pw_movie_dir/Sintel.2010.mkv"
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
            if $NEEDS_SUDO docker exec pelicula-test-procula-1 ffmpeg -y \
                -f lavfi -i "color=c=blue:s=320x240:d=10:r=24" \
                -f lavfi -i "sine=frequency=440:duration=10:sample_rate=44100" \
                -c:v libx264 -preset ultrafast -crf 28 \
                -c:a aac -b:a 64k \
                "/media/movies/Sintel (2010)/Sintel.2010.mkv" 2>/dev/null; then
                pw_ffmpeg_ok=true
            fi
        fi

        # Fixture 2: Night of the Living Dead for subtitle-acquisition spec.
        # Place the file in /media/movies (Jellyfin's library root) so the Jellyfin refresh
        # fired by CatalogEarly actually makes the movie visible in the library.
        local pw_notld_dir="$test_library_dir/movies/Night of the Living Dead (1968)"
        local pw_notld_file="$pw_notld_dir/Night.of.the.Living.Dead.1968.mkv"
        mkdir -p "$pw_notld_dir"
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
                $NEEDS_SUDO docker exec pelicula-test-procula-1 ffmpeg -y \
                    -f lavfi -i "color=c=black:s=320x240:d=15:r=24" \
                    -f lavfi -i "sine=frequency=220:duration=15:sample_rate=44100" \
                    -c:v libx264 -preset ultrafast -crf 28 \
                    -c:a aac -b:a 64k \
                    -metadata title="Night of the Living Dead" \
                    -metadata year="1968" \
                    "/media/movies/Night of the Living Dead (1968)/Night.of.the.Living.Dead.1968.mkv" 2>/dev/null || pw_ffmpeg_ok=false
            fi
        fi

        # Fixture 3: Sub Timeout (2099) — no subtitle tracks, padded to 64 MB
        # so the validation sample-floor check (≥50 MB) passes.  Bazarr has no
        # configured providers in the test stack, so await_subs will time out
        # after sub_acquire_timeout_min=1 minute (set in Stage 3 settings).
        local pw_timeout_dir="$test_library_dir/movies/Pelicula Timeout Fixture (2099)"
        local pw_timeout_file="$pw_timeout_dir/Pelicula.Timeout.Fixture.2099.mkv"
        mkdir -p "$pw_timeout_dir"
        if [[ "$pw_ffmpeg_ok" == "true" ]]; then
            local pw_timeout_ok=false
            if command -v ffmpeg &>/dev/null; then
                if ffmpeg -y \
                    -f lavfi -i "color=c=red:s=320x240:d=15:r=24" \
                    -f lavfi -i "sine=frequency=440:duration=15:sample_rate=44100" \
                    -c:v libx264 -preset ultrafast -crf 28 \
                    -c:a aac -b:a 64k \
                    -metadata title="Pelicula Timeout Fixture" -metadata year="2099" \
                    "$pw_timeout_file" 2>/dev/null; then
                    pw_timeout_ok=true
                fi
            fi
            if [[ "$pw_timeout_ok" != "true" ]]; then
                if $NEEDS_SUDO docker exec pelicula-test-procula-1 ffmpeg -y \
                        -f lavfi -i "color=c=red:s=320x240:d=15:r=24" \
                        -f lavfi -i "sine=frequency=440:duration=15:sample_rate=44100" \
                        -c:v libx264 -preset ultrafast -crf 28 \
                        -c:a aac -b:a 64k \
                        -metadata title="Pelicula Timeout Fixture" -metadata year="2099" \
                        "/media/movies/Pelicula Timeout Fixture (2099)/Pelicula.Timeout.Fixture.2099.mkv" 2>/dev/null; then
                    pw_timeout_ok=true
                fi
            fi
            if [[ "$pw_timeout_ok" == "true" ]]; then
                # Pad to 64 MB so it clears the 50 MB validation sample floor.
                # truncate fills with zero bytes; ffprobe ignores trailing nulls
                # because MKV parsing is EBML-bounded.
                if command -v truncate &>/dev/null; then
                    truncate -s 67108864 "$pw_timeout_file" 2>/dev/null || true
                else
                    local timeout_sz
                    timeout_sz=$(stat -f%z "$pw_timeout_file" 2>/dev/null || stat -c%s "$pw_timeout_file" 2>/dev/null || echo 0)
                    if [[ "$timeout_sz" -gt 0 && "$timeout_sz" -lt 67108864 ]]; then
                        dd if=/dev/zero bs=1048576 \
                            count=$(( (67108864 - timeout_sz + 1048575) / 1048576 )) \
                            >> "$pw_timeout_file" 2>/dev/null || true
                    fi
                fi
            else
                warn "Sub Timeout fixture generation failed (non-fatal — sub-timeout spec will be skipped)"
            fi
        fi

        # Fixture 4: Dualsub Happy (2024) — embedded en+es SRT streams so
        # GenerateDualSubs can extract both cue sets and write .en-es.ass.
        local pw_happy_dir="$test_library_dir/movies/Dualsub Happy (2024)"
        local pw_happy_file="$pw_happy_dir/Dualsub.Happy.2024.mkv"
        mkdir -p "$pw_happy_dir"
        if [[ "$pw_ffmpeg_ok" == "true" ]]; then
            printf '1\n00:00:01,000 --> 00:00:03,000\nHello world\n\n2\n00:00:04,000 --> 00:00:06,000\nGoodbye world\n' \
                > "$pw_happy_dir/en.srt"
            printf '1\n00:00:01,000 --> 00:00:03,000\nHola mundo\n\n2\n00:00:04,000 --> 00:00:06,000\nAdios mundo\n' \
                > "$pw_happy_dir/es.srt"
            local pw_happy_base="$pw_happy_dir/base.mkv"
            local pw_happy_ok=false
            if command -v ffmpeg &>/dev/null; then
                if ffmpeg -y \
                        -f lavfi -i "color=c=purple:s=320x240:d=10:r=24" \
                        -f lavfi -i "sine=frequency=440:duration=10:sample_rate=44100" \
                        -c:v libx264 -preset ultrafast -crf 28 \
                        -c:a aac -b:a 64k \
                        "$pw_happy_base" 2>/dev/null && \
                    ffmpeg -y \
                        -i "$pw_happy_base" \
                        -i "$pw_happy_dir/en.srt" -i "$pw_happy_dir/es.srt" \
                        -map 0:v -map 0:a -map 1 -map 2 \
                        -c:v copy -c:a copy -c:s srt \
                        -metadata:s:s:0 language=eng \
                        -metadata:s:s:1 language=spa \
                        -metadata title="Dualsub Happy" -metadata year="2024" \
                        "$pw_happy_file" 2>/dev/null; then
                    pw_happy_ok=true
                fi
            fi
            if [[ "$pw_happy_ok" != "true" ]]; then
                if $NEEDS_SUDO docker exec pelicula-test-procula-1 ffmpeg -y \
                            -f lavfi -i "color=c=purple:s=320x240:d=10:r=24" \
                            -f lavfi -i "sine=frequency=440:duration=10:sample_rate=44100" \
                            -c:v libx264 -preset ultrafast -crf 28 \
                            -c:a aac -b:a 64k \
                            "/media/movies/Dualsub Happy (2024)/base.mkv" 2>/dev/null && \
                    $NEEDS_SUDO docker exec pelicula-test-procula-1 ffmpeg -y \
                            -i "/media/movies/Dualsub Happy (2024)/base.mkv" \
                            -i "/media/movies/Dualsub Happy (2024)/en.srt" \
                            -i "/media/movies/Dualsub Happy (2024)/es.srt" \
                            -map 0:v -map 0:a -map 1 -map 2 \
                            -c:v copy -c:a copy -c:s srt \
                            -metadata:s:s:0 language=eng \
                            -metadata:s:s:1 language=spa \
                            -metadata title="Dualsub Happy" -metadata year="2024" \
                            "/media/movies/Dualsub Happy (2024)/Dualsub.Happy.2024.mkv" 2>/dev/null; then
                    pw_happy_ok=true
                fi
            fi
            rm -f "$pw_happy_dir/base.mkv" "$pw_happy_dir/en.srt" "$pw_happy_dir/es.srt"
            if [[ "$pw_happy_ok" != "true" ]]; then
                warn "Dualsub Happy fixture generation failed — dualsub-happy spec may fail"
            fi
        fi

        # Fixture 5: Dualsub Failed (2024) — embedded en only; there is no es
        # cue source and no translation fallback, so GenerateDualSubs records
        # a dualsub_error and DualSubOutputs is empty (non-fatal).
        local pw_failed_dir="$test_library_dir/movies/Dualsub Failed (2024)"
        local pw_failed_file="$pw_failed_dir/Dualsub.Failed.2024.mkv"
        mkdir -p "$pw_failed_dir"
        if [[ "$pw_ffmpeg_ok" == "true" ]]; then
            printf '1\n00:00:01,000 --> 00:00:03,000\nHello world\n\n2\n00:00:04,000 --> 00:00:06,000\nGoodbye world\n' \
                > "$pw_failed_dir/en.srt"
            local pw_failed_base="$pw_failed_dir/base.mkv"
            local pw_failed_ok=false
            if command -v ffmpeg &>/dev/null; then
                if ffmpeg -y \
                        -f lavfi -i "color=c=green:s=320x240:d=10:r=24" \
                        -f lavfi -i "sine=frequency=440:duration=10:sample_rate=44100" \
                        -c:v libx264 -preset ultrafast -crf 28 \
                        -c:a aac -b:a 64k \
                        "$pw_failed_base" 2>/dev/null && \
                    ffmpeg -y \
                        -i "$pw_failed_base" \
                        -i "$pw_failed_dir/en.srt" \
                        -map 0:v -map 0:a -map 1 \
                        -c:v copy -c:a copy -c:s srt \
                        -metadata:s:s:0 language=eng \
                        -metadata title="Dualsub Failed" -metadata year="2024" \
                        "$pw_failed_file" 2>/dev/null; then
                    pw_failed_ok=true
                fi
            fi
            if [[ "$pw_failed_ok" != "true" ]]; then
                if $NEEDS_SUDO docker exec pelicula-test-procula-1 ffmpeg -y \
                            -f lavfi -i "color=c=green:s=320x240:d=10:r=24" \
                            -f lavfi -i "sine=frequency=440:duration=10:sample_rate=44100" \
                            -c:v libx264 -preset ultrafast -crf 28 \
                            -c:a aac -b:a 64k \
                            "/media/movies/Dualsub Failed (2024)/base.mkv" 2>/dev/null && \
                    $NEEDS_SUDO docker exec pelicula-test-procula-1 ffmpeg -y \
                            -i "/media/movies/Dualsub Failed (2024)/base.mkv" \
                            -i "/media/movies/Dualsub Failed (2024)/en.srt" \
                            -map 0:v -map 0:a -map 1 \
                            -c:v copy -c:a copy -c:s srt \
                            -metadata:s:s:0 language=eng \
                            -metadata title="Dualsub Failed" -metadata year="2024" \
                            "/media/movies/Dualsub Failed (2024)/Dualsub.Failed.2024.mkv" 2>/dev/null; then
                    pw_failed_ok=true
                fi
            fi
            rm -f "$pw_failed_dir/base.mkv" "$pw_failed_dir/en.srt"
            if [[ "$pw_failed_ok" != "true" ]]; then
                warn "Dualsub Failed fixture generation failed — dualsub-failed spec may fail"
            fi
        fi

        if [[ "$pw_ffmpeg_ok" != "true" ]]; then
            warn "Playwright fixture generation failed — skipping UI tests"
        else
            t_pass "Playwright fixtures seeded"

            # Pre-fire Night of the Living Dead import webhook from inside Docker
            # (nginx IP-restricts /api/pelicula/hooks/import to internal networks;
            # Playwright runs on the host and can't call it directly through nginx)
            # Path uses /media/movies so CatalogEarly's Jellyfin refresh picks it up.
            info "Pre-firing Night of the Living Dead import webhook..."
            fire_import_webhook 1968 "Night of the Living Dead" 1968 "Night.of.the.Living.Dead.1968.mkv" 500000 5760 "playwright-notld-test" >/dev/null || true

            # Pre-fire Sub Timeout webhook.
            # Temporarily re-enable validation so checkMissingSubtitles runs and
            # populates MissingSubs → await_subs stage is entered.  Bazarr has no
            # configured providers, so it times out after sub_acquire_timeout_min=1 min.
            # Validation is re-disabled after a brief sleep to ensure the worker has
            # picked up the job and read its settings (worker reads settings once at
            # job start; the sleep makes the race window negligibly small).
            if [[ -f "$pw_timeout_file" ]]; then
                info "Temporarily enabling validation for Sub Timeout fixture..."
                post_procula_settings true false >/dev/null || true
                info "Pre-firing Sub Timeout import webhook..."
                fire_import_webhook 2099 "Pelicula Timeout Fixture" 2099 "Pelicula.Timeout.Fixture.2099.mkv" 67108864 15 "playwright-timeout-test" >/dev/null || true
                # Brief wait so the worker has picked up the job and read validation=true.
                sleep 5
                # Restore standard test settings (validation off, transcoding on).
                post_procula_settings false true >/dev/null || true
            fi

            # Pre-fire Dualsub Happy import webhook.
            if [[ -f "$pw_happy_file" ]]; then
                info "Pre-firing Dualsub Happy import webhook..."
                fire_import_webhook 2024 "Dualsub Happy" 2024 "Dualsub.Happy.2024.mkv" 500000 10 "playwright-dualsub-happy-test" >/dev/null || true
            fi

            # Pre-fire Dualsub Failed import webhook.
            if [[ -f "$pw_failed_file" ]]; then
                info "Pre-firing Dualsub Failed import webhook..."
                fire_import_webhook 2025 "Dualsub Failed" 2024 "Dualsub.Failed.2024.mkv" 500000 10 "playwright-dualsub-failed-test" >/dev/null || true
            fi

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

    # ── Rate-limit burst test (LAST — pollutes auth lockout) ─────────────────
    # Runs after every other auth-required stage because it deliberately trips
    # peligrosa's anti-brute-force lockout (5 failed POSTs in 5 min → 429 for
    # that IP for the remainder of the window). Stages 9/10/11 all need admin
    # login from the same source IP and would 429 if this ran earlier.
    #
    # Soft-skip when /api/pelicula/auth/login is not active (returns 404 = peligrosa
    # auth layer not enabled). When enabled, fire 12 bad-credential POSTs in a
    # tight loop; nginx's peli_auth zone (burst=5 nodelay, 10r/m) or peligrosa's
    # per-IP lockout will reject at least one with 429.
    local probe_code
    probe_code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
        -X POST "http://localhost:${test_port}/api/pelicula/auth/login" \
        -H "Content-Type: application/json" \
        -d '{"username":"ratelimit-probe","password":"probe"}' 2>/dev/null || echo "000")"

    if [[ "$probe_code" == "404" ]]; then
        warn "Skipping rate-limit burst test: /api/pelicula/auth/login returned 404 (peligrosa not enabled)"
    else
        local got_429=0
        for _i in $(seq 1 12); do
            local burst_code
            burst_code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
                -X POST "http://localhost:${test_port}/api/pelicula/auth/login" \
                -H "Content-Type: application/json" \
                -d '{"username":"burst-test","password":"burst-test"}' 2>/dev/null || echo "000")"
            if [[ "$burst_code" == "429" ]]; then
                got_429=1
                break
            fi
        done

        if [[ $got_429 -eq 1 ]]; then
            t_pass "Rate-limit burst: 429 returned before 12th login attempt"
        else
            t_fail "Rate-limit burst: no 429 after 12 rapid POSTs to /api/pelicula/auth/login"
        fi
    fi

    # ── Summary ───────────────────────────────────────

    echo ""
    local total=$((test_passes + test_failures))
    if [[ $test_failures -eq 0 ]]; then
        echo -e "  ${GREEN}${BOLD}ALL TESTS PASSED${NC} (${test_passes}/${total})"
        echo ""
        # Disable the trap and run cleanup directly while locals are still in scope.
        trap - EXIT
        cleanup_test
    else
        echo -e "  ${RED}${BOLD}${test_failures} FAILED${NC}, ${test_passes} passed (${test_passes}/${total})"
        echo ""
        echo "  Debug commands:"
        echo "    bash tests/e2e.sh --keep        re-run, keep containers up"
        echo "  Then, with the PELICULA_ENV_FILE/PELICULA_COMPOSE_OVERLAY exports"
        echo "  that --keep run prints on exit:"
        echo "    ${SCRIPT_DIR}/bin/pelicula logs procula"
        echo "    ${SCRIPT_DIR}/bin/pelicula logs pelicula-api"
        echo "    ${SCRIPT_DIR}/bin/pelicula logs jellyfin"
        echo ""
        trap - EXIT
        cleanup_test
        return 1
    fi
}

if ! cmd_test "${1:-}"; then
    exit 1
fi
