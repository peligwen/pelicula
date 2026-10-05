# API

All routes are served by the `pelicula` server on `:8181` and reached through nginx at `http://<host>:7354/api/...`. Everything is JSON. Source of truth for behaviour is `internal/auth` and `internal/api`; this file mirrors the contract they implement.

Conventions:

- **Auth.** A session cookie, `pelicula_session` (HttpOnly, SameSite=Lax), set by login or register. "Min role" is the lowest role allowed: viewer < manager < admin. No session is a 401; a role that is too low is a 403.
- **CSRF.** For POST, PUT, PATCH and DELETE, a present `Origin` (else `Referer`) header must match the request host, otherwise 403. Requests with neither header pass.
- **Errors.** Every error is `{"error":"message"}` with a status that matches the cause: 400 bad input, 401 no session or bad credentials, 403 role too low or CSRF, 404 missing, 409 conflict, 410 gone, 413 body too large, 500 server error, 502 an upstream (Radarr, Sonarr) did not answer, 503 dependency unavailable. One error carries extra data: a duplicate request is `409 {"error":"already requested","request":{...}}`.
- **Timestamps** are RFC 3339 (UTC).
- **Rate limits** on login, register and the invite lookup are applied by nginx, not the server: 10 requests per minute per IP across the three, excess answered with `429` (not the JSON error format). See [ARCHITECTURE.md](ARCHITECTURE.md#nginx-route-map).

## Auth routes

| Method and path | Min role | Request | Response |
|---|---|---|---|
| `POST /api/auth/login` | public | `{"username","password"}` | `200 {"username","role"}` and a session cookie. 401 bad credentials; 503 Jellyfin unreachable. Role is the stored role, else `admin` for a Jellyfin administrator, else `viewer` (and the default is saved). |
| `POST /api/auth/logout` | any | none | `204`; clears the session |
| `GET /api/auth/me` | any | none | `200 {"username","role"}` or 401 |
| `GET /api/auth/check` | any | none | `204` with a valid session cookie, else 401. Used by nginx `auth_request`. |
| `GET /api/register/{code}` | public | none | `200 {"valid":bool,"role":"viewer"}`. `valid` means the invite exists, is unused and is unexpired. |
| `POST /api/register` | public | `{"code","username","password"}` | `201 {"username","role"}` and a session cookie. Username must match `^[A-Za-z0-9._-]{3,32}$`, password at least 8 characters (400 otherwise). 409 if the Jellyfin user exists; 410 if the invite is unavailable. |
| `GET /api/invites` | admin | none | `200 {"invites":[{"code","role","created_by","created_at","expires_at","used_by","used_at"}]}` |
| `POST /api/invites` | admin | `{"role","expires_hours"}`; role defaults to `viewer`, hours to 72, maximum 720 | `201 {"code","role","expires_at","path":"/register?code=<code>"}`. The code is 16 random bytes, base64url. |
| `DELETE /api/invites/{code}` | admin | none | `204`, or 404 |

## API routes

| Method and path | Min role | Request | Response |
|---|---|---|---|
| `GET /api/health` | public | none | `{"ok":true,"wired":bool,"version":"..."}`. `wired` turns true when autowire has finished. |
| `GET /api/status` | viewer | none | `{"services":[{"name","ok","path"}],"vpn":{"enabled","tunnel","public_ip","country","forwarded_port"},"wired","version","pending_requests","queued_jobs"}`. Services are sonarr (`/sonarr/`), radarr, jellyfin, plus prowlarr (`/prowlarr/`) and qbittorrent (`/qbt/`) when the VPN is on. Each is pinged with a 2 s timeout, in parallel, cached for 5 s. `pending_requests` is 0 unless the caller is a manager or above. |
| `GET /api/search?q=` | viewer | query `q` (empty is 400); 502 when both Radarr and Sonarr lookups fail | `{"results":[{"type":"movie"\|"series","title","year","overview","poster","tmdb_id","tvdb_id","in_library","arr_id","has_file"}]}`. Radarr and Sonarr lookups run in parallel and are interleaved movie, series, movie, ... with a maximum of 40. `in_library` is true when the lookup result has a non-zero `id`; `has_file` comes from `hasFile` (movie) or `statistics.episodeFileCount > 0` (series). |
| `POST /api/search/add` | manager | `{"type","tmdb_id","tvdb_id"}` | `{"arr_id":N}`. Adds the title to Radarr or Sonarr and starts a search. |
| `GET /api/requests` | viewer | none | `{"requests":[Request]}`. Viewers get their own; managers and admins get all. |
| `POST /api/requests` | viewer | `{"type","tmdb_id","tvdb_id","title","year","poster"}` | `201 Request`. 409 duplicate (see Errors). If the `auto_approve_requests` setting is true the title is added immediately and the request is stored `approved` with `decided_by` `"auto"`. |
| `POST /api/requests/{id}/approve` | manager | none | `200 Request` with `status` `approved`, `decided_by` the approver and `arr_id` set. Adds the title to Radarr or Sonarr. |
| `POST /api/requests/{id}/decline` | manager | `{"note"}` | `200 Request` with `status` `declined` |
| `GET /api/downloads` | viewer | none | `{"vpn":bool,"downloads":[{"hash","name","state","progress","dlspeed","upspeed","eta","size","category"}],"transfer":{"dl_speed","up_speed"}}`. Without the VPN: `{"vpn":false,"downloads":[],"transfer":{zeros}}`. |
| `POST /api/downloads/{hash}/pause` | manager | none | `204` (qBittorrent v5 `stop`) |
| `POST /api/downloads/{hash}/resume` | manager | none | `204` (qBittorrent v5 `start`) |
| `DELETE /api/downloads/{hash}?blocklist=true` | admin | optional `blocklist` query | `204`. Finds the Radarr then Sonarr queue record whose `downloadId` equals the hash (case-insensitive) and deletes it with `removeFromClient=true` and the given blocklist flag; if none matches, deletes the torrent and its files from qBittorrent. |
| `GET /api/jobs?limit=50` | viewer | `limit` optional | `{"jobs":[Job]}`, newest first |
| `POST /api/jobs/{id}/retry` | manager | none | `204`; requeues a finished job and wakes the worker. 404 unknown job; 409 if the job is still `queued` or `running`. |
| `GET /api/settings` | admin | none | `{"settings":{"validation_enabled":"true",...},"info":{"config_dir","library_dir","work_dir","server_countries","vpn_enabled","version","tz"}}` |
| `PUT /api/settings` | admin | `{"<key>":"true"\|"false",...}`; keys must be in `store.SettingDefaults` and values must parse as booleans (400 otherwise) | `200` the full settings map as a bare object, e.g. `{"validation_enabled":"true",...}` (not wrapped in `settings`) |
| `GET /api/users` | admin | none | `{"users":[{"id","name","is_admin","is_disabled","role","last_login"}]}`: Jellyfin's users joined with the `roles` table (default role `admin` if `is_admin`, else `viewer`) |
| `PUT /api/users/{username}/role` | admin | `{"role"}` | `204`. 400 when changing your own role or an invalid role. |
| `DELETE /api/users/{username}` | admin | none | `204`. 400 when deleting yourself. Deletes the Jellyfin user, the role row and the user's sessions. |
| `POST /api/hooks/import` | none, secret | Sonarr or Radarr webhook; see below | see below |

Settings keys:

| Key | Default | Meaning |
|---|---|---|
| `validation_enabled` | `true` | run the ffprobe check on import |
| `auto_blocklist` | `true` | on a failed check, mark the release failed in *arr (blocklist), delete the bad file via the *arr API and search again |
| `auto_approve_requests` | `false` | viewer requests are added without a manager's approval |

### Request

`store.Request`, as returned by `/api/requests`:

```json
{
  "id": 7,
  "media_type": "movie",
  "tmdb_id": 603,
  "tvdb_id": 0,
  "title": "The Matrix",
  "year": 1999,
  "poster": "https://...",
  "requested_by": "alice",
  "status": "approved",
  "arr_id": 12,
  "decided_by": "admin",
  "note": "",
  "created_at": "2026-01-01T12:00:00Z",
  "updated_at": "2026-01-01T12:05:00Z"
}
```

`media_type` is `movie` or `series`. Status is `pending`, `approved`, `declined` or `available`. `tmdb_id`, `tvdb_id`, `year`, `poster`, `arr_id`, `decided_by` and `note` are omitted when empty or zero. Note that the request body of `POST /api/requests` and `POST /api/search/add` names the media type `type`, while the stored request and the response use `media_type`. A pass in the pipeline flips `approved` requests for that *arr item to `available`.

### Job

`store.Job`, as returned by `/api/jobs`:

```json
{
  "id": 3,
  "arr_type": "radarr",
  "arr_id": 12,
  "episode_id": 0,
  "title": "The Matrix",
  "path": "/media/movies/The Matrix (1999)/The Matrix (1999).mkv",
  "size": 8123456789,
  "download_id": "ABCDEF0123",
  "runtime_min": 136,
  "status": "failed",
  "result": "{\"passed\":false,...}",
  "error": "no video stream",
  "attempts": 1,
  "created_at": "2026-01-01T12:00:00Z",
  "started_at": "2026-01-01T12:00:01Z",
  "finished_at": "2026-01-01T12:00:03Z"
}
```

`status` is `queued`, `running`, `passed` or `failed`. `episode_id`, `download_id`, `runtime_min`, `result`, `error`, `started_at` and `finished_at` are omitted when empty. `result` is a **string containing JSON** (parse it client-side) once the pipeline has finished the job, with the shape below. `error` holds the failure reason for a failed job.

#### Job `result` JSON

Written by `pipeline.Result`:

```json
{
  "passed": true,
  "skipped": false,
  "integrity": "pass",
  "sample": "pass",
  "duration": "warn",
  "video": "h264",
  "audio": ["aac(eng)", "ac3(spa)"],
  "subtitles": ["eng", "spa"],
  "width": 1920,
  "height": 1080,
  "duration_sec": 8160.4,
  "reason": ""
}
```

| Field | Type | Meaning |
|---|---|---|
| `passed` | bool | overall outcome |
| `skipped` | bool, omitted when false | validation was disabled; the job passed without checks |
| `integrity` | `pass` \| `fail` \| `skip` | file exists and ffprobe found a video stream |
| `sample` | `pass` \| `fail` \| `skip` | size is not sample-like (at least 50 MB, and at least 3 MB per expected minute when the runtime is known) |
| `duration` | `pass` \| `warn` \| `fail` \| `skip` | within 10% of the expected runtime passes, over 10% warns, over 50% fails, unknown skips |
| `video` | string, omitted if empty | video codec |
| `audio` | string array, omitted if empty | `codec(lang)` per audio track |
| `subtitles` | string array, omitted if empty | subtitle languages |
| `width`, `height` | int, omitted if 0 | video dimensions |
| `duration_sec` | number, omitted if 0 | measured duration |
| `reason` | string, omitted if empty | why it failed |

## Webhook contract

`POST /api/hooks/import` is called by Sonarr and Radarr, not by browsers. nginx only lets loopback and private ranges reach it.

- **Header** `X-Webhook-Secret` must equal the server's `WEBHOOK_SECRET` (constant-time comparison). A wrong or missing secret is rejected with 401. If the server has no secret configured, every call is rejected with 503.
- **Body** is at most 1 MB of JSON (413 above that, 400 if it is not JSON), selected by `eventType` (case-insensitive):
  - `Test`: `200 {"status":"ok"}` (sent by the *arr "Test" button).
  - `Download`: enqueue a job and return `200 {"status":"queued","job_id":N}`. If a queued or running job for the same `path` exists, its id is returned instead of inserting a duplicate.
  - anything else: `200 {"status":"ignored"}`.

A `Download` payload must carry a `movie` (Radarr) or `series` (Sonarr) object with a non-zero `id` and a file with a non-empty `path`; otherwise the call is a 400.

Fields read from a Radarr payload:

| Field | Becomes |
|---|---|
| `movie.id` | `job.arr_id` |
| `movie.title`, `movie.year`, `movie.tmdbId` | `job.title` (the others are informational) |
| `movieFile.path` | `job.path` (must be a path as seen inside the containers, under `/media/movies`) |
| `movieFile.size` | `job.size` |
| `downloadId` | `job.download_id` |

Fields read from a Sonarr payload:

| Field | Becomes |
|---|---|
| `series.id` | `job.arr_id` |
| `series.title`, `series.tvdbId` | `job.title` |
| `episodes[0].id` | `job.episode_id` (first episode only) |
| `episodeFile.path` | `job.path` (under `/media/tv`) |
| `episodeFile.size` | `job.size` |
| `downloadId` | `job.download_id` |

`job.arr_type` is `radarr` or `sonarr`. `job.runtime_min` is looked up best-effort (`GetMovie(id).runtime` or `GetSeriesByID(id).runtime`) and is 0 when the lookup fails. After enqueuing, the worker is woken.

Example (Radarr):

```json
{
  "eventType": "Download",
  "movie": {"id": 12, "title": "The Matrix", "year": 1999, "tmdbId": 603},
  "movieFile": {"path": "/media/movies/The Matrix (1999)/The Matrix (1999).mkv", "size": 8123456789},
  "downloadId": "ABCDEF0123"
}
```

## Environment variables

Read by `cmd/pelicula-server` through `config.FromEnv`. Compose sets them from `.env`; the CLI writes `.env`.

| Variable | Default | Meaning |
|---|---|---|
| `PELICULA_LISTEN` | `:8181` | API listen address |
| `CONFIG_DIR` | `/config` | mounted config root: `pelicula/` (read-write), `sonarr/ radarr/ prowlarr/` (read-only; API keys are read from `config.xml`) |
| `PELICULA_DB` | `$CONFIG_DIR/pelicula/pelicula.db` | SQLite file (override mainly for tests) |
| `SONARR_URL` | `http://sonarr:8989/sonarr` | Sonarr base URL |
| `RADARR_URL` | `http://radarr:7878/radarr` | Radarr base URL |
| `PROWLARR_URL` | `http://gluetun:9696/prowlarr` | only used when `PELICULA_VPN=true` |
| `QBITTORRENT_URL` | `http://gluetun:8080` | only used when `PELICULA_VPN=true`; no auth (subnet whitelist seeded by the CLI) |
| `JELLYFIN_URL` | `http://jellyfin:8096/jellyfin` | Jellyfin base URL |
| `GLUETUN_CONTROL_URL` | `http://gluetun:8000` | gluetun control API, basic auth with the two variables below |
| `GLUETUN_HTTP_USER` | `pelicula` | gluetun control API user |
| `GLUETUN_HTTP_PASS` | | gluetun control API password |
| `PELICULA_URL` | `http://pelicula:8181` | URL Sonarr and Radarr use to call the import webhook |
| `WEBHOOK_SECRET` | | sent by *arr in `X-Webhook-Secret`; empty makes the webhook return 503 |
| `JELLYFIN_ADMIN_USER` | `admin` | Jellyfin admin created by autowire; the server authenticates as it |
| `JELLYFIN_PASSWORD` | | that admin's password |
| `PELICULA_VPN` | `false` | `true` enables the Prowlarr, qBittorrent and gluetun clients and port sync; the CLI exports `true` when `WIREGUARD_PRIVATE_KEY` is set |
| `MOVIES_PATH` | `/media/movies` | movies root folder inside the containers |
| `TV_PATH` | `/media/tv` | TV root folder inside the containers |
| `SERVER_COUNTRIES` | | display only (shown in settings info) |
| `HOST_CONFIG_DIR`, `HOST_LIBRARY_DIR`, `HOST_WORK_DIR` | | display only: the host paths behind the mounts |
| `TZ` | `UTC` | time zone, also shown in settings info |

Variables read by the CLI and compose only (not the server): `PELICULA_PORT` (host port for nginx, default 7354), `PUID`, `PGID`, `LIBRARY_DIR`, `WORK_DIR`, `WIREGUARD_PRIVATE_KEY`, `SERVER_COUNTRIES`, `PELICULA_PROJECT_NAME` (compose project, default `pelicula`), `PELICULA_VERSION` (stamped into the image build). See `.env.example`.
