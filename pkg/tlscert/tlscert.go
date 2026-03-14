package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

var defaultDNSNames = []string{"localhost"}
var defaultIPs = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}

func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "guppi", "tls"), nil
}

// ParseSANs parses a comma-separated list of SANs into DNS names and IPs,
// merged with the defaults (localhost, 127.0.0.1, ::1).
func ParseSANs(sans []string) (dnsNames []string, ips []net.IP) {
	dnsSet := make(map[string]bool)
	ipSet := make(map[string]bool)

	for _, d := range defaultDNSNames {
		dnsSet[d] = true
	}
	for _, ip := range defaultIPs {
		ipSet[ip.String()] = true
	}

	for _, san := range sans {
		san = strings.TrimSpace(san)
		if san == "" {
			continue
		}
		if ip := net.ParseIP(san); ip != nil {
			ipSet[ip.String()] = true
		} else {
			dnsSet[san] = true
		}
	}

	for d := range dnsSet {
		dnsNames = append(dnsNames, d)
	}
	sort.Strings(dnsNames)

	for ipStr := range ipSet {
		ips = append(ips, net.ParseIP(ipStr))
	}

	return dnsNames, ips
}

// LoadOrGenerateCA loads an existing CA from ~/.config/guppi/tls/ or generates
// a new ECDSA P-256 CA certificate with a 10-year lifetime.
func LoadOrGenerateCA() (caCertPath, caKeyPath string, err error) {
	dir, err := configDir()
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}

	caCertPath = filepath.Join(dir, "ca-cert.pem")
	caKeyPath = filepath.Join(dir, "ca-key.pem")

	if fileExists(caCertPath) && fileExists(caKeyPath) {
		// Validate existing CA
		data, err := os.ReadFile(caCertPath)
		if err != nil {
			return "", "", fmt.Errorf("read CA cert: %w", err)
		}
		block, _ := pem.Decode(data)
		if block == nil {
			return "", "", fmt.Errorf("invalid CA cert PEM")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", "", fmt.Errorf("parse CA cert: %w", err)
		}
		if !cert.IsCA {
			return "", "", fmt.Errorf("existing CA cert is not a CA")
		}
		if time.Now().After(cert.NotAfter) {
			logrus.Warn("CA certificate has expired, regenerating")
		} else {
			remaining := time.Until(cert.NotAfter)
			if remaining < 365*24*time.Hour {
				logrus.WithField("expires", cert.NotAfter.Format("2006-01-02")).
					Warn("CA certificate has less than 1 year remaining")
			}
			logrus.Debug("using existing CA certificate")
			return caCertPath, caKeyPath, nil
		}
	}

	logrus.Info("generating CA certificate (10-year lifetime)")
	if err := generateCA(caCertPath, caKeyPath); err != nil {
		return "", "", err
	}
	return caCertPath, caKeyPath, nil
}

// generateCA creates a new ECDSA P-256 CA certificate with a 10-year lifetime.
func generateCA(caCertPath, caKeyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"guppi"},
			CommonName:   "guppi CA",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour), // 10 years
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}

	certFile, err := os.OpenFile(caCertPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer certFile.Close()
	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyFile, err := os.OpenFile(caKeyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer keyFile.Close()
	if err := pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		return err
	}

	logrus.Info("CA certificate generated (valid for 10 years)")
	return nil
}

// generateWithCA generates a server certificate signed by the given CA and writes
// the full chain (server cert + CA cert) to certPath.
func generateWithCA(certPath, keyPath, caCertPath, caKeyPath string, dnsNames []string, ips []net.IP) error {
	// Load CA cert and key
	caCertPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return fmt.Errorf("read CA cert: %w", err)
	}
	caBlock, _ := pem.Decode(caCertPEM)
	if caBlock == nil {
		return fmt.Errorf("invalid CA cert PEM")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return fmt.Errorf("parse CA cert: %w", err)
	}

	caKeyPEM, err := os.ReadFile(caKeyPath)
	if err != nil {
		return fmt.Errorf("read CA key: %w", err)
	}
	caKeyBlock, _ := pem.Decode(caKeyPEM)
	if caKeyBlock == nil {
		return fmt.Errorf("invalid CA key PEM")
	}
	caKey, err := x509.ParseECPrivateKey(caKeyBlock.Bytes)
	if err != nil {
		return fmt.Errorf("parse CA key: %w", err)
	}

	// Generate server key
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"guppi"},
			CommonName:   "guppi server",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour), // 1 year
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}

	// Sign with CA
	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		return err
	}

	// Write full chain: server cert + CA cert
	certFile, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer certFile.Close()
	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return err
	}
	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw}); err != nil {
		return err
	}

	// Write server key
	keyDER, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		return err
	}
	keyFile, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer keyFile.Close()
	if err := pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		return err
	}

	sanList := append(dnsNames, ipStrings(ips)...)
	logrus.WithField("sans", strings.Join(sanList, ", ")).Info("CA-signed TLS certificate generated (valid for 1 year)")
	return nil
}

// LoadCACertPEM reads a CA certificate file and returns its PEM content as a string.
func LoadCACertPEM(caCertPath string) (string, error) {
	data, err := os.ReadFile(caCertPath)
	if err != nil {
		return "", err
	}
	// Validate it's a proper PEM
	block, _ := pem.Decode(data)
	if block == nil {
		return "", fmt.Errorf("invalid PEM in %s", caCertPath)
	}
	return string(data), nil
}

