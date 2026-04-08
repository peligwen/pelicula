# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

---

## [Unreleased]

### Added
- **Bazarr** — automatic subtitle acquisition from OpenSubtitles, Addic7ed, Podnapisi, and others. Wired to Sonarr and Radarr automatically on startup. Language profile created from `PELICULA_SUB_LANGS`. Procula flags imports missing subtitles for the configured languages.
- **`./pelicula configure` → Subtitles** — new menu option (9) to set `PELICULA_SUB_LANGS` (comma-separated ISO 639-1 codes, e.g. `en,es`). Drives both the Bazarr language profile and the Procula missing-subs check.
- **Dual subtitles** (`procula`) — new pipeline stage that generates stacked ASS sidecar files (e.g. `Movie.en-es.ass`) for language learners. Base language appears bottom-center in white; learning language appears top-center in yellow. Configure via `DUALSUB_ENABLED` / `DUALSUB_PAIRS` / `DUALSUB_TRANSLATOR` or the Procula settings UI. Argos Translate (offline) is supported as a fallback translator when no secondary track exists. Known limitations: no bitmap (PGS/DVD) sub support; per-title opt-out not yet implemented.
- **Invite flow** — shareable one-time invite links for admin-free user onboarding. Admins create links from the dashboard Users section; recipients choose a username and password at `/register`. Configurable TTL (default 7 days) and max-uses cap. Expired/exhausted/revoked tokens return clear error states.
- **Jellyfin SSO** (`PELICULA_AUTH=jellyfin`) — credentials verified against Jellyfin's `/Users/AuthenticateByName`; roles stored in `roles.json`; Jellyfin admins auto-promoted.
- **Central CSRF middleware** — `requireLocalOriginStrict` / `requireLocalOriginSoft` wired per-route, replacing per-handler inline checks.

---

## [0.1.0] — TBD

> Fill in the release date and finalize this section when tagging v0.1.0.
> Summarise the full v0.1 scope here.
