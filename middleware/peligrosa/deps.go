package peligrosa

import (
	"database/sql"

	"pelicula-api/clients"
)

// Deps bundles the dependencies needed by handlers that span
// multiple peligrosa-scope stores. It is constructed once in main() and
// its methods serve as http.Handlers.
type Deps struct {
	DB       *sql.DB
	Auth     *Auth
	Invites  *InviteStore
	Requests *RequestStore
	Jellyfin clients.JellyfinClient
	// Notify is called to send user-facing notifications (e.g. Apprise).
	// If nil, notifications are silently skipped.
	Notify func(title, body string)
	// GenPassword is called by HandleGeneratePassword to produce a passphrase.
	// If nil, a random hex string is returned instead.
	GenPassword func() string
}

// NewDeps creates a Deps from the given components.
func NewDeps(db *sql.DB, auth *Auth, invites *InviteStore, requests *RequestStore, jf clients.JellyfinClient) *Deps {
	return &Deps{DB: db, Auth: auth, Invites: invites, Requests: requests, Jellyfin: jf}
}

// notify calls d.Notify if set, otherwise is a no-op.
func (d *Deps) notify(title, body string) {
	if d.Notify != nil {
		d.Notify(title, body)
	}
}

// genPassword calls d.GenPassword if set, otherwise returns a random hex token.
func (d *Deps) genPassword() string {
	if d.GenPassword != nil {
		return d.GenPassword()
	}
	// Fallback: 16 random hex bytes
	token, _ := generateToken()
	return token[:16]
}
