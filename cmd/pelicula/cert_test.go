package main

import (
	"os"
	"testing"
)

func TestSetupCert_GeneratesFiles(t *testing.T) {
	certDir := t.TempDir()

	if err := SetupCert(certDir); err != nil {
		t.Fatalf("SetupCert: %v", err)
	}

	for _, name := range []string{"pelicula.crt", "pelicula.key"} {
		path := certDir + "/certs/" + name
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected file %q to exist: %v", path, err)
		}
	}
}

func TestSetupCert_Idempotent(t *testing.T) {
	certDir := t.TempDir()

	if err := SetupCert(certDir); err != nil {
		t.Fatalf("first SetupCert: %v", err)
	}

	// Stat the cert file before second call.
	certPath := certDir + "/certs/pelicula.crt"
	stat1, err := os.Stat(certPath)
	if err != nil {
		t.Fatalf("stat after first SetupCert: %v", err)
	}

	if err := SetupCert(certDir); err != nil {
		t.Fatalf("second SetupCert: %v", err)
	}

	stat2, err := os.Stat(certPath)
	if err != nil {
		t.Fatalf("stat after second SetupCert: %v", err)
	}

	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Error("cert was regenerated on second call — SetupCert should be idempotent")
	}
}
