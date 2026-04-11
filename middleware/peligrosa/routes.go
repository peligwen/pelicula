package peligrosa

import (
	"net/http"

	"pelicula-api/httputil"
)

// RegisterRoutes wires all peligrosa-owned HTTP routes into mux.
// Webhook routes (hooks/import, jellyfin/refresh) stay in the main package
// because their handlers live in middleware/hooks.go.
func RegisterRoutes(mux *http.ServeMux, d *Deps) {
	auth := d.Auth

	// Auth endpoints (always accessible)
	mux.HandleFunc("/api/pelicula/auth/login", auth.HandleLogin)
	mux.HandleFunc("/api/pelicula/auth/logout", auth.HandleLogout)
	mux.HandleFunc("/api/pelicula/auth/check", auth.HandleCheck)

	// viewer+: request queue (list own requests + create)
	mux.Handle("/api/pelicula/requests", auth.Guard(http.HandlerFunc(d.HandleRequests)))
	// admin only: per-request approve/deny/delete
	mux.Handle("/api/pelicula/requests/", auth.GuardAdmin(http.HandlerFunc(d.HandleRequestOp)))

	// Invites: list+create are admin-only; check+redeem are public (auth checked inside handler).
	// Peligrosa: httputil.RequireLocalOriginSoft on both routes — redeem is public but invite-gated.
	mux.Handle("/api/pelicula/invites", auth.GuardAdmin(httputil.RequireLocalOriginSoft(http.HandlerFunc(d.HandleInvites))))
	mux.HandleFunc("/api/pelicula/invites/", httputil.RequireLocalOriginSoft(http.HandlerFunc(d.HandleInviteOp)).ServeHTTP)

	// Open registration (LAN-only, optional): public account creation without invite tokens.
	// Peligrosa: httputil.RequireLocalOriginStrict ensures only LAN browsers can POST.
	mux.HandleFunc("/api/pelicula/register/check", auth.HandleOpenRegCheck)
	mux.HandleFunc("/api/pelicula/generate-password", d.HandleGeneratePassword)
	mux.Handle("/api/pelicula/register", httputil.RequireLocalOriginStrict(http.HandlerFunc(auth.HandleOpenRegister)))
}
