package main

import (
	"net"
	"testing"
	"time"
)

// ── /proc/net/tcp hex parsing ──────────────────────────────────────────────

func TestParseHexAddrIPv4(t *testing.T) {
	tests := []struct {
		name     string
		field    string
		wantIP   string
		wantPort uint16
	}{
		{
			// 0100007F = 127.0.0.1 stored as LE uint32
			name:     "loopback",
			field:    "0100007F:0035",
			wantIP:   "127.0.0.1",
			wantPort: 53,
		},
		{
			// 010110AC = 172.16.1.1
			name:     "RFC1918 172.16",
			field:    "010110AC:01BB",
			wantIP:   "172.16.1.1",
			wantPort: 443,
		},
		{
			// 0101A8C0 = 192.168.1.1
			name:     "RFC1918 192.168",
			field:    "0101A8C0:0050",
			wantIP:   "192.168.1.1",
			wantPort: 80,
		},
		{
			// 1D21221D = 29.34.33.29
			name:     "public IP",
			field:    "1D21221D:1AE1",
			wantIP:   "29.34.33.29",
			wantPort: 6881,
		},
		{
			// FE00FEA9 = 169.254.0.254 (link-local)
			name:     "link-local",
			field:    "FE00FEA9:0050",
			wantIP:   "169.254.0.254",
			wantPort: 80,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip, port, err := parseHexAddr(tt.field, false)
			if err != nil {
				t.Fatalf("parseHexAddr(%q): %v", tt.field, err)
			}
			if got := ip.String(); got != tt.wantIP {
				t.Errorf("IP: got %q want %q", got, tt.wantIP)
			}
			if port != tt.wantPort {
				t.Errorf("port: got %d want %d", port, tt.wantPort)
			}
		})
	}
}

func TestParseHexAddrIPv6(t *testing.T) {
	tests := []struct {
		name     string
		field    string
		wantIP   string
		wantPort uint16
	}{
		{
			// ::1 — the classic IPv6 little-endian word-order test.
			// Network order: 00 00 00 00  00 00 00 00  00 00 00 00  00 00 00 01
			// Each word stored LE:
			//   word0: 00000000 (same)
			//   word1: 00000000 (same)
			//   word2: 00000000 (same)
			//   word3: 01 00 00 00 -> LE hex: 01000000
			// Full hex string: 00000000000000000000000001000000
			// If you naively reverse all 16 bytes you'd get 00000000...01 reversed
			// = 01000000... which is wrong. The per-word reversal is essential.
			name:     "loopback ::1",
			field:    "00000000000000000000000001000000:0050",
			wantIP:   "::1",
			wantPort: 80,
		},
		{
			// ::ffff:8.8.8.8 (IPv4-mapped)
			// Network order: 00*10 + FF FF 08 08 08 08
			// word2 BE: 0x0000FFFF -> LE bytes: FF FF 00 00 -> hex: FFFF0000
			// word3 BE: 0x08080808 -> LE bytes: 08 08 08 08 -> hex: 08080808
			// Full: 0000000000000000FFFF000008080808
			// Go renders IPv4-mapped addresses as "8.8.8.8" via To4().
			name:     "IPv4-mapped ::ffff:8.8.8.8",
			field:    "0000000000000000FFFF000008080808:01BB",
			wantIP:   "8.8.8.8",
			wantPort: 443,
		},
		{
			// ::ffff:1.2.3.4
			// word3 BE: 0x01020304 -> LE bytes: 04 03 02 01 -> hex: 04030201
			// Full: 0000000000000000FFFF000004030201
			name:     "IPv4-mapped ::ffff:1.2.3.4",
			field:    "0000000000000000FFFF000004030201:01BB",
			wantIP:   "1.2.3.4",
			wantPort: 443,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip, port, err := parseHexAddr(tt.field, true)
			if err != nil {
				t.Fatalf("parseHexAddr(%q, ipv6=true): %v", tt.field, err)
			}
			// Use To4() to normalize IPv4-mapped addresses, same as Go's net package.
			ipStr := ip.String()
			if ip4 := ip.To4(); ip4 != nil {
				ipStr = ip4.String()
			}
			if ipStr != tt.wantIP {
				t.Errorf("IP: got %q want %q", ipStr, tt.wantIP)
			}
			if port != tt.wantPort {
				t.Errorf("port: got %d want %d", port, tt.wantPort)
			}
		})
	}
}

