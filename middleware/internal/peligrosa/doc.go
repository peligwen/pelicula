// Package peligrosa is the user-interaction safety layer for pelicula-api.
// It owns authentication, session management, login rate limiting, loopback
// auto-session, open registration, CSRF origin guards, invite token lifecycle,
// the viewer request queue, and role mapping (viewer / manager / admin).
//
// The package enforces the trust boundary between the operator (the admin
// running the box, who is implicitly trusted) and external callers — LAN
// clients who must authenticate normally, and remote viewers arriving via the
// Peligrosa remote nginx vhost (whose role is capped to viewer regardless of
// stored credentials).
//
// Sessions and rate-limit counters are persisted to SQLite (pelicula.db) so
// they survive middleware restarts.
//
// For the full threat model see docs/PELIGROSA.md.
package peligrosa
