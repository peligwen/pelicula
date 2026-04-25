// Package remoteconfig holds rendering helpers for files the dashboard needs
// to rewrite at runtime when the user changes settings (Jellyfin network.xml,
// nginx remote vhost, certs, etc).
//
// The CLI (cmd/pelicula) holds the canonical templates and seed logic; this
// package is a deliberately small replication of the bits the middleware
// container can apply in-place after a settings POST. Cross-module sharing
// would require module surgery (separate go.mod files); replication keeps the
// dependency graph clean and is small enough that drift is easy to spot.
package remoteconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteJellyfinNetworkXML rewrites jellyfinConfigDir/network.xml to advertise
// the given publishedURL via Jellyfin's <PublishedServerUrl> element. Mirrors
// the canonical seed in cmd/pelicula/seed.go.
//
// jellyfinConfigDir is the path Jellyfin sees as /config (i.e., the host's
// ${CONFIG_DIR}/jellyfin, mounted into both Jellyfin and pelicula-api).
//
// When publishedURL is empty, the file is rewritten without the element so
// Jellyfin falls back to its default advertising behavior — keeping a knob
// for "I want auto-detect" without forcing operators to manually delete the
// element from the XML.
//
// Caller is responsible for restarting Jellyfin after this returns; the
// network.xml is read at startup.
func WriteJellyfinNetworkXML(jellyfinConfigDir, publishedURL string) error {
	if jellyfinConfigDir == "" {
		return fmt.Errorf("jellyfinConfigDir is required")
	}
	if err := os.MkdirAll(jellyfinConfigDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", jellyfinConfigDir, err)
	}

	const header = `<?xml version="1.0" encoding="utf-8"?><NetworkConfiguration xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema"><BaseUrl>/jellyfin</BaseUrl>`
	const footer = `</NetworkConfiguration>`
	var content string
	if publishedURL != "" {
		content = header + "<PublishedServerUrl>" + xmlEscape(publishedURL) + "</PublishedServerUrl>" + footer
	} else {
		content = header + footer
	}

	path := filepath.Join(jellyfinConfigDir, "network.xml")
	return os.WriteFile(path, []byte(content), 0644)
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}
