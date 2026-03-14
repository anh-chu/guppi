package tlscert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// generateTestCert writes a self-signed cert and key to the given paths.
func generateTestCert(t *testing.T, certPath, keyPath string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	certFile, err := os.Create(certPath)
	if err != nil {
		t.Fatal(err)
	}
	defer certFile.Close()
	pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyFile, err := os.Create(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer keyFile.Close()
	pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func TestNewCertReloader(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	generateTestCert(t, certPath, keyPath)

	r, err := NewCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("NewCertReloader: %v", err)
	}

	if r.Fingerprint() == "" {
		t.Error("expected non-empty fingerprint")
	}

	cert, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if cert == nil {
		t.Fatal("expected non-nil certificate")
	}
}

func TestNewCertReloader_BadFiles(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	_, err := NewCertReloader(certPath, keyPath)
	if err == nil {
		t.Fatal("expected error for missing files")
	}
}

func TestCertReloader_WatchReloads(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	generateTestCert(t, certPath, keyPath)

	r, err := NewCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("NewCertReloader: %v", err)
	}

	oldFP := r.Fingerprint()

	// Start watcher with short interval
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Watch(ctx, 50*time.Millisecond)

	// Wait a bit to ensure the mod time will differ
	time.Sleep(100 * time.Millisecond)

	// Replace cert with a new one
	generateTestCert(t, certPath, keyPath)

	// Wait for reload
	time.Sleep(200 * time.Millisecond)

	newFP := r.Fingerprint()
	if oldFP == newFP {
		t.Error("expected fingerprint to change after cert replacement")
	}
}

func TestCertReloader_GracefulBadReload(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	generateTestCert(t, certPath, keyPath)

	r, err := NewCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("NewCertReloader: %v", err)
	}

	originalFP := r.Fingerprint()

	// Start watcher
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Watch(ctx, 50*time.Millisecond)

	// Write garbage to cert file
	time.Sleep(100 * time.Millisecond)
	os.WriteFile(certPath, []byte("not a cert"), 0644)

	// Wait for reload attempt
	time.Sleep(200 * time.Millisecond)

	// Fingerprint should remain unchanged (old cert kept)
	if r.Fingerprint() != originalFP {
		t.Error("expected fingerprint to remain unchanged after bad cert write")
	}

	// Certificate should still be servable
	cert, err := r.GetCertificate(nil)
	if err != nil || cert == nil {
		t.Error("expected old certificate to still be available")
	}
}
