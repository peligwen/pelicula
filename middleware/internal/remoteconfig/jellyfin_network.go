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

	var middle string
	if publishedURL != "" {
		middle = "<PublishedServerUrl>" + xmlEscape(publishedURL) + "</PublishedServerUrl>"
	}
	// KnownProxies is required for nginx → Jellyfin X-Forwarded-For trust;
	// without it, remote-vhost role capping and per-IP auth rate limiting
	// see every request as coming from nginx itself. Must mirror the seed
	// in cmd/pelicula/seed.go (jellyfinKnownProxiesXML).
	knownProxies := jellyfinKnownProxiesXML()

	content := header + middle + knownProxies + footer

	path := filepath.Join(jellyfinConfigDir, "network.xml")
	tmp, err := os.CreateTemp(jellyfinConfigDir, "network-*.xml")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write([]byte(content)); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	renamed = true
	return nil
}

// jellyfinKnownProxiesXML returns the <KnownProxies> XML fragment.
// Uses PELICULA_KNOWN_PROXIES (comma-separated CIDRs) when set; otherwise
// defaults to the Docker bridge subnet 172.16.0.0/12. Mirrors the canonical
// helper in cmd/pelicula/seed.go.
func jellyfinKnownProxiesXML() string {
	raw := os.Getenv("PELICULA_KNOWN_PROXIES")
	var entries []string
	if raw != "" {
		for _, e := range strings.Split(raw, ",") {
			e = strings.TrimSpace(e)
			if e != "" {
				entries = append(entries, "<string>"+xmlEscape(e)+"</string>")
			}
		}
	}
	if len(entries) == 0 {
		entries = []string{"<string>172.16.0.0/12</string>"}
	}
	return "<KnownProxies>" + strings.Join(entries, "") + "</KnownProxies>"
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}
