package main

import "database/sql"

// peligrosaDeps bundles the dependencies needed by handlers that span
// multiple peligrosa-scope stores. It is constructed once in main() and
// its methods serve as http.Handlers. Moving this file (and the peligrosa-
// destined handlers) into middleware/peligrosa/ in Task 6 is the final
// extraction step; at that point this type becomes peligrosa.Deps and
// exported methods become the subpackage's public API.
type peligrosaDeps struct {
	DB       *sql.DB
	Auth     *Auth
	Invites  *InviteStore
	Requests *RequestStore
	Jellyfin JellyfinClient
}

func newPeligrosaDeps(db *sql.DB, auth *Auth, invites *InviteStore, requests *RequestStore, jf JellyfinClient) *peligrosaDeps {
	return &peligrosaDeps{DB: db, Auth: auth, Invites: invites, Requests: requests, Jellyfin: jf}
}
