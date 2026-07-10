# Procula — Media Processing Pipeline

Procula is the processing and storage management service for the Pelicula media stack. It handles everything between "download complete" and "ready to watch": import tracking, file validation, transcoding, subtitle acquisition, catalog updates, and storage lifecycle management.

## Architecture

```
Radarr/Sonarr                   pelicula-api                    Procula (:8282)
  |                                 |                               |
  |-- import webhook -------------->|-- POST /api/procula/jobs ---->|
  |                                 |                               |
  |                                 |   queue + persist (SQLite)    |
  |                                 |                               |
  |                                 |<-- GET /api/procula/status ---|
  |                                 |    (dashboard polls this)     |
  |                                 |                               |
  |                                 |                          [Pipeline]
  |                                 |                           1. Validate
  |                                 |                           2. Process
  |                                 |                           3. Catalog
  |                                 |                               |
  |                                 |<-- POST /api/pelicula/       |
  |                                 |    downloads/cancel           |
  |                                 |    (if validation fails,      |
  |<-- blocklist via *arr API ------|     auto-blocklist+research)  |
```

### How jobs enter the queue

Radarr and Sonarr fire **Connect** webhooks on import. The middleware receives these at `POST /api/pelicula/hooks/import`, normalizes the payload, and forwards it to Procula's job queue (`POST /api/procula/jobs`). This keeps Procula decoupled — it never talks to the *arr apps directly. The middleware remains the single coordination point.

**Webhook authentication:** The import hook is protected by a shared secret (`WEBHOOK_SECRET` in `.env`). The autowired Sonarr/Radarr webhook URL does not embed the secret; instead, it is delivered via the `X-Webhook-Secret` request header. The nginx config also restricts the route to the Docker internal network (172.16.0.0/12). Existing installs without `WEBHOOK_SECRET` in `.env` continue to work (the check is skipped when the env var is unset).

**Storage back-pressure:** When the storage monitor (runs every 5 minutes) determines that any monitored filesystem has crossed the critical threshold (default 95%), `POST /api/procula/jobs` returns `HTTP 503 Service Unavailable` with `Retry-After: 300` and a JSON body `{"error":"storage_critical","message":"..."}`. New job admission is paused until usage drops back below the *warning* threshold (default 85%), not merely the critical threshold — this hysteresis prevents rapid flip-flopping when usage hovers near 95%. Warning state (85–95%) is notification-only and does not pause admission. The middleware webhook handler already retries on 5xx, so the *arr import flow naturally backs off without any additional configuration.

### Service layout

| Service | Port | Role |
|---------|------|------|
| `procula` | 8282 | Job queue, pipeline orchestration, FFmpeg processing, storage management |
| `pelicula-api` | 8181 | Receives webhooks, forwards jobs, serves dashboard data, proxies Procula status |

Procula runs as a single Go binary with FFmpeg installed in the container image (Alpine + FFmpeg). Procula's single external Go dependency is `modernc.org/sqlite` (pure-Go SQLite driver, no CGO).

Bazarr (subtitle acquisition) is integrated — see the Await Subs stage below.

## Container Definition

```yaml
procula:
  build: ./procula
  container_name: procula
  restart: unless-stopped
  environment:
    - PUID=${PUID}
    - PGID=${PGID}
    - TZ=${TZ}
    - CONFIG_DIR=/config
    - PELICULA_API_URL=http://pelicula-api:8181
    - PROCULA_API_KEY=${PROCULA_API_KEY:-}
    - PELICULA_SUB_LANGS=${PELICULA_SUB_LANGS:-en}
    - PELICULA_AUDIO_LANG=${PELICULA_AUDIO_LANG:-eng}
    - BAZARR_URL=${BAZARR_URL:-http://bazarr:6767/bazarr}
  volumes:
    - ${CONFIG_DIR}/procula:/config                  # job state, transcode profiles
    - ${CONFIG_DIR}/bazarr:/config/bazarr:ro         # Bazarr config (API key)
    - ${WORK_DIR}/downloads:/downloads:ro            # Read: completed downloads
    - ${LIBRARY_DIR}:/media                          # Read/write: full media library
    - ${WORK_DIR}/processing:/processing             # Scratch space for transcoding
  healthcheck:
    test: ["CMD", "wget", "--spider", "-q", "http://localhost:8282/ping"]
    interval: 30s
    timeout: 10s
    retries: 3
```

