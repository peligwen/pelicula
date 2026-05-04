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