// TestIPv6WordOrderNaiveReversalWouldFail documents the invariant: if you
// reverse all 16 bytes of the ::1 hex instead of reversing within each word,
// you get the wrong answer.
func TestIPv6WordOrderNaiveReversalWouldFail(t *testing.T) {
	// ::1 proc field: 00000000000000000000000001000000
	// Per-word reversal (correct) → ::1
	ip, _, err := parseHexAddr("00000000000000000000000001000000:0000", true)
	if err != nil {
		t.Fatal(err)
	}
	if !ip.Equal(net.IPv6loopback) {
		t.Fatalf("expected ::1, got %s (word-order reversal broken)", ip)
	}

	// The wrong result (naive full-16-byte reversal) would give:
	// 00000000000000000000000001000000 reversed = 00000010000000000000000000000000
	// = 0.0.0.16.0.0.0.0.0.0.0.0.0.0.0.0 — clearly not ::1.
	// We just confirm the correct path produces ::1.
}

// ── parseProcNetTCP integration (fixture files) ────────────────────────────

func TestParseProcNetTCPFixture(t *testing.T) {
	entries, err := parseProcNetTCP("testdata/proc_net_tcp.txt", false)
	if err != nil {
		t.Fatalf("parseProcNetTCP: %v", err)
	}

	// Fixture has 7 data lines after the header:
	//   line 0: LISTEN (0A) → filtered by parseProcNetTCP
	//   line 1: ESTABLISHED, remote 192.168.1.1:443 → kept
	//   line 2: ESTABLISHED, remote 192.168.2.2:443 → kept
	//   line 3: ESTABLISHED, remote 29.34.33.29:6881 → kept
	//   line 4: ESTABLISHED, remote 169.254.0.254:80 (link-local) → kept by parse (filter is in poll)
	//   line 5: ESTABLISHED, remote 127.0.1.1:80 (loopback) → kept by parse (filter is in poll)
	//   line 6: ESTABLISHED, remote 46.2.6.13:6881 → kept
	// parseProcNetTCP only filters by state; shouldFilterRemote is called by poll.
	if len(entries) != 6 {
		t.Fatalf("expected 6 ESTABLISHED entries, got %d", len(entries))
	}

	// Verify line 1 (first ESTABLISHED): remote 192.168.1.1:443
	e := entries[0]
	if got := e.RemoteIP.String(); got != "192.168.1.1" {
		t.Errorf("entry[0] RemoteIP: got %q want 192.168.1.1", got)
	}
	if e.RemotePort != 443 {
		t.Errorf("entry[0] RemotePort: got %d want 443", e.RemotePort)
	}

	// Verify line 3: remote 29.34.33.29:6881
	e3 := entries[2]
	if got := e3.RemoteIP.String(); got != "29.34.33.29" {
		t.Errorf("entry[2] RemoteIP: got %q want 29.34.33.29", got)
	}
	if e3.RemotePort != 6881 {
		t.Errorf("entry[2] RemotePort: got %d want 6881", e3.RemotePort)
	}
}

func TestParseProcNetTCP6Fixture(t *testing.T) {
	entries, err := parseProcNetTCP("testdata/proc_net_tcp6.txt", true)
	if err != nil {
		t.Fatalf("parseProcNetTCP6: %v", err)
	}

	// Fixture has 4 data lines after the header:
	//   line 0: LISTEN (0A) → filtered
	//   line 1: ESTABLISHED, ::1→::1 (loopback) → kept by parse
	//   line 2: ESTABLISHED, ::ffff:172.16.3.3 → ::ffff:8.8.8.8:443 → kept
	//   line 3: ESTABLISHED, ::ffff:172.16.3.3 → ::ffff:1.2.3.4:443 → kept
	if len(entries) != 3 {
		t.Fatalf("expected 3 ESTABLISHED entries, got %d", len(entries))
	}

	// entry[0] is the ::1→::1 loopback (line 1 in data)
	if !entries[0].RemoteIP.Equal(net.IPv6loopback) {
		t.Errorf("entry[0] RemoteIP: got %s want ::1", entries[0].RemoteIP)
	}

	// entry[1]: remote should be 8.8.8.8 (IPv4-mapped normalizes to IPv4)
	e1 := entries[1]
	remoteStr := e1.RemoteIP.String()
	if ip4 := e1.RemoteIP.To4(); ip4 != nil {
		remoteStr = ip4.String()
	}
	if remoteStr != "8.8.8.8" {
		t.Errorf("entry[1] RemoteIP: got %q want 8.8.8.8", remoteStr)
	}
	if e1.RemotePort != 443 {
		t.Errorf("entry[1] RemotePort: got %d want 443", e1.RemotePort)
	}

	// entry[2]: remote should be 1.2.3.4
	e2 := entries[2]
	remoteStr2 := e2.RemoteIP.String()
	if ip4 := e2.RemoteIP.To4(); ip4 != nil {
		remoteStr2 = ip4.String()
	}
	if remoteStr2 != "1.2.3.4" {
		t.Errorf("entry[2] RemoteIP: got %q want 1.2.3.4", remoteStr2)
	}
}

