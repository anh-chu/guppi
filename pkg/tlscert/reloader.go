package tlscert

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// CertReloader watches TLS cert/key files and reloads them when they change.
// It implements the tls.Config.GetCertificate callback for hot-reload support.
type CertReloader struct {
	certPath, keyPath string
	mu                sync.RWMutex
	cert              *tls.Certificate
	fingerprint       string
	lastModCert       time.Time
	lastModKey        time.Time
}

// NewCertReloader creates a CertReloader and loads the initial certificate.
func NewCertReloader(certPath, keyPath string) (*CertReloader, error) {
	r := &CertReloader{
		certPath: certPath,
		keyPath:  keyPath,
	}
	if err := r.reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// GetCertificate returns the current certificate for use in tls.Config.GetCertificate.
func (r *CertReloader) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cert, nil
}

// GetClientCertificate returns the current certificate for use in tls.Config.GetClientCertificate.
func (r *CertReloader) GetClientCertificate(_ *tls.CertificateRequestInfo) (*tls.Certificate, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cert, nil
}

// Fingerprint returns the current SHA256 fingerprint of the leaf certificate.
func (r *CertReloader) Fingerprint() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.fingerprint
}

// Watch polls cert/key files at the given interval and reloads when they change.
// It blocks until the context is cancelled.
func (r *CertReloader) Watch(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log := logrus.WithField("component", "cert-reloader")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			certInfo, err := os.Stat(r.certPath)
			if err != nil {
				continue
			}
			keyInfo, err := os.Stat(r.keyPath)
			if err != nil {
				continue
			}

			r.mu.RLock()
			changed := certInfo.ModTime().After(r.lastModCert) || keyInfo.ModTime().After(r.lastModKey)
			r.mu.RUnlock()

			if !changed {
				continue
			}

			oldFP := r.Fingerprint()
			if err := r.reload(); err != nil {
				log.WithError(err).Warn("failed to reload TLS certificate, keeping current cert")
				continue
			}

			newFP := r.Fingerprint()
			if oldFP != newFP {
				log.WithField("fingerprint", newFP).Info("TLS certificate reloaded")
			}
		}
	}
}

// reload loads the cert/key pair from disk and updates the fingerprint.
func (r *CertReloader) reload() error {
	cert, err := tls.LoadX509KeyPair(r.certPath, r.keyPath)
	if err != nil {
		return err
	}

	// Parse the leaf to compute fingerprint
	if cert.Leaf == nil && len(cert.Certificate) > 0 {
		cert.Leaf, _ = x509.ParseCertificate(cert.Certificate[0])
	}

	var fp string
	if cert.Leaf != nil {
		// Fingerprint from raw DER bytes (matches existing Fingerprint function)
		derBytes := cert.Certificate[0]
		hash := sha256.Sum256(derBytes)
		fp = hex.EncodeToString(hash[:])
	} else {
		// Fallback: read the PEM and hash the DER block
		data, err := os.ReadFile(r.certPath)
		if err == nil {
			block, _ := pem.Decode(data)
			if block != nil {
				hash := sha256.Sum256(block.Bytes)
				fp = hex.EncodeToString(hash[:])
			}
		}
	}

	certInfo, _ := os.Stat(r.certPath)
	keyInfo, _ := os.Stat(r.keyPath)

	r.mu.Lock()
	r.cert = &cert
	r.fingerprint = fp
	if certInfo != nil {
		r.lastModCert = certInfo.ModTime()
	}
	if keyInfo != nil {
		r.lastModKey = keyInfo.ModTime()
	}
	r.mu.Unlock()

	return nil
}
