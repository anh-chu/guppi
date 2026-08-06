package peer

import (
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/identity"
)

func testManager(t *testing.T) (*Manager, *identity.Identity) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	id, err := identity.LoadOrCreate("test-node")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	peerStore, err := identity.NewPeerStore()
	if err != nil {
		t.Fatalf("peer store: %v", err)
	}
	return NewManager(id, peerStore), id
}

// TestUnregisterPeer_Stale documents the current behavior when UnregisterPeer
// is called repeatedly or for a host that is already disconnected. Today it
// re-broadcasts and updates LastSeen each time; a v2 redesign should make it
// idempotent.
func TestUnregisterPeer_Stale(t *testing.T) {
	mgr, _ := testManager(t)

	remoteID, err := identity.Generate("remote")
	if err != nil {
		t.Fatal(err)
	}
	fp := remoteID.Fingerprint()
	conn := NewPeerConnection(fp, 64)

	mgr.RegisterPeer(fp, "remote", remoteID.PublicKey, conn)
	if !mgr.HasLiveConnection(fp) {
		t.Fatal("expected live connection after register")
	}

	mgr.UnregisterPeer(fp)
	if mgr.HasLiveConnection(fp) {
		t.Error("expected no live connection after unregister")
	}

	hosts := mgr.GetHosts()
	if len(hosts) == 0 {
		t.Fatal("expected host to remain in offline state")
	}
	before := hosts[0].LastSeen

	// A stale second unregister currently mutates state again.
	time.Sleep(5 * time.Millisecond)
	mgr.UnregisterPeer(fp)
	after := mgr.GetHosts()[0].LastSeen

	if after.Equal(before) {
		t.Log("second UnregisterPeer is now idempotent")
	} else {
		t.Logf("documented current semantics: stale UnregisterPeer still advances LastSeen (%v -> %v)", before, after)
	}
}
