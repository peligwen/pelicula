package clients

// JellyfinLoginResult holds the fields Peligrosa needs after authenticating a
// user against Jellyfin's /Users/AuthenticateByName endpoint.
type JellyfinLoginResult struct {
	UserID          string
	Username        string
	IsAdministrator bool
	AccessToken     string
}

// JellyfinClient is the subset of Jellyfin operations that peligrosa-scope
// code needs. A concrete *jellyfinHTTPClient wraps the existing package-level
// helpers. Consumers depend on this interface so peligrosa can live in a
// subpackage without importing the main package.
type JellyfinClient interface {
	AuthenticateByName(username, password string) (*JellyfinLoginResult, error)
	CreateUser(username, password string) (string, error)
}
