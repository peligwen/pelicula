package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// certSpec describes the parameters for generating a self-signed certificate.
type certSpec struct {
	cn          string   // Common Name
	dnsNames    []string // SAN DNS names
	ipAddresses []net.IP // SAN IP addresses
	certFile    string   // output path for the PEM certificate
	keyFile     string   // output path for the PEM private key
}

// generateSelfSignedCert creates an ECDSA P-256 self-signed certificate with a
// 10-year validity. Both output files are created or truncated. The parent
// directory must already exist.
func generateSelfSignedCert(spec certSpec) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: spec.cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     spec.dnsNames,
		IPAddresses:  spec.ipAddresses,
		IsCA:         true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return err
	}

	cf, err := os.OpenFile(spec.certFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer cf.Close()
	if err := pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return err
	}

	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return err
	}
	kf, err := os.OpenFile(spec.keyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer kf.Close()
	return pem.Encode(kf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER})
}

// SetupCert generates a self-signed TLS certificate for the pelicula LAN dashboard
// if one does not already exist under configDir/certs/.
// Uses ECDSA P-256 (more modern than RSA-2048) with a 10-year validity.
func SetupCert(configDir string) error {
	certsDir := filepath.Join(configDir, "certs")
	certFile := filepath.Join(certsDir, "pelicula.crt")
	keyFile := filepath.Join(certsDir, "pelicula.key")

	// Already exists — skip
	if _, err := os.Stat(certFile); err == nil {
		return nil
	}

	if err := os.MkdirAll(certsDir, 0755); err != nil {
		return err
	}

	if err := generateSelfSignedCert(certSpec{
		cn:          "pelicula",
		dnsNames:    []string{"localhost"},
		ipAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		certFile:    certFile,
		keyFile:     keyFile,
	}); err != nil {
		return err
	}

	pass("Generated TLS certificate")
	return nil
}

// SetupRemoteSelfSignedCert generates a self-signed TLS cert for the remote
// Peligrosa vhost. Always includes the host's RFC1918 LAN IPv4 addresses and
// 127.0.0.1 as Subject Alternative Names so native Jellyfin clients (iOS,
// Apple TV, Android TV, Roku) can connect to https://<lan-ip>:8920/ without
// silent TLS rejection.
//
// Idempotent: if a cert already exists at certDir/fullchain.pem, it is parsed
// and regenerated only when the SAN set drifts from the current host's LAN
// IPs (interface change, DHCP rebind, new VLAN). Operators don't need to
// hand-delete certs after a network change.
func SetupRemoteSelfSignedCert(certDir, hostname string) error {
	certFile := filepath.Join(certDir, "fullchain.pem")
	keyFile := filepath.Join(certDir, "privkey.pem")

	dnsNames, ipAddrs := remoteCertSANs(hostname)

	if _, err := os.Stat(certFile); err == nil {
		fresh, reason := certSANsCurrent(certFile, dnsNames, ipAddrs)
		if fresh {
			pass("Remote certificate exists")
			return nil
		}
		// Stale — fall through to regenerate. Remove key as well so the
		// new cert/key pair stays consistent.
		_ = os.Remove(certFile)
		_ = os.Remove(keyFile)
		pass("Remote certificate SANs out of date (" + reason + ") — regenerating")
	}

	if err := os.MkdirAll(certDir, 0755); err != nil {
		return err
	}

	if err := generateSelfSignedCert(certSpec{
		cn:          hostname,
		dnsNames:    dnsNames,
		ipAddresses: ipAddrs,
		certFile:    certFile,
		keyFile:     keyFile,
	}); err != nil {
		return err
	}

	pass("Generated self-signed remote certificate (" + hostname + ")")
	return nil
}

// remoteCertSANs returns the DNS names and IP SANs that the remote self-signed
// cert should include for the given hostname. Always adds 127.0.0.1 and
// every detected RFC1918 LAN IPv4 so native Jellyfin apps can reach the
// server via either localhost or its LAN IP.
func remoteCertSANs(hostname string) ([]string, []net.IP) {
	dnsNames := []string{hostname}
	if hostname != "localhost" {
		dnsNames = append(dnsNames, "localhost")
	}
	ips := []net.IP{net.ParseIP("127.0.0.1").To4()}
	for _, ip := range detectLANIPs() {
		ips = append(ips, ip)
	}
	return dnsNames, ips
}

// certSANsCurrent reports whether the cert at path covers every DNS name and
// IP in the wanted lists. Returns (true, "") if covered; (false, reason)
// otherwise. Read failures are treated as "not current" so the caller
// regenerates rather than silently keeping a broken cert.
func certSANsCurrent(path string, wantDNS []string, wantIPs []net.IP) (bool, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, "read failed"
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return false, "pem decode failed"
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false, "x509 parse failed"
	}

	have := make(map[string]struct{}, len(cert.DNSNames))
	for _, n := range cert.DNSNames {
		have[strings.ToLower(n)] = struct{}{}
	}
	for _, n := range wantDNS {
		if _, ok := have[strings.ToLower(n)]; !ok {
			return false, "missing DNS " + n
		}
	}

	haveIP := make(map[string]struct{}, len(cert.IPAddresses))
	for _, ip := range cert.IPAddresses {
		haveIP[ip.String()] = struct{}{}
	}
	for _, ip := range wantIPs {
		if _, ok := haveIP[ip.String()]; !ok {
			return false, "missing IP " + ip.String()
		}
	}
	return true, ""
}
