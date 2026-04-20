package main

// proc.go — /proc/net/tcp and /proc/net/tcp6 parsing.
//
// /proc/net/tcp format (space-separated, first line is header):
//
//	sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode
//	0:  0100007F:0035 00000000:0000 0A ...
//
// Addresses are hex-encoded in network (big-endian on wire) byte order BUT
// stored as a 32-bit little-endian integer on x86. So each 4-byte word must be
// byte-reversed before interpreting as an IPv4 quad or IPv6 component.
//
// State 01 = ESTABLISHED. We keep only those.

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// tcpEntry is a single parsed line from /proc/net/tcp or /proc/net/tcp6.
type tcpEntry struct {
	LocalIP    net.IP
	LocalPort  uint16
	RemoteIP   net.IP
	RemotePort uint16
}

// parseProcNetTCP parses a /proc/net/tcp or /proc/net/tcp6 file and returns
// only ESTABLISHED (state=01) entries. The isIPv6 flag selects 16-byte vs
// 4-byte address parsing.
func parseProcNetTCP(path string, isIPv6 bool) ([]tcpEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []tcpEntry
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue // skip header line
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		e, ok := parseProcLine(line, isIPv6)
		if !ok {
			continue
		}
		entries = append(entries, e)
	}
	return entries, sc.Err()
}

// parseProcLine parses a single /proc/net/tcp[6] data line.
// Returns (entry, true) only for ESTABLISHED state.
func parseProcLine(line string, isIPv6 bool) (tcpEntry, bool) {
	fields := strings.Fields(line)
	// Minimum: sl, local_addr, rem_addr, st  → indices 0,1,2,3
	if len(fields) < 4 {
		return tcpEntry{}, false
	}
	// State is fields[3], a 2-char hex string.
	if fields[3] != "01" { // 01 = TCP_ESTABLISHED
		return tcpEntry{}, false
	}

	localIP, localPort, err := parseHexAddr(fields[1], isIPv6)
	if err != nil {
		return tcpEntry{}, false
	}
	remoteIP, remotePort, err := parseHexAddr(fields[2], isIPv6)
	if err != nil {
		return tcpEntry{}, false
	}

	return tcpEntry{
		LocalIP:    localIP,
		LocalPort:  localPort,
		RemoteIP:   remoteIP,
		RemotePort: remotePort,
	}, true
}

// parseHexAddr parses a "HHHHHHHH:PPPP" or "HHHH...HHHH:PPPP" address.
//
// IPv4: 8 hex chars = 4 bytes stored as a little-endian 32-bit word.
//
//	"0101A8C0:01BB" → bytes [0x01,0x01,0xa8,0xc0] as uint32-LE → 0xC0A80101 → 192.168.1.1:443
//
// IPv6: 32 hex chars = four consecutive little-endian 32-bit words.
//
//	Word order is preserved; only the bytes within each 4-byte word are reversed.
func parseHexAddr(s string, isIPv6 bool) (net.IP, uint16, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return nil, 0, fmt.Errorf("bad addr field %q", s)
	}
	addrHex, portHex := parts[0], parts[1]

	port64, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return nil, 0, fmt.Errorf("bad port %q: %w", portHex, err)
	}
	port := uint16(port64)

	raw, err := hex.DecodeString(addrHex)
	if err != nil {
		return nil, 0, fmt.Errorf("bad addr hex %q: %w", addrHex, err)
	}

	var ip net.IP
	if !isIPv6 {
		// IPv4: 4 bytes, little-endian 32-bit word → reverse to get network order.
		if len(raw) != 4 {
			return nil, 0, fmt.Errorf("IPv4 addr wrong length %d", len(raw))
		}
		v := binary.LittleEndian.Uint32(raw)
		ip = make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, v)
	} else {
		// IPv6: 16 bytes = four 32-bit little-endian words.
		// Byte-reverse within each 4-byte word; word order is already correct.
		if len(raw) != 16 {
			return nil, 0, fmt.Errorf("IPv6 addr wrong length %d", len(raw))
		}
		ip = make(net.IP, 16)
		for i := 0; i < 4; i++ {
			w := binary.LittleEndian.Uint32(raw[i*4 : i*4+4])
			binary.BigEndian.PutUint32(ip[i*4:], w)
		}
	}
	return ip, port, nil
}

// isLoopback returns true for 127.x.x.x and ::1.
func isLoopback(ip net.IP) bool {
	return ip.IsLoopback()
}

// isLinkLocal returns true for 169.254.0.0/16 and fe80::/10.
func isLinkLocal(ip net.IP) bool {
	return ip.IsLinkLocalUnicast()
}

// isIPv4MappedLoopback returns true for ::ffff:127.x.x.x.
func isIPv4MappedLoopback(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		// To4() strips the v4-mapped prefix, so this catches both forms.
		return ip4.IsLoopback()
	}
	return false
}

// shouldFilterRemote returns true if the remote address should be excluded from
// the connection table (loopback, link-local, or unspecified).
func shouldFilterRemote(ip net.IP) bool {
	return isLoopback(ip) || isIPv4MappedLoopback(ip) || isLinkLocal(ip) || ip.IsUnspecified()
}
