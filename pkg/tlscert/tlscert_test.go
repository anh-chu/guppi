package tlscert

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateCA(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca-cert.pem")
	keyPath := filepath.Join(dir, "ca-key.pem")

	if err := generateCA(certPath, keyPath); err != nil {
		t.Fatalf("generateCA: %v", err)
	}

	// Verify cert file exists and is valid
	data, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read CA cert: %v", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("invalid PEM in CA cert")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}

	if !cert.IsCA {
		t.Error("CA cert should have IsCA=true")
	}
	if cert.Subject.CommonName != "guppi CA" {
		t.Errorf("unexpected CN: %s", cert.Subject.CommonName)
	}
	if cert.MaxPathLen != 0 || !cert.MaxPathLenZero {
		t.Error("CA should have MaxPathLen=0")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("CA should have KeyUsageCertSign")
	}

	// Verify key file exists
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("CA key file not found: %v", err)
	}
}

func TestGenerateWithCA(t *testing.T) {
	dir := t.TempDir()
	caCertPath := filepath.Join(dir, "ca-cert.pem")
	caKeyPath := filepath.Join(dir, "ca-key.pem")

	if err := generateCA(caCertPath, caKeyPath); err != nil {
		t.Fatalf("generateCA: %v", err)
	}

	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	if err := generateWithCA(certPath, keyPath, caCertPath, caKeyPath, []string{"localhost"}, nil); err != nil {
		t.Fatalf("generateWithCA: %v", err)
	}

	// Read cert file — should contain two PEM blocks (server + CA)
	data, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}

	var blocks []*pem.Block
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		blocks = append(blocks, block)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 PEM blocks in cert chain, got %d", len(blocks))
	}

	// First block is server cert
	serverCert, err := x509.ParseCertificate(blocks[0].Bytes)
	if err != nil {
		t.Fatalf("parse server cert: %v", err)
	}
	if serverCert.IsCA {
		t.Error("server cert should not be a CA")
	}
	if serverCert.Subject.CommonName != "guppi server" {
		t.Errorf("unexpected server CN: %s", serverCert.Subject.CommonName)
	}

	// Second block is CA cert
	caCert, err := x509.ParseCertificate(blocks[1].Bytes)
	if err != nil {
		t.Fatalf("parse CA cert in chain: %v", err)
	}
	if !caCert.IsCA {
		t.Error("second cert in chain should be CA")
	}

	// Verify the server cert is signed by the CA
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	_, err = serverCert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if err != nil {
		t.Errorf("server cert should be verifiable against CA: %v", err)
	}
}

func TestIsCertSignedByCA(t *testing.T) {
	dir := t.TempDir()
	caCertPath := filepath.Join(dir, "ca-cert.pem")
	caKeyPath := filepath.Join(dir, "ca-key.pem")
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	if err := generateCA(caCertPath, caKeyPath); err != nil {
		t.Fatalf("generateCA: %v", err)
	}
	if err := generateWithCA(certPath, keyPath, caCertPath, caKeyPath, []string{"localhost"}, nil); err != nil {
		t.Fatalf("generateWithCA: %v", err)
	}

	if !isCertSignedByCA(certPath, caCertPath) {
		t.Error("cert should be signed by CA")
	}

	// Generate a different CA and check it doesn't match
	otherCACertPath := filepath.Join(dir, "other-ca-cert.pem")
	otherCAKeyPath := filepath.Join(dir, "other-ca-key.pem")
	if err := generateCA(otherCACertPath, otherCAKeyPath); err != nil {
		t.Fatalf("generateCA (other): %v", err)
	}
	if isCertSignedByCA(certPath, otherCACertPath) {
		t.Error("cert should NOT be signed by a different CA")
	}
}

func TestLoadCACertPEM(t *testing.T) {
	dir := t.TempDir()
	caCertPath := filepath.Join(dir, "ca-cert.pem")
	caKeyPath := filepath.Join(dir, "ca-key.pem")

	if err := generateCA(caCertPath, caKeyPath); err != nil {
		t.Fatalf("generateCA: %v", err)
	}

	pemStr, err := LoadCACertPEM(caCertPath)
	if err != nil {
		t.Fatalf("LoadCACertPEM: %v", err)
	}
	if pemStr == "" {
		t.Fatal("expected non-empty PEM string")
	}

	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		t.Fatal("returned string is not valid PEM")
	}
}

func TestCertRotationWithSameCA(t *testing.T) {
	dir := t.TempDir()
	caCertPath := filepath.Join(dir, "ca-cert.pem")
	caKeyPath := filepath.Join(dir, "ca-key.pem")

	if err := generateCA(caCertPath, caKeyPath); err != nil {
		t.Fatalf("generateCA: %v", err)
	}

	// Generate first server cert
	cert1Path := filepath.Join(dir, "cert1.pem")
	key1Path := filepath.Join(dir, "key1.pem")
	if err := generateWithCA(cert1Path, key1Path, caCertPath, caKeyPath, []string{"localhost"}, nil); err != nil {
		t.Fatalf("generateWithCA (1): %v", err)
	}

	// Generate second server cert (simulating rotation)
	cert2Path := filepath.Join(dir, "cert2.pem")
	key2Path := filepath.Join(dir, "key2.pem")
	if err := generateWithCA(cert2Path, key2Path, caCertPath, caKeyPath, []string{"localhost"}, nil); err != nil {
		t.Fatalf("generateWithCA (2): %v", err)
	}

	// Both should be verifiable against the same CA
	if !isCertSignedByCA(cert1Path, caCertPath) {
		t.Error("first cert should be signed by CA")
	}
	if !isCertSignedByCA(cert2Path, caCertPath) {
		t.Error("second cert should be signed by CA")
	}
}
