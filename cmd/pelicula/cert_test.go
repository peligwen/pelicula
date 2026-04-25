package main

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// withDetectLANIPs swaps the package-level detector and returns a cleanup.
func withDetectLANIPs(t *testing.T, ips []net.IP) func() {
	t.Helper()
	prev := detectLANIPsFn
	detectLANIPsFn = func() []net.IP {
		out := make([]net.IP, len(ips))
		copy(out, ips)
		return out
	}
	return func() { detectLANIPsFn = prev }
}

func parseCertSANs(t *testing.T, path string) (dns []string, ips []net.IP) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatalf("pem decode failed")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("x509 parse: %v", err)
	}
	return cert.DNSNames, cert.IPAddresses
}

func TestSetupRemoteSelfSignedCert_SimpleMode_IncludesLANIPs(t *testing.T) {
	cleanup := withDetectLANIPs(t, []net.IP{net.ParseIP("192.168.1.42").To4()})
	defer cleanup()

	certDir := t.TempDir()
	if err := SetupRemoteSelfSignedCert(certDir, "pelicula-remote"); err != nil {
		t.Fatalf("SetupRemoteSelfSignedCert: %v", err)
	}

	dns, ips := parseCertSANs(t, filepath.Join(certDir, "fullchain.pem"))

	wantDNS := map[string]bool{"pelicula-remote": false, "localhost": false}
	for _, n := range dns {
		if _, ok := wantDNS[n]; ok {
			wantDNS[n] = true
		}
	}
	for n, seen := range wantDNS {
		if !seen {
			t.Errorf("DNS SAN missing %q (have %v)", n, dns)
		}
	}

	wantIPs := map[string]bool{"127.0.0.1": false, "192.168.1.42": false}
	for _, ip := range ips {
		if _, ok := wantIPs[ip.String()]; ok {
			wantIPs[ip.String()] = true
		}
	}
	for ip, seen := range wantIPs {
		if !seen {
			t.Errorf("IP SAN missing %s (have %v)", ip, ips)
		}
	}
}

func TestSetupRemoteSelfSignedCert_FullMode_IncludesHostnameAndLANIPs(t *testing.T) {
	cleanup := withDetectLANIPs(t, []net.IP{net.ParseIP("10.0.0.7").To4()})
	defer cleanup()

	certDir := t.TempDir()
	if err := SetupRemoteSelfSignedCert(certDir, "media.example.com"); err != nil {
		t.Fatalf("SetupRemoteSelfSignedCert: %v", err)
	}

	dns, ips := parseCertSANs(t, filepath.Join(certDir, "fullchain.pem"))

	hasDNS := func(n string) bool {
		for _, x := range dns {
			if x == n {
				return true
			}
		}
		return false
	}
	if !hasDNS("media.example.com") {
		t.Errorf("DNS SAN missing hostname (have %v)", dns)
	}
	if !hasDNS("localhost") {
		t.Errorf("DNS SAN missing localhost (have %v)", dns)
	}

	hasIP := func(s string) bool {
		for _, ip := range ips {
			if ip.String() == s {
				return true
			}
		}
		return false
	}
	if !hasIP("127.0.0.1") {
		t.Errorf("IP SAN missing 127.0.0.1 (have %v)", ips)
	}
	if !hasIP("10.0.0.7") {
		t.Errorf("IP SAN missing LAN IP 10.0.0.7 (have %v)", ips)
	}
}

func TestSetupRemoteSelfSignedCert_NoLANIPs_StillIncludesLoopback(t *testing.T) {
	// Hosts with no RFC1918 interface (CI sandboxes, --network=none) must still
	// produce a usable cert; assert at minimum 127.0.0.1 + the hostname/localhost
	// DNS names land in the SAN.
	cleanup := withDetectLANIPs(t, nil)
	defer cleanup()

	certDir := t.TempDir()
	if err := SetupRemoteSelfSignedCert(certDir, "pelicula-remote"); err != nil {
		t.Fatalf("SetupRemoteSelfSignedCert: %v", err)
	}

	_, ips := parseCertSANs(t, filepath.Join(certDir, "fullchain.pem"))
	found := false
	for _, ip := range ips {
		if ip.String() == "127.0.0.1" {
			found = true
		}
	}
	if !found {
		t.Errorf("IP SAN missing 127.0.0.1 even with no LAN IPs (have %v)", ips)
	}
}

