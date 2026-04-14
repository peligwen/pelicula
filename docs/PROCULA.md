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

**Webhook authentication:** The import hook is protected by a shared secret (`WEBHOOK_SECRET` in `.env`), appended as `?secret=<value>` to the autowired Sonarr/Radarr webhook URL. The nginx config also restricts the route to the Docker internal network (172.16.0.0/12). Existing installs without `WEBHOOK_SECRET` in `.env` continue to work (the check is skipped when the env var is unset).

### Service layout

| Service | Port | Role |
|---------|------|------|
| `procula` | 8282 | Job queue, pipeline orchestration, FFmpeg processing, storage management |
| `pelicula-api` | 8181 | Receives webhooks, forwards jobs, serves dashboard data, proxies Procula status |

Procula runs as a single Go binary with FFmpeg installed in the container image (Alpine + FFmpeg). No external Go dependencies (stdlib only).

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
  volumes:
    - ${CONFIG_DIR}/procula:/config           # job state, transcode profiles
    - ${MEDIA_DIR}/downloads:/downloads       # Read: completed downloads
    - ${MEDIA_DIR}/movies:/movies             # Read/write: movie library
    - ${MEDIA_DIR}/tv:/tv                     # Read/write: TV library
    - ${MEDIA_DIR}/processing:/processing     # Scratch space for transcoding
  healthcheck:
    test: ["CMD", "wget", "--spider", "-q", "http://localhost:8282/ping"]
    interval: 30s
    timeout: 10s
    retries: 3
```

### Dockerfile

```dockerfile
FROM golang:1.23-alpine AS build
RUN apk add --no-cache gcc musl-dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=1 go build -o procula .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates ffmpeg ffprobe
COPY --from=build /app/procula /usr/local/bin/
EXPOSE 8282
CMD ["procula"]
```

**Queue implementation:** Jobs are persisted in SQLite (`procula.db`, tables: `jobs`, `settings`) via `modernc.org/sqlite` (pure-Go driver, no CGO). On first startup, any existing JSON job files in `/config/procula/jobs/` are migrated automatically (idempotent). The single-goroutine worker means there is no lock contention beyond standard SQLite serialized writes.

## API Endpoints

### Procula service (port 8282)

```
GET  /ping                          Health check
GET  /api/procula/status            Queue summary: pending, processing, completed, failed counts
GET  /api/procula/jobs              List jobs with state, progress, stage
GET  /api/procula/jobs/:id          Single job detail
POST /api/procula/jobs              Create job (called by pelicula-api)
POST /api/procula/jobs/:id/retry    Retry a failed job
POST /api/procula/jobs/:id/cancel   Cancel a running/pending job
GET  /api/procula/storage           Disk usage per volume, growth rate, time-to-full
GET  /api/procula/profiles          List transcode profiles
POST /api/procula/profiles          Create/update transcode profile
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
| **Archive detection** | Check for RAR/ZIP containers | Queue extraction before further processing |

On validation failure:
1. Procula calls `POST /api/pelicula/downloads/cancel` with `blocklist: true` and the appropriate reason
2. The middleware handles the *arr blocklist + unmonitor
3. The watcher (already exists) picks up the still-missing item and triggers a new search
4. **File deletion:** Only if `delete_on_failure: true` is set in Procula settings, **and** the file path is under `/downloads` or `/processing`. Paths under `/movies` or `/tv` are never deleted — those are already-imported files and removing them in response to a re-queued job would be destructive.

### Stage 2: Process

Runs after validation passes. Heavy work happens here.

**2a. Extract (if needed)**
- Unpack RAR/ZIP archives to `/processing/`
- Move extracted media file to the library path
- Clean up archive files

**2b. Transcode (if profile matches)**
Transcode profiles are stored in `/config/procula/profiles/` as JSON:

```json
{
  "name": "mobile-1080p",
  "enabled": true,
  "conditions": {
    "min_height": 2160,
    "codecs_include": ["hevc", "h265"]
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

A profile's `conditions` determine which files it applies to. A file can match multiple profiles, producing multiple output files (e.g., keep the 4K master and also produce a 1080p copy).

FFmpeg invocation is shelled out from Go:
```
ffmpeg -i input.mkv -c:v libx264 -preset medium -crf 20 -vf scale=-2:1080 \
       -c:a aac -ac 2 -c:s copy -movflags +faststart output-mobile.mkv
```

Progress tracking: parse FFmpeg's stderr for `frame=` / `time=` / `speed=` to report percentage.

**2c. Audio normalization (optional per profile)**
```
ffmpeg -i input.mkv -af loudnorm=I=-14:TP=-2:LRA=7 ...
```

**2d. Subtitle handling**
- Detect embedded subtitle tracks via FFprobe
- If none found for configured languages, flags them in `missing_subs`; the Await Subs stage then kicks Bazarr and waits for sidecars

### Stage 3: Catalog

After processing completes:

1. **Trigger Jellyfin scan** — `POST /jellyfin/Library/Refresh` (Jellyfin API)
2. **Verify Jellyfin picked it up** — poll `GET /jellyfin/Items?searchTerm=...` until the item appears (timeout after 60s)
3. **Update job record** — mark as completed with metadata (final file paths, sizes, codecs, duration)
4. **Notification** (if configured) — webhook to user-configured URL with payload:
   ```json
   {
     "event": "ready",
     "title": "The Voice of Hind Rajab",
     "year": 2025,
     "quality": "2160p",
     "type": "movie",
     "url": "http://localhost:7354/jellyfin/..."
   }
   ```
   Supports: Slack webhook, Discord webhook, Gotify, generic HTTP POST. Configured via `/config/procula/notifications.json`.

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

Add a **Processing** section between Downloads and Services:

```
+-- Search
+-- Downloads
+-- Processing (NEW)
|   +-- section-header: "Processing" | "X queued / Y active / Z completed today"
|   +-- pipeline items (same card style as downloads):
|       +-- name + stage badge (Validating / Transcoding 45% / Cataloging / Done)
|       +-- progress bar (purple for processing, green for done, red for failed)
|       +-- meta: stage details, time elapsed
|       +-- actions: retry (on failed), cancel
|   +-- storage bar (if storage monitoring enabled):
|       +-- "Movies: 1.2 TB / 4 TB" with fill bar
|       +-- "TV: 800 GB / 4 TB" with fill bar
|       +-- growth rate + time-to-full estimate
+-- Services (Procula card, Bazarr card)
```

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

```
procula/
  Dockerfile
  go.mod
  main.go               # HTTP server, route registration
  db.go                 # SQLite schema, migration, job/settings CRUD
  db_test.go
  migrate_json.go       # One-time migration from legacy JSON job files
  migrate_json_test.go
  queue.go              # Job queue: create, list, update (backed by db.go)
  pipeline.go           # Stage orchestration: validate -> process -> catalog
  validate.go           # FFprobe checks, sample detection, duration sanity
  process.go            # FFmpeg transcoding, extraction, audio normalization
  catalog.go            # Jellyfin refresh, verification, notifications
  storage.go            # Disk monitoring, tiered storage, retention, dedup detection
  profiles.go           # Transcode profile CRUD
  dualsub.go            # Dual-subtitle ASS sidecar generation
  dualsub_test.go
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
  jobs/                    # Legacy — migrated to SQLite (procula.db)
  profiles/                # Transcode profile JSON files
  dualsub-cache/           # Translator cue cache (SHA-256-keyed .txt files)
  notifications.json       # Webhook URLs and preferences
  storage.json             # Retention policies, tier config, alert thresholds
  history.json             # Ring buffer of hourly disk usage samples
```
