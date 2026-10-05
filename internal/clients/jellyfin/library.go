package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Library is a Jellyfin virtual folder.
type Library struct {
	Name           string   `json:"name"`
	CollectionType string   `json:"collection_type"` // movies, tvshows, ...; empty for mixed
	Locations      []string `json:"locations"`
}

// ListLibraries returns the configured libraries.
func (c *Client) ListLibraries(ctx context.Context, token string) ([]Library, error) {
	body, err := c.Get(ctx, "/Library/VirtualFolders", token)
	if err != nil {
		return nil, wrap("list libraries", err)
	}
	var raw []struct {
		Name           string   `json:"Name"`
		CollectionType string   `json:"CollectionType"`
		Locations      []string `json:"Locations"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("jellyfin: parse libraries: %w", err)
	}
	libs := make([]Library, 0, len(raw))
	for _, l := range raw {
		libs = append(libs, Library{Name: l.Name, CollectionType: l.CollectionType, Locations: l.Locations})
	}
	return libs, nil
}

// AddLibrary creates a library over path and starts a scan. collectionType is
// "movies" or "tvshows".
func (c *Client) AddLibrary(ctx context.Context, token, name, collectionType, path string) error {
	q := url.Values{
		"name":           {name},
		"collectionType": {collectionType},
		"refreshLibrary": {"true"},
	}
	// "paths" is comma-delimited server-side, so a comma would split the path;
	// the body's PathInfos carries it either way.
	if !strings.Contains(path, ",") {
		q.Set("paths", path)
	}
	_, err := c.Post(ctx, "/Library/VirtualFolders?"+q.Encode(), token, map[string]any{
		"LibraryOptions": map[string]any{
			"PathInfos": []map[string]any{{"Path": path}},
		},
	})
	return wrap("add library", err)
}

// RefreshLibrary asks Jellyfin to scan all libraries.
func (c *Client) RefreshLibrary(ctx context.Context, token string) error {
	_, err := c.Do(ctx, http.MethodPost, "/Library/Refresh", token, nil)
	return wrap("refresh library", err)
}
