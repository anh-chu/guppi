package identity

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Peer represents a known paired peer
type Peer struct {
	Name       string    `json:"name"`
	PublicKey  string    `json:"public_key"`
	PairedAt   time.Time `json:"paired_at"`
	TLSCertPEM string   `json:"tls_cert_pem,omitempty"` // pinned TLS certificate from pairing
	CACertPEM  string   `json:"ca_cert_pem,omitempty"`  // node's CA certificate
}

// Fingerprint returns a short identifier derived from the peer's public key
func (p *Peer) Fingerprint() string {
	id := &Identity{PublicKey: p.PublicKey}
	return id.Fingerprint()
}

// TLSConfig returns a tls.Config that trusts the peer's certificate.
// Trust priority: CACertPEM (standard CA verification) > TLSCertPEM (pinned cert) > system defaults.
func (p *Peer) TLSConfig(insecure bool) *tls.Config {
	if insecure {
		return &tls.Config{InsecureSkipVerify: true}
	}
	if p.CACertPEM != "" {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM([]byte(p.CACertPEM)) {
			return &tls.Config{RootCAs: pool}
		}
	}
	if p.TLSCertPEM != "" {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM([]byte(p.TLSCertPEM)) {
			return &tls.Config{RootCAs: pool}
		}
	}
	return nil // use system defaults
}

// PeerStore manages the list of known peers
type PeerStore struct {
	mu    sync.RWMutex
	path  string
	store peerStoreData
}

type peerStoreData struct {
	Peers []Peer `json:"peers"`
}

// NewPeerStore loads or creates the peer store
func NewPeerStore() (*PeerStore, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}

	path := filepath.Join(dir, "peers.json")
	ps := &PeerStore{path: path}

	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &ps.store); err != nil {
			return nil, fmt.Errorf("parse peers: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read peers: %w", err)
	}

	return ps, nil
}

// Add adds a peer to the store and persists to disk
func (ps *PeerStore) Add(peer Peer) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	// Replace if public key already exists
	for i, p := range ps.store.Peers {
		if p.PublicKey == peer.PublicKey {
			ps.store.Peers[i] = peer
			return ps.save()
		}
	}

	ps.store.Peers = append(ps.store.Peers, peer)
	return ps.save()
}

// Remove removes a peer by name and persists to disk
func (ps *PeerStore) Remove(name string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	for i, p := range ps.store.Peers {
		if p.Name == name {
			ps.store.Peers = append(ps.store.Peers[:i], ps.store.Peers[i+1:]...)
			return ps.save()
		}
	}
	return fmt.Errorf("peer %q not found", name)
}

// Get returns a peer by name
func (ps *PeerStore) Get(name string) *Peer {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	for _, p := range ps.store.Peers {
		if p.Name == name {
			return &p
		}
	}
	return nil
}

// GetByPublicKey returns a peer by public key
func (ps *PeerStore) GetByPublicKey(publicKey string) *Peer {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	for _, p := range ps.store.Peers {
		if p.PublicKey == publicKey {
			return &p
		}
	}
	return nil
}

// List returns all known peers
func (ps *PeerStore) List() []Peer {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	result := make([]Peer, len(ps.store.Peers))
	copy(result, ps.store.Peers)
	return result
}

// UpdateTLSCert updates the pinned TLS certificate for a peer identified by
// public key and persists the change to disk.
func (ps *PeerStore) UpdateTLSCert(publicKey, certPEM string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	for i, p := range ps.store.Peers {
		if p.PublicKey == publicKey {
			ps.store.Peers[i].TLSCertPEM = certPEM
			return ps.save()
		}
	}
	return fmt.Errorf("peer with public key %q not found", publicKey[:16]+"...")
}

// save writes the peer store to disk (must be called with lock held)
func (ps *PeerStore) save() error {
	data, err := json.MarshalIndent(ps.store, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal peers: %w", err)
	}
	if err := os.WriteFile(ps.path, data, 0600); err != nil {
		return fmt.Errorf("write peers: %w", err)
	}
	return nil
}
