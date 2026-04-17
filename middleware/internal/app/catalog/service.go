package catalog

// ArrClient is the subset of ServiceClients that the catalog package needs.
type ArrClient interface {
	// Keys returns API keys for Sonarr, Radarr, and Prowlarr.
	Keys() (sonarr, radarr, prowlarr string)
	// ArrGet makes a GET request to a *arr service.
	ArrGet(baseURL, apiKey, path string) ([]byte, error)
	// ArrPost makes a POST request to a *arr service.
	ArrPost(baseURL, apiKey, path string, payload any) ([]byte, error)
	// ArrPut makes a PUT request to a *arr service.
	ArrPut(baseURL, apiKey, path string, payload any) ([]byte, error)
	// ArrDelete makes a DELETE request to a *arr service.
	ArrDelete(baseURL, apiKey, path string) ([]byte, error)
	// ArrGetAllQueueRecords paginates all records from an *arr queue endpoint.
	ArrGetAllQueueRecords(baseURL, apiKey, apiVer, extraParams string) ([]map[string]any, error)
}

// JellyfinMetaClient is the subset needed for Jellyfin metadata sync.
// It is a function-based interface to break the cycle between catalog and jellyfin packages.
type JellyfinMetaClient interface {
	// GetJellyfinAPIKey returns the Jellyfin API key.
	GetJellyfinAPIKey() string
	// GetJellyfinUserID returns (and optionally resolves) the pelicula-internal user ID.
	GetJellyfinUserID() string
	// SetJellyfinUserID caches the resolved user ID.
	SetJellyfinUserID(id string)
	// JellyfinGet makes a GET request to Jellyfin.
	JellyfinGet(path, apiKey string) ([]byte, error)
}
