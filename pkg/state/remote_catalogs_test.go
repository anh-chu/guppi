package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore_RemoteCatalogsPersistAndLoad(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, "node", StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	owner := NewOwnerID()
	sessionID := NewSessionID()
	entries := []RemoteCatalogCacheEntry{
		{
			PeerID: "peer-fingerprint-abc",
			Snapshot: OwnerCatalogSnapshot{
				Owner:    owner,
				Revision: 7,
				Sessions: []LocalSessionRecord{
					{ID: sessionID, Owner: owner, Ref: SessionRef{Owner: owner, Session: sessionID}},
				},
			},
		},
	}

	if err := s.SaveRemoteCatalogs(entries); err != nil {
		t.Fatalf("SaveRemoteCatalogs: %v", err)
	}

	// Ensure the sidecar exists and the local owner revision is unchanged.
	if _, err := os.Stat(filepath.Join(dir, "node.remote-catalogs.json")); err != nil {
		t.Fatalf("sidecar missing: %v", err)
	}
	if rev := s.Revision(); rev != 0 {
		t.Fatalf("local owner revision changed to %d", rev)
	}

	loaded, err := s.LoadRemoteCatalogs()
	if err != nil {
		t.Fatalf("LoadRemoteCatalogs: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 catalog, got %d", len(loaded))
	}
	if loaded[0].Snapshot.Revision != 7 || len(loaded[0].Snapshot.Sessions) != 1 {
		t.Fatalf("unexpected loaded catalog: %+v", loaded[0])
	}
	if loaded[0].PeerID != "peer-fingerprint-abc" {
		t.Fatalf("peer fingerprint not round-tripped: got %q, want %q", loaded[0].PeerID, "peer-fingerprint-abc")
	}
}

func TestStore_LoadRemoteCatalogs_MissingFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, "node", StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadRemoteCatalogs()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 0 {
		t.Fatalf("expected empty, got %d", len(loaded))
	}
}