**Startup-loaded env vars:** `PROCULA_API_KEY`, `JELLYFIN_REFRESH_DEBOUNCE_MS`, and `PELICULA_AUDIO_LANG` are read once at startup into package-level vars (R14 P3) — they are not re-read on every request.

### Dockerfile

```dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY cmd/ ./cmd/
RUN go build -o procula ./cmd/procula/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates ffmpeg
WORKDIR /
COPY --from=build /app/procula /usr/local/bin/
EXPOSE 8282
CMD ["procula"]
```

No CGO — pure-Go build. FFprobe is bundled as part of the `ffmpeg` Alpine package.

**Queue implementation:** Jobs are persisted in SQLite (`procula.db`, tables: `jobs`, `settings`) via `modernc.org/sqlite` (pure-Go SQLite driver, no CGO — Procula's single external Go dependency). The single-goroutine worker means there is no lock contention beyond standard SQLite serialized writes.

**Historical note:** Pre-SQLite installs (schema versions before the SQLite migration) once had legacy JSON job files under `/config/procula/jobs/` migrated automatically on first startup. That migration code (`migrate_json.go`, plus the equivalent `middleware/internal/repo/migratejson` for the middleware's own JSON state) has since been removed — the schema has been stable at SQLite for many releases and no supported upgrade path still needs it. Pre-SQLite JSON state is no longer auto-migrated; an install with leftover un-migrated JSON files from that era would need to be rebuilt from a fresh setup.

## API Endpoints

### Procula service (port 8282)

```
GET    /ping                                          Health check
GET    /api/procula/status                            Queue summary: pending, processing, completed, failed counts
GET    /api/procula/jobs                              List jobs with state, progress, stage
POST   /api/procula/jobs                              Create job (called by pelicula-api) — requires X-API-Key
GET    /api/procula/jobs/:id                          Single job detail
POST   /api/procula/jobs/:id/retry                   Retry a failed job — requires X-API-Key
POST   /api/procula/jobs/:id/cancel                  Cancel a running/pending job — requires X-API-Key
POST   /api/procula/jobs/:id/resub                   Re-trigger subtitle search for a job — requires X-API-Key
GET    /api/procula/storage                           Disk usage per volume, growth rate, time-to-full
POST   /api/procula/storage/scan                     Trigger an on-demand storage scan — requires X-API-Key
GET    /api/procula/updates                           Latest update check result
GET    /api/procula/notifications                     Dashboard notification feed (paginated)
GET    /api/procula/settings                          Read Procula settings
POST   /api/procula/settings                         Save Procula settings — requires X-API-Key
GET    /api/procula/profiles                          List transcode profiles
POST   /api/procula/profiles                         Create/update transcode profile — requires X-API-Key
DELETE /api/procula/profiles/:name                   Delete a transcode profile — requires X-API-Key
GET    /api/procula/dualsub-profiles                 List dual-subtitle render profiles
POST   /api/procula/dualsub-profiles                 Create dual-subtitle profile — requires X-API-Key
PUT    /api/procula/dualsub-profiles/:name           Update dual-subtitle profile — requires X-API-Key
DELETE /api/procula/dualsub-profiles/:name           Delete dual-subtitle profile — requires X-API-Key
GET    /api/procula/subtitle-tracks                  List subtitle tracks for a given file path
DELETE /api/procula/dualsub-sidecars                 Delete generated ASS sidecar files — requires X-API-Key
POST   /api/procula/subtitles/search                 Trigger a per-language Bazarr subtitle search — requires X-API-Key
POST   /api/procula/transcode                        Enqueue a manual transcode action — requires X-API-Key
GET    /api/procula/events                           Paginated JSON GET of pipeline event log entries (`?limit=&offset=&type=`) — not Server-Sent Events despite the name
POST   /api/procula/actions                          Enqueue an action-bus job — requires X-API-Key
GET    /api/procula/actions/registry                 List registered action handlers
GET    /api/procula/catalog/flags                    Catalog flag summary ("Needs Attention" entries)
GET    /api/procula/blocked-releases                 List blocked/removed releases
DELETE /api/procula/blocked-releases/:id             Remove a blocked release record — requires X-API-Key
```

### New pelicula-api endpoints

```
POST /api/pelicula/hooks/import     Receives *arr webhook, creates Procula job
GET  /api/pelicula/processing       Proxies Procula job status for dashboard
GET  /api/pelicula/storage          Proxies Procula storage stats for dashboard
```

## Pipeline Stages

### Stage 1: Validate

Runs immediately when a job enters the queue. Fast checks, no heavy processing.

| Check | How | Fail action |
|-------|-----|-------------|
| **FFprobe integrity** | `ffprobe -v error -count_frames -of json` on each stream | Blocklist release, trigger re-search |
| **Duration sanity** | Compare FFprobe duration vs expected runtime from *arr metadata (passed in job payload) | Warn if >10% deviation, fail if >50% |
| **Codec detection** | Extract video codec, audio codec, subtitle tracks | Log for processing stage decisions |
| **Sample detection** | File size vs expected (a "1080p movie" under 500MB is suspicious) | Blocklist release, trigger re-search |

**Not implemented: archive detection.** RAR/ZIP unpacking was planned but never built — Sonarr/Radarr already unpack archives themselves before firing the import webhook, so by the time a job reaches Procula the file is not an archive. `Validate()` performs exactly the four checks above.

On validation failure:
1. Procula calls `POST /api/pelicula/downloads/cancel` with `blocklist: true` and the appropriate reason
2. The middleware handles the *arr blocklist + unmonitor
3. The watcher (already exists) picks up the still-missing item and triggers a new search
4. **File deletion:** Only if `delete_on_failure: true` is set in Procula settings, **and** the file path is under `/downloads` or `/processing`. Paths under `/movies` or `/tv` are never deleted — those are already-imported files and removing them in response to a re-queued job would be destructive.

### Stage 2: Process

Runs after validation passes. Heavy work happens here. (There is no separate extraction sub-stage — see the archive-detection note under Stage 1: Validate.)

**2a. Transcode (if profile matches)**
Transcode profiles are stored in `/config/procula/profiles/` as JSON:

```json
{
  "name": "mobile-1080p",
  "enabled": true,
  "conditions": {
    "min_height": 2160,
    "codecs_include": ["hevc", "h265"],
    "max_source_height": 2160
  },
  "output": {
    "video_codec": "libx264",
    "video_preset": "medium",
    "video_crf": 20,
    "max_height": 1080,
    "audio_codec": "aac",
    "audio_channels": 2,
    "suffix": "-mobile"
  }
}
```

A profile's `conditions` determine which files it applies to:
- `codecs_include` and `min_height` are **triggers** — a profile matches if *any* trigger it specifies is satisfied (they OR together). A profile with no triggers at all matches everything (catch-all).
- `max_source_height` is a **ceiling**, not a trigger — when set, it *excludes* sources at or above that height regardless of whether a trigger fired (it ANDs against the rest). Use it so one profile doesn't shadow a later, more specific one for the same codec at a higher resolution.
- Don't confuse `conditions.max_source_height` (how tall the *input* may be for this profile to apply) with `output.max_height` (how tall the *output* is scaled to) — they're independent fields on different objects.

**Only the first matching enabled profile is applied — there is no fan-out.** Procula walks a file's enabled profiles in order and transcodes with the first one whose conditions match; it does not run every matching profile to produce multiple outputs (e.g. it will not keep a 4K master *and* also produce a 1080p copy from a single job). Profiles are loaded from `/config/procula/profiles/*.json` in the directory's lexical filename order, so filename sort order **is** evaluation priority — name profile files (or the `name` field, which the filename is derived from) so the profile you want checked first sorts first alphabetically.

The three shipped default profiles rely on this: `compatibility-1080p.json` and `compatibility-720p.json` both sort before `downscale-4k-to-1080p.json`, so without a ceiling condition either compatibility profile would claim every HEVC/AV1 source — including 4K ones — before the downscale profile is ever reached. Both compatibility profiles set `max_source_height: 2160` for exactly this reason: it excludes 4K-and-above sources so they fall through to the downscale profile instead.

FFmpeg invocation is shelled out from Go:
```
ffmpeg -i input.mkv -c:v libx264 -preset medium -crf 20 -vf scale=-2:1080 \
       -c:a aac -ac 2 -c:s copy -movflags +faststart output-mobile.mkv
```

Progress tracking: parse FFmpeg's stderr for `frame=` / `time=` / `speed=` to report percentage.

**2b. Audio normalization (optional per profile)**
```
ffmpeg -i input.mkv -af loudnorm=I=-14:TP=-2:LRA=7 ...
```

**2c. Subtitle handling**
- Detect embedded subtitle tracks via FFprobe
- If none found for configured languages, flags them in `missing_subs`; the Await Subs stage then kicks Bazarr and waits for sidecars (see [Await Subs](#await-subs) below for how this no longer blocks the worker)

### Await Subs

Procula has a single sequential worker. Job-record writes are serialized per job ID — a per-job mutex guards each job's read-modify-write update cycle — which is what makes it safe for a background goroutine to keep updating a job's record after the worker has moved on to the next one (see below). Waiting for Bazarr to deliver a missing subtitle sidecar can take minutes — the timeout defaults to 30 minutes — and historically this blocked the single worker from picking up the next queued job for the whole wait.

Await Subs now **parks** instead of blocking: when a job has missing subtitles, the wait (and the rest of that job's pipeline run — dual-sub generation, transcoding, late catalog) is handed off to a background goroutine, and the worker immediately moves on to the next queued job. Parking is bounded to **8 concurrent slots**; if all 8 are in use when a job needs to park, that job falls back to the old inline (worker-blocking) wait rather than skipping subtitle acquisition — a deliberate bounded degradation instead of unbounded goroutine growth after a large import burst.

FFmpeg transcodes remain **serialized process-wide** regardless of parking — a single-slot gate ensures at most one FFmpeg process runs at a time, since parked continuations could otherwise reach the transcode stage concurrently and stack multiple FFmpeg processes on NAS-class hardware.

Parked jobs behave like any other in-flight job: cancelling one commits `StateCancelled` and unblocks its wait immediately, and if the Procula process dies while a job is parked, the job row is still `state=processing` — the normal crash-recovery path re-queues it from the Validate stage on restart (stages are idempotent, and already-acquired subtitle sidecars are picked up on the first poll).

### Stage 3: Catalog

After processing completes:

1. **Trigger Jellyfin scan** — Procula calls `POST /api/pelicula/jellyfin/refresh` on pelicula-api, which proxies the Jellyfin library scan. Authenticated via `X-API-Key: <PROCULA_API_KEY>`.
2. **Verify Jellyfin picked it up** — poll `GET /jellyfin/Items?searchTerm=...` until the item appears (timeout after 60s)
3. **Update job record** — mark as completed with metadata (final file paths, sizes, codecs, duration)
4. **Notification** (if configured) — sends an external notification with the event payload. Three notification modes, configured via `/config/procula/notifications.json` (or the Settings UI):
   - **`internal`** (default) — notification stored in the dashboard feed only; no external call
   - **`apprise`** — forwards to the Apprise container at `http://apprise:8000/notify` using provider URLs (e.g. `ntfy://topic`, `gotify://host/token`). Apprise supports dozens of services; see [Apprise docs](https://github.com/caronc/apprise) for the full URL schema.
   - **`direct`** — POST to a single arbitrary webhook URL as JSON. Compatible with ntfy HTTP API, Gotify, and generic webhook receivers.

### Stage 4: Storage Management

Runs on a schedule (configurable, default every 6 hours) and on-demand via API.

**Disk usage monitoring:**
- `syscall.Statfs` on each mounted volume (`/movies`, `/tv`, `/downloads`, `/processing`)
- Track historical usage in a ring buffer (JSON file, last 30 days of hourly samples)
- Calculate: current usage, growth rate (GB/day), estimated time-to-full
- Alert thresholds: warn at 85%, critical at 95%

**Tiered storage (optional, configured):**
```json
{
  "enabled": true,
  "hot_path": "/processing",
  "cold_path": "/movies",
  "move_after_hours": 1
}
```
Files land in hot tier during processing, then move to cold tier when complete. Only relevant if the user has separate volumes (e.g., SSD + HDD on NAS).

**Retention policies (optional):**
```json
{
  "enabled": false,
  "watched_after_days": 90,
  "min_free_gb": 100,
  "protected_tags": ["favorite", "keep"]
}
```
When enabled and disk is low: query Jellyfin API for watched status, delete files watched more than N days ago (unless tagged). Radarr/Sonarr are notified so they unmonitor the item. **Disabled by default** — destructive, requires explicit opt-in.

**Deduplication detection:**
- Scan library for same title+year with multiple quality files
- Report via API (dashboard shows "3 movies have multiple copies, 12GB reclaimable")
- No auto-delete — user confirms via dashboard

## Dashboard Integration

Pipeline visibility is consolidated into the main dashboard at `http://localhost:7354/`:

- **Jobs tab** — shows all pipeline jobs with stage badges (Validating / Transcoding 45% / Cataloging / Done), progress bars (purple for processing, green for done, red for failed), stage details, time elapsed, and retry/cancel actions. The in-page job drawer surfaces validation checks, file info, transcode details, and a job timeline.
- **Settings tab** — pipeline toggles (transcoding, dual subtitles), subtitle language settings, notification mode, and download defaults. Includes a per-profile enable/disable toggle for transcode profiles.
- **Storage monitoring** — per-volume usage bars with growth rate and time-to-full estimates appear in the main dashboard lane.

The dashboard polls `GET /api/pelicula/processing` on the same 15-second refresh cycle.

## Bazarr Integration

Bazarr runs as a standard stack container (port 6767, proxied at `/bazarr`). On startup, `middleware/autowire.go` reads the API key from `$CONFIG_DIR/bazarr/config/config.yaml` (under `auth.apikey`) and issues a single form-encoded `POST /api/system/settings` that wires Sonarr and Radarr (API keys, hosts, sync intervals), enables both sources, and installs a language profile named `Pelicula` built from `PELICULA_SUB_LANGS` (set via the Settings UI → Subtitles). Bazarr's REST layer is Flask-RESTx and reads `request.form`, so every mutation must be `application/x-www-form-urlencoded` — settings keys follow the `settings-<section>-<field>` shape and language profiles are passed as a JSON-encoded list in the `languages-profiles` form field (there is no standalone profile CRUD endpoint). Bazarr only registers its `wanted_search_missing_subtitles_*` scheduled tasks when `use_sonarr`/`use_radarr` are true, so this wiring is load-bearing for the whole subtitle pipeline.

After the `catalog` stage, Procula scans each imported file for embedded subtitle streams against `PELICULA_SUB_LANGS`; missing codes are stored in `missing_subs` and surfaced in the dashboard job card. For each missing language, Procula issues `PATCH /api/movies/subtitles` (or `/api/episodes/subtitles`) to Bazarr, which triggers an immediate per-language search (`POST` on that path is the file-upload endpoint and is intentionally not used). The library resub button in the dashboard uses the same path for files not tied to an active Procula job, falling back to the full `PELICULA_SUB_LANGS` list when the synthetic job has no `missing_subs`.

## nginx additions

```nginx
location /api/procula/ {
    proxy_pass http://procula:8282;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}

location /bazarr {
    proxy_pass http://bazarr:6767;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;
}
```

## File Structure

(abbreviated — not exhaustive)

```
procula/
  Dockerfile
  go.mod
  cmd/
    procula/
      main.go           # Entry point: HTTP server startup, route registration
  main.go               # Package-level HTTP route wiring
  db.go                 # SQLite schema, migration, job/settings CRUD
  queue.go              # Job queue: create, list, update (backed by db.go)
  pipeline.go           # Stage orchestration: validate -> process -> catalog
  validate.go           # FFprobe checks, sample detection, duration sanity
  process.go            # FFmpeg transcoding, extraction, audio normalization
  catalog.go            # Jellyfin refresh (via pelicula-api), notifications
  storage.go            # Disk monitoring, tiered storage, retention, dedup detection
  profiles.go           # Transcode profile CRUD
  dualsub.go            # Dual-subtitle ASS sidecar generation
  settings.go           # Procula settings (SQLite-backed)
  actions.go            # Action bus handler registrations
  events.go             # Pipeline event log (paginated JSON, not SSE)
  libraries.go          # Library management
  updates.go            # Update check
```

## Job Schema

```json
{
  "id": "job_1712345678_abc",
  "created_at": "2026-04-04T12:00:00Z",
  "updated_at": "2026-04-04T12:05:30Z",
  "state": "processing",
  "stage": "transcode",
  "progress": 0.45,
  "source": {
    "type": "movie",
    "title": "The Voice of Hind Rajab",
    "year": 2025,
    "path": "/movies/The Voice of Hind Rajab (2025)/The.Voice.of.Hind.Rajab.2025.2160p.mkv",
    "size": 3102337546,
    "arr_id": 1,
    "arr_type": "radarr",
    "download_hash": "fc927e50ffc31deebc5835a3c5876fb0e615daa6",
    "expected_runtime_minutes": 89
  },
  "validation": {
    "passed": true,
    "checks": {
      "integrity": "pass",
      "duration": "pass",
      "sample": "pass",
      "codecs": {"video": "hevc", "audio": "eac3", "subtitles": ["eng", "ara"]}
    }
  },
  "processing": {
    "profiles_applied": ["mobile-1080p"],
    "outputs": [
      {"profile": "mobile-1080p", "path": "/movies/.../movie-mobile.mkv", "size": 1500000000}
    ]
  },
  "catalog": {
    "jellyfin_synced": true,
    "notification_sent": true
  },
  "error": null,
  "retry_count": 0
}
```

## Dual Subtitles

### Relationship to Bazarr

Dual subtitles is a **post-Bazarr** stage. The typical flow is: Bazarr acquires a `.es.srt` sidecar → Procula's dualsub stage stacks it with the existing English track into `Movie.en-es.ass`. Argos Translate (`DUALSUB_TRANSLATOR=argos`) is a fallback for when no secondary track is available after Bazarr's pass — human-authored subtitles are preferred. If `DUALSUB_TRANSLATOR=none` (the default), the stage skips silently when the secondary track is missing rather than machine-translating.

### Overview

Procula can generate **stacked dual-language subtitle files** (`.en-es.ass`) alongside any media file. These ASS sidecar files are automatically picked up by Jellyfin as an external subtitle track that works on every client — web, mobile, and TV — with no plugin or player changes needed.

### How it works

For each configured language pair (e.g. `en-es`), Procula:

1. Finds the base-language subtitle track — first by checking embedded streams in the media file, then by looking for a sidecar `.{lang}.srt` or `.{lang}.ass` next to it.
2. Finds (or generates) the secondary-language track the same way. If neither an embedded stream nor a sidecar exists, falls back to translating the base track cue-by-cue using Argos Translate.
3. Aligns the two tracks: for each base-language cue, the secondary cue whose midpoint falls within the base cue's time range is matched to it.
4. Writes a stacked ASS file where the base language appears at the **bottom in white** (`{\an2}`) and the secondary language appears at the **top in yellow** (`{\an8}`).

The sidecar is written atomically (`.partial` → final rename) and is idempotent: if the output file already exists and is newer than the source media, the stage skips silently.

### Language pairs and output filenames

`DUALSUB_PAIRS=en-es,en-de` produces:
- `Movie.en-es.ass` — English bottom, Spanish top
- `Movie.en-de.ass` — English bottom, German top

The first language in each pair is the familiar one (bottom); the second is the learning target (top).

### Supported subtitle codecs

Embedded tracks must be text-based to be extractable: `subrip`, `ass`, `ssa`, `webvtt`, `mov_text`, `text`. Bitmap tracks (PGS `hdmv_pgs_subtitle`, DVD `dvd_subtitle`) are silently skipped — they require OCR and are not currently supported.

### Translator setup (Argos Translate)

When a secondary-language track is not available, Procula can call [Argos Translate](https://github.com/argosopentech/argos-translate) to synthesize it. Argos runs fully offline — nothing is sent to an external service.

Argos Translate is **not bundled in the Procula Docker image** by default. To enable it:

```bash
# Inside the procula container (or add to Dockerfile):
pip install argostranslate
python3 -c "import argostranslate.package; argostranslate.package.update_package_index(); \
  pkgs = argostranslate.package.get_available_packages(); \
  [p.install() for p in pkgs if p.from_code=='en' and p.to_code=='es']"
```

Models are ~200MB per language pair. Mount a persistent volume at `/root/.local/share/argos-translate/` so models survive container restarts.

Set `DUALSUB_TRANSLATOR=argos` in `.env` (or via the Procula settings UI) to activate.

### Translation cache

Translated cues are cached by SHA-256 of `(fromLang, toLang, text)` in `/config/procula/dualsub-cache/`. Re-processing the same title skips already-translated cues. The cache is not invalidated when the Argos model version changes — delete the cache dir manually if you upgrade models and want fresh translations.

### Configuration

| Env var | Default | Notes |
|---|---|---|
| `DUALSUB_ENABLED` | `false` | Set `true` to enable the stage |
| `DUALSUB_PAIRS` | `en-es` | Comma-separated list of `base-secondary` pairs |
| `DUALSUB_TRANSLATOR` | `none` | `argos` or `none` |

All settings are also exposed in the Procula dashboard under **Settings → Dual Subtitles**.

### Known limitations

- Timing alignment is base-language-anchored: secondary cues are matched by midpoint containment within the base cue's range. Fast-dialogue scenes with very short, overlapping cues may not align perfectly.
- Font name in generated ASS is `Arial`. Jellyfin's libass will substitute its fallback font if Arial is not available; results are visually acceptable for Latin and CJK scripts. Arabic RTL rendering is not tested.
- Per-title opt-out is not yet implemented. Enable/disable is stack-wide.
- Forced/SDH subtitle tracks (`.en.forced.srt`, `.en.sdh.srt`) are not auto-detected as sidecars.

## Config Files (created in /config/procula/)

```
/config/procula/
  procula.db               # SQLite database: jobs, settings, catalog_flags, dualsub_profiles, blocked_releases, notifications, migrated_json_files (schema kept for history; the migration code that populated it has been removed — see the Historical note above)
  profiles/                # Transcode profile JSON files
  dualsub-cache/           # Translator cue cache (SHA-256-keyed .txt files)
  notifications.json       # Webhook URLs and preferences
```