func TestSetupRemoteSelfSignedCert_StaleCertRegenerated(t *testing.T) {
	// First pass: cert generated with one LAN IP.
	cleanup1 := withDetectLANIPs(t, []net.IP{net.ParseIP("192.168.1.10").To4()})
	certDir := t.TempDir()
	if err := SetupRemoteSelfSignedCert(certDir, "pelicula-remote"); err != nil {
		t.Fatalf("first SetupRemoteSelfSignedCert: %v", err)
	}
	_, ips1 := parseCertSANs(t, filepath.Join(certDir, "fullchain.pem"))
	if len(ips1) == 0 {
		t.Fatal("expected ips on first cert")
	}
	cleanup1()

	// Second pass: LAN IP changed (DHCP rebind, new VLAN). Cert must regen.
	cleanup2 := withDetectLANIPs(t, []net.IP{net.ParseIP("10.0.0.99").To4()})
	defer cleanup2()
	if err := SetupRemoteSelfSignedCert(certDir, "pelicula-remote"); err != nil {
		t.Fatalf("second SetupRemoteSelfSignedCert: %v", err)
	}
	_, ips2 := parseCertSANs(t, filepath.Join(certDir, "fullchain.pem"))

	hasIP := func(slice []net.IP, s string) bool {
		for _, ip := range slice {
			if ip.String() == s {
				return true
			}
		}
		return false
	}
	if !hasIP(ips2, "10.0.0.99") {
		t.Errorf("regenerated cert missing new LAN IP (have %v)", ips2)
	}
	if hasIP(ips2, "192.168.1.10") {
		t.Errorf("regenerated cert still contains old LAN IP (have %v)", ips2)
	}
}

func TestSetupRemoteSelfSignedCert_FreshCertNotRegenerated(t *testing.T) {
	cleanup := withDetectLANIPs(t, []net.IP{net.ParseIP("192.168.1.55").To4()})
	defer cleanup()

	certDir := t.TempDir()
	if err := SetupRemoteSelfSignedCert(certDir, "pelicula-remote"); err != nil {
		t.Fatalf("first SetupRemoteSelfSignedCert: %v", err)
	}
	certFile := filepath.Join(certDir, "fullchain.pem")
	stat1, err := os.Stat(certFile)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Second pass — same LAN IP set; must NOT regenerate (mtime stable).
	if err := SetupRemoteSelfSignedCert(certDir, "pelicula-remote"); err != nil {
		t.Fatalf("second SetupRemoteSelfSignedCert: %v", err)
	}
	stat2, err := os.Stat(certFile)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Errorf("cert was regenerated despite SANs being current")
	}
}

func TestCertSANsCurrent_DetectsMissing(t *testing.T) {
	cleanup := withDetectLANIPs(t, []net.IP{net.ParseIP("192.168.1.1").To4()})
	defer cleanup()

	certDir := t.TempDir()
	if err := SetupRemoteSelfSignedCert(certDir, "pelicula-remote"); err != nil {
		t.Fatalf("SetupRemoteSelfSignedCert: %v", err)
	}
	certFile := filepath.Join(certDir, "fullchain.pem")

	if ok, _ := certSANsCurrent(certFile, []string{"pelicula-remote", "localhost"}, []net.IP{net.ParseIP("127.0.0.1").To4(), net.ParseIP("192.168.1.1").To4()}); !ok {
		t.Errorf("expected current SANs to be reported as fresh")
	}
	if ok, reason := certSANsCurrent(certFile, []string{"pelicula-remote"}, []net.IP{net.ParseIP("172.16.0.1").To4()}); ok {
		t.Errorf("expected stale detection for missing IP, got fresh (%s)", reason)
	}
}
