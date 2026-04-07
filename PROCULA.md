# Procula — Media Processing Pipeline

Procula is the processing and storage management service for the Pelicula media stack. It handles everything between "download complete" and "ready to watch": import tracking, file validation, transcoding, subtitle acquisition, catalog updates, and storage lifecycle management.

## Architecture

```
Radarr/Sonarr                   pelicula-api                    Procula (:8282)
  |                                 |                               |
  |-- import webhook -------------->|-- POST /api/procula/jobs ---->|
  |                                 |                               |
  |                                 |   queue + persist (JSON files)|
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

Radarr and Sonarr fire **Connect** webhooks on import. The middleware receives these at a new endpoint (`POST /api/pelicula/hooks/import`), normalizes the payload, and forwards it to Procula's job queue (`POST /api/procula/jobs`). This keeps Procula decoupled — it never talks to the *arr apps directly. The middleware remains the single coordination point.

### Service layout

| Service | Port | Role |
|---------|------|------|
| `procula` | 8282 | Job queue, pipeline orchestration, FFmpeg processing, storage management |
| `pelicula-api` | 8181 | Receives webhooks, forwards jobs, serves dashboard data, proxies Procula status |

Procula runs as a single Go binary with FFmpeg installed in the container image (Alpine + FFmpeg). No external Go dependencies (stdlib only).

Bazarr (subtitle acquisition) is planned but not yet shipped — see ROADMAP.md.

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

**Queue implementation:** One JSON file per job in `/config/procula/jobs/`. No SQLite dependency, stays stdlib-only, matches the middleware pattern. SQLite was considered (CGO_ENABLED=1, `modernc.org/sqlite` or `mattn/go-sqlite3`) but rejected for the initial implementation — JSON files are simpler, inspectable with `ls`/`cat`, and the single-goroutine worker means there's no lock contention. See ROADMAP.md "Procula queue" for revisit criteria (cross-job analytics, second worker, or high job volume).

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
- If none found for configured languages, flag for Bazarr (planned — see ROADMAP.md); Bazarr auto-discovers from Sonarr/Radarr via its own polling

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
+-- Services (add Procula card; Bazarr card planned)
```

The dashboard polls `GET /api/pelicula/processing` on the same 15-second refresh cycle.

## Bazarr Integration *(Planned — not yet shipped)*

Add Bazarr to docker-compose.yml:

```yaml
bazarr:
  image: lscr.io/linuxserver/bazarr:latest
  container_name: bazarr
  restart: unless-stopped
  environment:
    - PUID=${PUID}
    - PGID=${PGID}
    - TZ=${TZ}
  volumes:
    - ${CONFIG_DIR}/bazarr:/config
    - ${MEDIA_DIR}/movies:/movies
    - ${MEDIA_DIR}/tv:/tv
  healthcheck:
    test: ["CMD", "wget", "--spider", "-q", "http://localhost:6767/bazarr/ping"]
    interval: 30s
    timeout: 10s
    retries: 3
```

Auto-wire in middleware: connect Bazarr to Sonarr and Radarr (similar to Prowlarr wiring). Seed config with `UrlBase: /bazarr`.

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
  main.go          # HTTP server, route registration
  queue.go         # Job queue: create, list, update, persist to JSON files
  pipeline.go      # Stage orchestration: validate -> process -> catalog
  validate.go      # FFprobe checks, sample detection, duration sanity
  process.go       # FFmpeg transcoding, extraction, audio normalization
  catalog.go       # Jellyfin refresh, verification, notifications
  storage.go       # Disk monitoring, tiered storage, retention, dedup detection
  profiles.go      # Transcode profile CRUD
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

## Implementation Order

Build in this order — each stage is independently useful:

### Phase 1: Skeleton + Validate (most immediate value)
1. Create `procula/` directory with Dockerfile, go.mod, main.go
2. Implement JSON file job queue (queue.go)
3. Implement validation stage (validate.go) — FFprobe checks
4. Add webhook receiver to pelicula-api (`/api/pelicula/hooks/import`)
5. Add Procula to docker-compose.yml
6. Add nginx proxy rule
7. Wire health check into dashboard service grid
8. **Result:** Bad downloads get auto-blocklisted and re-searched

### Phase 2: Storage monitoring (quick win, high visibility)
1. Implement storage.go — disk usage, growth rate, time-to-full
2. Add storage endpoint to Procula API
3. Add storage bar widget to dashboard
4. **Result:** Users see disk usage at a glance, get warned before running out

### Phase 3: Catalog + Notifications
1. Implement catalog.go — Jellyfin refresh + verification
2. Add notification support (webhook POST to configured URLs)
3. Add "Processing" section to dashboard with job cards
4. Proxy Procula status through pelicula-api
5. **Result:** Users get notified when content is ready; dashboard shows pipeline status

### Phase 4: Subtitles (Bazarr) *(Planned)*
1. Add Bazarr to docker-compose.yml
2. Seed Bazarr config (UrlBase)
3. Auto-wire Bazarr to Sonarr/Radarr in middleware
4. Add nginx proxy rule
5. Add to dashboard service grid
6. Subtitle check in Procula validation stage
7. **Result:** Subtitles auto-download for all content

### Phase 5: Transcoding
1. Implement process.go — FFmpeg invocation with progress tracking
2. Implement profiles.go — profile CRUD API
3. Add profile management to dashboard (or document API-only for now)
4. Scratch volume for processing (`/processing`)
5. **Result:** Multi-quality library from single downloads

### Phase 6: Advanced storage (retention, dedup, tiering)
1. Retention policies with Jellyfin watched-status integration
2. Dedup detection and reporting
3. Tiered storage moves
4. **Result:** Storage manages itself; library stays clean

## Config Files (created in /config/procula/)

```
/config/procula/
  jobs/                    # One JSON file per job
  profiles/                # Transcode profile JSON files
  notifications.json       # Webhook URLs and preferences
  storage.json             # Retention policies, tier config, alert thresholds
  history.json             # Ring buffer of hourly disk usage samples
```
