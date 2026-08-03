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
	Schema   int                    `json:"schema"`
	Catalogs []OwnerCatalogSnapshot `json:"catalogs"`
}

const remoteCatalogsSchema = 1

// remoteCatalogsPath returns the sidecar path for remote catalog caches.
func (s *Store) remoteCatalogsPath() string {
	return filepath.Join(s.path, s.nodeID+".remote-catalogs.json")
}

// SaveRemoteCatalogs persists the supplied remote owner catalogs atomically.
// The local owner's catalog revision is unaffected.
func (s *Store) SaveRemoteCatalogs(catalogs []OwnerCatalogSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc := RemoteCatalogsDocument{
		Schema:   remoteCatalogsSchema,
		Catalogs: make([]OwnerCatalogSnapshot, len(catalogs)),
	}
	for i, c := range catalogs {
		doc.Catalogs[i] = cloneOwnerCatalogSnapshot(c)
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

// LoadRemoteCatalogs loads the persisted remote owner catalogs, returning an
// empty slice if the sidecar does not exist.
func cloneOwnerCatalogSnapshot(c OwnerCatalogSnapshot) OwnerCatalogSnapshot {
	sessions := make([]LocalSessionRecord, len(c.Sessions))
	copy(sessions, c.Sessions)
	layouts := make([]LayoutRecord, len(c.Layouts))
	copy(layouts, c.Layouts)
	return OwnerCatalogSnapshot{
		Owner:    c.Owner,
		Revision: c.Revision,
		Sessions: sessions,
		Layouts:  layouts,
	}
}

func (s *Store) LoadRemoteCatalogs() ([]OwnerCatalogSnapshot, error) {
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

	out := make([]OwnerCatalogSnapshot, len(doc.Catalogs))
	for i, c := range doc.Catalogs {
		out[i] = cloneOwnerCatalogSnapshot(c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Owner < out[j].Owner })
	return out, nil
}