// LoadOrGenerate loads existing TLS cert/key from ~/.config/guppi/tls/ or
// generates a new CA-signed certificate if none exists.
// Extra SANs are merged with defaults. If SANs changed from the existing cert,
// the cert is regenerated.
// Returns the cert/key paths and the CA certificate PEM (empty if no CA was used).
func LoadOrGenerate(extraSANs []string) (certPath, keyPath, caCertPEM string, err error) {
	dir, err := configDir()
	if err != nil {
		return "", "", "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", "", err
	}

	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	dnsNames, ips := ParseSANs(extraSANs)

	// Generate or load CA
	caCertPath, caKeyPath, err := LoadOrGenerateCA()
	if err != nil {
		return "", "", "", fmt.Errorf("CA setup: %w", err)
	}

	caCertPEM, err = LoadCACertPEM(caCertPath)
	if err != nil {
		return "", "", "", fmt.Errorf("load CA cert PEM: %w", err)
	}

	// Check if existing server cert is valid
	if fileExists(certPath) && fileExists(keyPath) {
		valid, matchesSANs := isCertValidWithSANs(certPath, dnsNames, ips)
		if valid && matchesSANs && isCertSignedByCA(certPath, caCertPath) {
			logrus.Debug("using existing TLS certificate")
			return certPath, keyPath, caCertPEM, nil
		}
		if !valid {
			logrus.Info("TLS certificate expired or invalid, regenerating")
		} else if !matchesSANs {
			logrus.Info("TLS SANs changed, regenerating certificate")
		} else {
			logrus.Info("TLS certificate not signed by current CA, regenerating")
			logrus.Warn("existing peers should re-pair to receive the new CA certificate")
		}
	}

	logrus.Info("generating CA-signed TLS certificate")
	if err := generateWithCA(certPath, keyPath, caCertPath, caKeyPath, dnsNames, ips); err != nil {
		return "", "", "", err
	}
	return certPath, keyPath, caCertPEM, nil
}

// LoadTLSConfig loads a tls.Config from cert and key paths.
func LoadTLSConfig(certPath, keyPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// LoadTLSConfigWithReloader loads a tls.Config that uses a CertReloader for
// dynamic certificate hot-reload. The returned CertReloader should have its
// Watch method called in a goroutine to enable automatic reloading.
func LoadTLSConfigWithReloader(certPath, keyPath string) (*tls.Config, *CertReloader, error) {
	reloader, err := NewCertReloader(certPath, keyPath)
	if err != nil {
		return nil, nil, err
	}
	tlsCfg := &tls.Config{
		GetCertificate: reloader.GetCertificate,
		MinVersion:     tls.VersionTLS12,
	}
	return tlsCfg, reloader, nil
}

// Fingerprint returns the hex-encoded SHA256 fingerprint of the leaf certificate
// in the given PEM file. Returns empty string on error.
func Fingerprint(certPath string) string {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return ""
	}
	hash := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(hash[:])
}

func generate(certPath, keyPath string, dnsNames []string, ips []net.IP) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"guppi"},
			CommonName:   "guppi self-signed",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour), // 1 year
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}

	// Write cert
	certFile, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer certFile.Close()
	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return err
	}

	// Write key
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyFile, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer keyFile.Close()
	if err := pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		return err
	}

	sanList := append(dnsNames, ipStrings(ips)...)
	logrus.WithField("sans", strings.Join(sanList, ", ")).Info("TLS certificate generated (valid for 1 year)")
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// isCertValidWithSANs checks expiry (>7 days remaining) and whether the cert's
// SANs match the requested set.
func isCertValidWithSANs(certPath string, wantDNS []string, wantIPs []net.IP) (valid bool, sansMatch bool) {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return false, false
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return false, false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false, false
	}

	// Check expiry (invalid if within 7 days)
	valid = time.Now().Add(7 * 24 * time.Hour).Before(cert.NotAfter)

	// Check SANs match
	certDNS := make([]string, len(cert.DNSNames))
	copy(certDNS, cert.DNSNames)
	sort.Strings(certDNS)

	wantDNSSorted := make([]string, len(wantDNS))
	copy(wantDNSSorted, wantDNS)
	sort.Strings(wantDNSSorted)

	if !slices.Equal(certDNS, wantDNSSorted) {
		return valid, false
	}

	certIPStrs := ipStrings(cert.IPAddresses)
	wantIPStrs := ipStrings(wantIPs)
	sort.Strings(certIPStrs)
	sort.Strings(wantIPStrs)

	if !slices.Equal(certIPStrs, wantIPStrs) {
		return valid, false
	}

	return valid, true
}

// isCertSignedByCA checks if the server cert was signed by the given CA.
func isCertSignedByCA(certPath, caCertPath string) bool {
	certData, err := os.ReadFile(certPath)
	if err != nil {
		return false
	}
	caData, err := os.ReadFile(caCertPath)
	if err != nil {
		return false
	}

	block, _ := pem.Decode(certData)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		return false
	}

	_, err = cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	return err == nil
}

func ipStrings(ips []net.IP) []string {
	s := make([]string, len(ips))
	for i, ip := range ips {
		s[i] = ip.String()
	}
	return s
}