// ── shouldFilterRemote ─────────────────────────────────────────────────────

func TestShouldFilterRemote(t *testing.T) {
	tests := []struct {
		ip     string
		want   bool
		reason string
	}{
		{"127.0.0.1", true, "loopback"},
		{"127.0.1.1", true, "loopback range"},
		{"::1", true, "IPv6 loopback"},
		{"169.254.0.1", true, "link-local"},
		{"169.254.255.254", true, "link-local high"},
		{"fe80::1", true, "IPv6 link-local"},
		{"0.0.0.0", true, "unspecified"},
		{"::", true, "IPv6 unspecified"},
		{"8.8.8.8", false, "public"},
		{"1.2.3.4", false, "public"},
		{"192.168.1.1", false, "RFC1918 (not filtered)"},
		{"172.16.0.1", false, "RFC1918 (not filtered)"},
		{"10.0.0.1", false, "RFC1918 (not filtered)"},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("invalid IP %q", tt.ip)
			}
			got := shouldFilterRemote(ip)
			if got != tt.want {
				t.Errorf("shouldFilterRemote(%q) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

// ── qBittorrent peer collapse ──────────────────────────────────────────────

func TestCollapseQBTPeers(t *testing.T) {
	gluetunIP := "172.16.3.3"
	s := newConnStore(time.Hour)

	base := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)

	// Insert three port-6881 entries from gluetunIP (torrent peers).
	peers := []connEntry{
		{Kind: "vpn", Container: "gluetun/vpn", SourceIP: gluetunIP, DestIP: "1.2.3.4", DestHost: "1.2.3.4", DestPort: 6881, FirstSeen: base, LastSeen: base.Add(10 * time.Minute)},
		{Kind: "vpn", Container: "gluetun/vpn", SourceIP: gluetunIP, DestIP: "5.6.7.8", DestHost: "5.6.7.8", DestPort: 6881, FirstSeen: base.Add(time.Minute), LastSeen: base.Add(20 * time.Minute)},
		{Kind: "vpn", Container: "gluetun/vpn", SourceIP: gluetunIP, DestIP: "9.10.11.12", DestHost: "9.10.11.12", DestPort: 6881, FirstSeen: base.Add(2 * time.Minute), LastSeen: base.Add(5 * time.Minute)},
	}
	for _, e := range peers {
		s.upsert(e)
	}

	// Insert one non-6881 connection to ensure it survives collapse.
	s.upsert(connEntry{Kind: "vpn", Container: "sonarr", SourceIP: "172.16.1.1", DestIP: "192.168.1.1", DestHost: "nas.local", DestPort: 443, FirstSeen: base, LastSeen: base.Add(30 * time.Minute)})

	snap := s.snapshot(gluetunIP)

	// Expect 2 entries: 1 collapsed qbt row + 1 sonarr row.
	if len(snap) != 2 {
		t.Fatalf("expected 2 entries in snapshot, got %d", len(snap))
	}

	// Find the collapsed qbt row.
	var qbt *connEntry
	for i := range snap {
		if snap[i].Container == "qbittorrent" {
			qbt = &snap[i]
		}
	}
	if qbt == nil {
		t.Fatal("no collapsed qbittorrent row found in snapshot")
	}

	if qbt.DestHost != "<peers>" {
		t.Errorf("qbt DestHost: got %q want <peers>", qbt.DestHost)
	}
	if qbt.DestIP != "" {
		t.Errorf("qbt DestIP: got %q want empty", qbt.DestIP)
	}
	if qbt.DestPort != 6881 {
		t.Errorf("qbt DestPort: got %d want 6881", qbt.DestPort)
	}
	if qbt.PeerCount != 3 {
		t.Errorf("qbt PeerCount: got %d want 3", qbt.PeerCount)
	}
	// last_seen should be max of the group: base + 20 min
	wantLastSeen := base.Add(20 * time.Minute)
	if !qbt.LastSeen.Equal(wantLastSeen) {
		t.Errorf("qbt LastSeen: got %v want %v", qbt.LastSeen, wantLastSeen)
	}
	// first_seen should be min of the group: base
	if !qbt.FirstSeen.Equal(base) {
		t.Errorf("qbt FirstSeen: got %v want %v", qbt.FirstSeen, base)
	}
	if qbt.Kind != "vpn" {
		t.Errorf("qbt Kind: got %q want vpn", qbt.Kind)
	}
}

// TestCollapseQBTPeersNoGluetun confirms that port-6881 entries from a
// non-gluetun source are NOT collapsed.
func TestCollapseQBTPeersNoGluetun(t *testing.T) {
	s := newConnStore(time.Hour)
	base := time.Now()

	// Port 6881 but gluetunIP is empty → no collapse.
	s.upsert(connEntry{Kind: "internet", Container: "someapp", SourceIP: "10.0.0.2", DestIP: "1.2.3.4", DestPort: 6881, FirstSeen: base, LastSeen: base})
	s.upsert(connEntry{Kind: "internet", Container: "someapp", SourceIP: "10.0.0.2", DestIP: "5.6.7.8", DestPort: 6881, FirstSeen: base, LastSeen: base})

	snap := s.snapshot("") // no gluetunIP
	if len(snap) != 2 {
		t.Errorf("expected 2 individual entries (no collapse), got %d", len(snap))
	}
	for _, e := range snap {
		if e.Container == "qbittorrent" {
			t.Error("unexpected collapsed qbittorrent row when gluetunIP is empty")
		}
	}
}

// ── Retention eviction ─────────────────────────────────────────────────────

func TestRetentionEviction(t *testing.T) {
	retention := time.Hour
	s := newConnStore(retention)

	now := time.Date(2026, 4, 20, 16, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)      // 2h ago — should be evicted
	fresh := now.Add(-30 * time.Minute) // 30m ago — should survive

	s.upsert(connEntry{SourceIP: "10.0.0.1", DestIP: "1.1.1.1", DestPort: 443, FirstSeen: old, LastSeen: old})
	s.upsert(connEntry{SourceIP: "10.0.0.2", DestIP: "8.8.8.8", DestPort: 443, FirstSeen: fresh, LastSeen: fresh})

	if s.size() != 2 {
		t.Fatalf("expected 2 entries before eviction, got %d", s.size())
	}

	s.evict(now)

	if s.size() != 1 {
		t.Fatalf("expected 1 entry after eviction, got %d", s.size())
	}

	snap := s.snapshot("")
	if len(snap) != 1 {
		t.Fatalf("expected 1 entry in snapshot, got %d", len(snap))
	}
	if snap[0].DestIP != "8.8.8.8" {
		t.Errorf("wrong entry survived: got DestIP=%q want 8.8.8.8", snap[0].DestIP)
	}
}

func TestRetentionEvictionBoundary(t *testing.T) {
	// Entry exactly at the cutoff boundary (LastSeen == now - retention) should
	// be evicted (Before uses strict less-than, so equal-to-cutoff survives).
	// Actually: evict removes if LastSeen.Before(cutoff), so cutoff-exact survives.
	retention := time.Hour
	s := newConnStore(retention)

	now := time.Date(2026, 4, 20, 16, 0, 0, 0, time.UTC)
	cutoff := now.Add(-retention)

	// Exactly at cutoff: LastSeen.Before(cutoff) is false → survives.
	s.upsert(connEntry{SourceIP: "10.0.0.1", DestIP: "1.1.1.1", DestPort: 443, FirstSeen: cutoff, LastSeen: cutoff})
	// One nanosecond before cutoff → evicted.
	old := cutoff.Add(-time.Nanosecond)
	s.upsert(connEntry{SourceIP: "10.0.0.2", DestIP: "2.2.2.2", DestPort: 443, FirstSeen: old, LastSeen: old})

	s.evict(now)

	if s.size() != 1 {
		t.Errorf("expected 1 entry at boundary, got %d", s.size())
	}
}

// ── Container name stripping ───────────────────────────────────────────────

func TestExtractServiceName(t *testing.T) {
	tests := []struct {
		rawName string
		prefix  string
		want    string
	}{
		{"/pelicula-sonarr-1", "pelicula-", "sonarr"},
		{"/pelicula-radarr-1", "pelicula-", "radarr"},
		{"/pelicula-docker-proxy-1", "pelicula-", "docker-proxy"},
		{"/pelicula-pelicula-api-1", "pelicula-", "pelicula-api"},
		{"/pelicula-gluetun-1", "pelicula-", "gluetun"},
		{"/mystack-sonarr-2", "mystack-", "sonarr"},
		// No leading slash (defensive).
		{"pelicula-jellyfin-1", "pelicula-", "jellyfin"},
		// No prefix match: return as-is (minus replica suffix).
		{"/other-sonarr-1", "pelicula-", "other-sonarr"},
	}

	for _, tt := range tests {
		t.Run(tt.rawName, func(t *testing.T) {
			got := extractServiceName(tt.rawName, tt.prefix)
			if got != tt.want {
				t.Errorf("extractServiceName(%q, %q) = %q, want %q", tt.rawName, tt.prefix, got, tt.want)
			}
		})
	}
}

// ── DNS cache ─────────────────────────────────────────────────────────────

func TestDNSCacheGetSet(t *testing.T) {
	c := newDNSCache(time.Hour)

	// Miss.
	if _, ok := c.get("1.2.3.4"); ok {
		t.Error("expected cache miss on empty cache")
	}

	c.set("1.2.3.4", "example.com")
	if h, ok := c.get("1.2.3.4"); !ok || h != "example.com" {
		t.Errorf("cache get after set: got (%q, %v), want (example.com, true)", h, ok)
	}
}

func TestDNSCacheExpiry(t *testing.T) {
	// Use a very short TTL to test expiry.
	c := newDNSCache(time.Millisecond)
	c.set("1.2.3.4", "example.com")
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.get("1.2.3.4"); ok {
		t.Error("expected cache miss after TTL expiry")
	}
}

// ── Upsert first_seen preservation ────────────────────────────────────────

func TestUpsertPreservesFirstSeen(t *testing.T) {
	s := newConnStore(time.Hour)
	first := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)

	s.upsert(connEntry{SourceIP: "10.0.0.1", DestIP: "1.1.1.1", DestPort: 443, FirstSeen: first, LastSeen: first})
	s.upsert(connEntry{SourceIP: "10.0.0.1", DestIP: "1.1.1.1", DestPort: 443, FirstSeen: second, LastSeen: second})

	snap := s.snapshot("")
	if len(snap) != 1 {
		t.Fatalf("expected 1 entry after double upsert, got %d", len(snap))
	}
	// first_seen must be the original time.
	if !snap[0].FirstSeen.Equal(first) {
		t.Errorf("FirstSeen: got %v want %v (upsert must not overwrite first_seen)", snap[0].FirstSeen, first)
	}
	// last_seen must be updated.
	if !snap[0].LastSeen.Equal(second) {
		t.Errorf("LastSeen: got %v want %v", snap[0].LastSeen, second)
	}
}

// ── Snapshot sort order ────────────────────────────────────────────────────

func TestSnapshotSortedByLastSeenDesc(t *testing.T) {
	s := newConnStore(time.Hour)
	base := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)

	s.upsert(connEntry{SourceIP: "10.0.0.1", DestIP: "1.1.1.1", DestPort: 443, FirstSeen: base, LastSeen: base})
	s.upsert(connEntry{SourceIP: "10.0.0.1", DestIP: "2.2.2.2", DestPort: 443, FirstSeen: base, LastSeen: base.Add(time.Hour)})
	s.upsert(connEntry{SourceIP: "10.0.0.1", DestIP: "3.3.3.3", DestPort: 443, FirstSeen: base, LastSeen: base.Add(30 * time.Minute)})

	snap := s.snapshot("")
	for i := 1; i < len(snap); i++ {
		if snap[i].LastSeen.After(snap[i-1].LastSeen) {
			t.Errorf("snapshot not sorted desc: snap[%d].LastSeen=%v > snap[%d].LastSeen=%v",
				i, snap[i].LastSeen, i-1, snap[i-1].LastSeen)
		}
	}
	// First entry should be 2.2.2.2 (most recent).
	if snap[0].DestIP != "2.2.2.2" {
		t.Errorf("first entry should be 2.2.2.2, got %q", snap[0].DestIP)
	}
}
