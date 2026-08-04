package peer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/state"
)

// TestRemoteCatalogCacheReloadRestoresPeerFingerprint is a real regression
// proof for the identity-domain collision in LoadRemoteCatalogCache
// (pkg/peer/manager.go): after a real UpdateRemoteCatalog + persist +
// process-restart round trip through the REAL sidecar file on disk, the
// peer fingerprint used to route SendCommand/GetPeerConnection must be the
// ORIGINAL authenticated fingerprint the peer connected with -- not a
// re-derivation from the persisted OwnerID.
//
// OwnerIDFromFingerprint is a one-way, format-changing conversion (raw
// fingerprint bytes -> lowercase base32); LoadRemoteCatalogCache currently
// does peerID := string(c.Owner), which produces the base32 STRING form
// of the owner, not the original fingerprint bytes. This test uses a
// fingerprint that is deliberately NOT equal to its own OwnerID conversion
// (true for every real fingerprint, since the encodings differ) and proves
// that after reload, PeerIDForOwner(owner) no longer resolves to a peer ID
// that a live connection would ever present, breaking SendCommand/
// GetPeerConnection lookups for every remote-owned command after a
// restart.
func TestRemoteCatalogCacheReloadRestoresPeerFingerprint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, ".config", "termyard"), 0o700)

	storeDir := filepath.Join(dir, "v2store")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	realFingerprint := "nodeB-real-authenticated-fingerprint-base64url-form"
	owner := state.OwnerIDFromFingerprint(realFingerprint)
	if string(owner) == realFingerprint {
		t.Fatalf("test fixture invalid: OwnerIDFromFingerprint must change format, got owner==fingerprint (%q)", owner)
	}

	// First Manager instance: a live peer connects, its catalog is cached and
	// persisted to the real sidecar file via the real production path
	// (UpdateRemoteCatalog -> persistRemoteCatalogs -> Store.SaveRemoteCatalogs).
	id1, err := identity.Generate("node-a-1")
	if err != nil {
		t.Fatal(err)
	}
	ps1, err := identity.NewPeerStore()
	if err != nil {
		t.Fatal(err)
	}
	mgr1 := NewManager(id1, ps1, nil)
	store1, err := state.OpenStore(storeDir, "node-a", state.StoreOptions{Owner: state.NewOwnerID()})
	if err != nil {
		t.Fatal(err)
	}
	mgr1.SetRemoteStore(store1)

	conn := NewPeerConnection(realFingerprint, 8)
	mgr1.RegisterPeer(realFingerprint, "node-b", "", conn)
	mgr1.UpdateRemoteCatalog(realFingerprint, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
	})

	// Sanity: while the peer is actually connected, PeerIDForOwner resolves
	// correctly to the real fingerprint (this direction already works).
	if got := mgr1.PeerIDForOwner(owner); got != realFingerprint {
		t.Fatalf("sanity check failed: live PeerIDForOwner(owner) = %q, want %q", got, realFingerprint)
	}

	// Second Manager instance: simulates a process restart. No peer is
	// connected; the ONLY source of truth is the real sidecar file on disk
	// written by mgr1 above, loaded via the real production
	// LoadRemoteCatalogCache path.
	id2, err := identity.Generate("node-a-2")
	if err != nil {
		t.Fatal(err)
	}
	ps2, err := identity.NewPeerStore()
	if err != nil {
		t.Fatal(err)
	}
	mgr2 := NewManager(id2, ps2, nil)
	store2, err := state.OpenStore(storeDir, "node-a", state.StoreOptions{Owner: store1.Owner()})
	if err != nil {
		t.Fatal(err)
	}
	mgr2.SetRemoteStore(store2)

	if err := mgr2.LoadRemoteCatalogCache(); err != nil {
		t.Fatalf("LoadRemoteCatalogCache: %v", err)
	}

	gotPeerID := mgr2.PeerIDForOwner(owner)
	if gotPeerID != realFingerprint {
		t.Fatalf("after restart, PeerIDForOwner(%q) = %q, want the original authenticated fingerprint %q -- "+
			"LoadRemoteCatalogCache reconstructed the peer ID from the OwnerID's own (different-format) string "+
			"instead of persisting and restoring the real fingerprint, so no live peer connection (keyed by its "+
			"real fingerprint) can ever be resolved for this owner after a restart",
			owner, gotPeerID, realFingerprint)
	}
}
