package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// RemoteCatalogsDocument is the sidecar format for persisted remote owner
// catalogs. It is intentionally separate from AppDocument so remote cache
// persistence never advances the local owner's revision.
type RemoteCatalogsDocument struct {
	Schema   int                       `json:"schema"`
	Catalogs []RemoteCatalogCacheEntry `json:"catalogs"`
}

// RemoteCatalogCacheEntry pairs a cached remote-owner catalog snapshot with
// the authenticated peer fingerprint it was received from. PeerID is the
// peer-connection identity (e.g. the TLS/noise handshake fingerprint
// string) and is NOT derivable from Snapshot.Owner: OwnerID lives in a
// separate, one-way-derived identifier space (see
// state.OwnerIDFromFingerprint), so the fingerprint must be persisted
// explicitly or the owner<->peer binding is lost across a restart.
type RemoteCatalogCacheEntry struct {
	PeerID   string               `json:"peer_id"`
	Snapshot OwnerCatalogSnapshot `json:"snapshot"`
}

const remoteCatalogsSchema = 2

// remoteCatalogsPath returns the sidecar path for remote catalog caches.
func (s *Store) remoteCatalogsPath() string {
	return filepath.Join(s.path, s.nodeID+".remote-catalogs.json")
}

// SaveRemoteCatalogs persists the supplied remote owner catalogs, together
// with the authenticated peer fingerprint each was received from, atomically.
// The local owner's catalog revision is unaffected.
func (s *Store) SaveRemoteCatalogs(entries []RemoteCatalogCacheEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc := RemoteCatalogsDocument{
		Schema:   remoteCatalogsSchema,
		Catalogs: make([]RemoteCatalogCacheEntry, len(entries)),
	}
	for i, e := range entries {
		doc.Catalogs[i] = RemoteCatalogCacheEntry{
			PeerID:   e.PeerID,
			Snapshot: cloneOwnerCatalogSnapshot(e.Snapshot),
		}
	}

	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal remote catalogs: %w", err)
	}

	pattern := s.nodeID + ".remote-catalogs.json.tmp-*"
	tmpPath, err := s.writeTemp(pattern, data)
	if err != nil {
		return fmt.Errorf("write remote catalogs temp: %w", err)
	}

	if err := s.renameHook(tmpPath, s.remoteCatalogsPath()); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename remote catalogs temp: %w", err)
	}
	if err := s.syncDirHook(s.path); err != nil {
		return fmt.Errorf("sync directory after remote catalogs: %w", err)
	}
	return nil
}

// cloneOwnerCatalogSnapshot returns a deep copy of c's slice fields so
// persisted/cached snapshots never alias caller-owned backing arrays.
func cloneOwnerCatalogSnapshot(c OwnerCatalogSnapshot) OwnerCatalogSnapshot {
	sessions := make([]LocalSessionRecord, len(c.Sessions))
	copy(sessions, c.Sessions)
	return OwnerCatalogSnapshot{
		Owner:    c.Owner,
		Revision: c.Revision,
		Sessions: sessions,
	}
}

// LoadRemoteCatalogs loads the persisted remote owner catalogs (with their
// bound peer fingerprints), returning an empty slice if the sidecar does
// not exist.
func (s *Store) LoadRemoteCatalogs() ([]RemoteCatalogCacheEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.remoteCatalogsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read remote catalogs: %w", err)
	}

	var doc RemoteCatalogsDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode remote catalogs: %w", err)
	}
	if doc.Schema != remoteCatalogsSchema {
		return nil, fmt.Errorf("unsupported remote catalogs schema %d", doc.Schema)
	}

	out := make([]RemoteCatalogCacheEntry, len(doc.Catalogs))
	for i, e := range doc.Catalogs {
		out[i] = RemoteCatalogCacheEntry{
			PeerID:   e.PeerID,
			Snapshot: cloneOwnerCatalogSnapshot(e.Snapshot),
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Snapshot.Owner < out[j].Snapshot.Owner })
	return out, nil
}
