package main

// store.go — in-memory connection buffer with upsert, eviction, and
// qBittorrent peer-collapse logic.

import (
	"net"
	"sort"
	"sync"
	"time"
)

// connKey uniquely identifies a connection for buffering purposes.
type connKey struct {
	SourceIP string
	DestIP   string
	DestPort uint16
}

// connEntry is a buffered connection record.
type connEntry struct {
	Kind      string // "vpn" or "internet"
	Container string // stripped service name
	SourceIP  string
	DestHost  string // reverse-DNS result, or raw IP if lookup failed
	DestIP    string
	DestPort  uint16
	FirstSeen time.Time
	LastSeen  time.Time
	PeerCount int // non-zero only for the collapsed qBittorrent peers row
}

// connStore is the in-memory buffer of active/recent connections.
type connStore struct {
	mu        sync.RWMutex
	entries   map[connKey]*connEntry
	retention time.Duration
}

func newConnStore(retention time.Duration) *connStore {
	return &connStore{
		entries:   make(map[connKey]*connEntry),
		retention: retention,
	}
}

// upsert inserts or updates an entry. If the key already exists, last_seen is
// updated and container/kind/destHost are refreshed (they may change as the
// container map warms up).
func (s *connStore) upsert(e connEntry) {
	k := connKey{SourceIP: e.SourceIP, DestIP: e.DestIP, DestPort: e.DestPort}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.entries[k]; ok {
		existing.LastSeen = e.LastSeen
		existing.Kind = e.Kind
		existing.Container = e.Container
		if e.DestHost != "" && e.DestHost != e.DestIP {
			existing.DestHost = e.DestHost
		}
		return
	}
	cp := e
	s.entries[k] = &cp
}

// updateDestHost sets the resolved hostname for a dest IP across all entries
// that share that IP.
func (s *connStore) updateDestHost(destIP, hostname string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.entries {
		if k.DestIP == destIP {
			e.DestHost = hostname
		}
	}
}

// evict removes entries whose last_seen is older than the retention window.
func (s *connStore) evict(now time.Time) {
	cutoff := now.Add(-s.retention)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.entries {
		if e.LastSeen.Before(cutoff) {
			delete(s.entries, k)
		}
	}
}

// snapshot returns a sorted slice of connEntry for the API response,
// with qBittorrent port-6881 entries collapsed into a single synthetic row.
//
// Collapse rule: any entry where DestPort==6881 and SourceIP==gluetunIP is
// collapsed into one row with:
//   - container: "qbittorrent"
//   - dest_host: "<peers>"
//   - dest_ip: ""
//   - dest_port: 6881
//   - peer_count: count of unique dest IPs in the group
//   - first_seen: min of group
//   - last_seen: max of group
//   - kind, source_ip: from the group (all identical for gluetun traffic)
func (s *connStore) snapshot(gluetunIP string) []connEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var normal []connEntry
	var qbtPeers []connEntry

	for _, e := range s.entries {
		if e.DestPort == 6881 && e.SourceIP == gluetunIP && gluetunIP != "" {
			qbtPeers = append(qbtPeers, *e)
		} else {
			normal = append(normal, *e)
		}
	}

	if len(qbtPeers) > 0 {
		collapsed := collapseQBTPeers(qbtPeers, gluetunIP)
		normal = append(normal, collapsed)
	}

	// Sort by last_seen descending.
	sort.Slice(normal, func(i, j int) bool {
		return normal[i].LastSeen.After(normal[j].LastSeen)
	})
	return normal
}

// collapseQBTPeers builds the synthetic qBittorrent peer row.
func collapseQBTPeers(peers []connEntry, gluetunIP string) connEntry {
	uniqueIPs := make(map[string]struct{}, len(peers))
	var firstSeen, lastSeen time.Time
	var kind string

	for i, p := range peers {
		uniqueIPs[p.DestIP] = struct{}{}
		if i == 0 || p.FirstSeen.Before(firstSeen) {
			firstSeen = p.FirstSeen
		}
		if p.LastSeen.After(lastSeen) {
			lastSeen = p.LastSeen
		}
		kind = p.Kind
	}

	return connEntry{
		Kind:      kind,
		Container: "qbittorrent",
		SourceIP:  gluetunIP,
		DestHost:  "<peers>",
		DestIP:    "", // collapsed; no single dest IP
		DestPort:  6881,
		FirstSeen: firstSeen,
		LastSeen:  lastSeen,
		PeerCount: len(uniqueIPs),
	}
}

// size returns the current number of entries (for diagnostics).
func (s *connStore) size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// dnsCache is a bounded TTL cache for reverse-DNS results.
// Cap ~1024 entries; entries expire after TTL.
type dnsCache struct {
	mu      sync.RWMutex
	entries map[string]dnsCacheEntry
	ttl     time.Duration
}

type dnsCacheEntry struct {
	hostname string
	expires  time.Time
}

func newDNSCache(ttl time.Duration) *dnsCache {
	return &dnsCache{
		entries: make(map[string]dnsCacheEntry),
		ttl:     ttl,
	}
}

// get returns (hostname, true) if a non-expired entry exists.
func (c *dnsCache) get(ip string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[ip]
	if !ok || time.Now().After(e.expires) {
		return "", false
	}
	return e.hostname, true
}

// set stores a hostname result for an IP.
func (c *dnsCache) set(ip, hostname string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Evict oldest entry if at cap.
	const maxEntries = 1024
	if len(c.entries) >= maxEntries {
		// Remove an arbitrary entry (Go map iteration order is random).
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[ip] = dnsCacheEntry{hostname: hostname, expires: time.Now().Add(c.ttl)}
}

// resolveIP does a reverse DNS lookup with a 2s timeout.
// Returns the first name found, or the raw IP on failure.
func resolveIP(ip string) string {
	ctx, cancel := withTimeout(2 * time.Second)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(ctx, ip)
	if err != nil || len(names) == 0 {
		return ip
	}
	// Trim trailing dot from FQDN.
	name := names[0]
	if len(name) > 0 && name[len(name)-1] == '.' {
		name = name[:len(name)-1]
	}
	return name
}
