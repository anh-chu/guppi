package peer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// generateSelfSignedCert creates a self-signed cert for testing.
func generateSelfSignedCert(t *testing.T) (certPEM string, cert *x509.Certificate) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}

	block := &pem.Block{Type: "CERTIFICATE", Bytes: certDER}
	pemBytes := pem.EncodeToMemory(block)

	return string(pemBytes), parsed
}

func TestEncodeCertPEM(t *testing.T) {
	certPEM, cert := generateSelfSignedCert(t)

	encoded := encodeCertPEM(cert)
	if encoded != certPEM {
		t.Error("encodeCertPEM output does not match original PEM")
	}
}

func TestEncodeCertPEM_RoundTrip(t *testing.T) {
	_, cert := generateSelfSignedCert(t)

	encoded := encodeCertPEM(cert)

	// Decode and verify we get the same cert back
	block, _ := pem.Decode([]byte(encoded))
	if block == nil {
		t.Fatal("failed to decode PEM")
	}

	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("failed to parse round-tripped cert: %v", err)
	}

	if !parsed.Equal(cert) {
		t.Error("round-tripped certificate does not match original")
	}
}

func TestIsSystemTrusted_SelfSigned(t *testing.T) {
	_, cert := generateSelfSignedCert(t)

	cs := tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
	}

	if isSystemTrusted(cs) {
		t.Error("self-signed cert should not be system-trusted")
	}
}

func TestIsSystemTrusted_NoCerts(t *testing.T) {
	cs := tls.ConnectionState{}
	if isSystemTrusted(cs) {
		t.Error("empty cert list should not be system-trusted")
	}
}

func TestClientTLSConfig_Insecure(t *testing.T) {
	c := &Client{insecure: true}
	cfg := c.tlsConfig()
	if cfg == nil || !cfg.InsecureSkipVerify {
		t.Error("insecure client should have InsecureSkipVerify=true")
	}
	if cfg.VerifyConnection != nil {
		t.Error("insecure client should not have VerifyConnection callback")
	}
}
