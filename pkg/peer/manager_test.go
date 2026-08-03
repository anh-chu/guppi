package peer

import (
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/state"
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
	localMgr := state.NewManager()
	return NewManager(id, peerStore, localMgr), id
}

// TestGetAllSessions_PointerMutation documents the current failing semantics:
// GetAllSessions mutates the shared session pointers in-place, so a caller
// that modifies a returned Session leaks that mutation back into the manager.
// A v2 redesign must return defensive copies.
func TestGetAllSessions_PointerMutation(t *testing.T) {
	mgr, _ := testManager(t)

	// Add a remote peer with one session.
	remoteID, err := identity.Generate("remote")
	if err != nil {
		t.Fatal(err)
	}
	fp := remoteID.Fingerprint()
	conn := NewPeerConnection(fp, 64)
	mgr.RegisterPeer(fp, "remote", remoteID.PublicKey, conn)
	mgr.UpdatePeerSessions(fp, []*model.Session{{Name: "remote-s1"}})

	all1 := mgr.GetAllSessions()
	if len(all1) != 1 {
		t.Fatalf("expected 1 remote session, got %d", len(all1))
	}

	// Mutate the returned slice — this is a consumer bug, but the manager
	// currently allows it to propagate.  Host is overwritten on every call,
	// so mutate a field that is not restamped.
	origName := all1[0].Name
	all1[0].Name = "mutated-name"

	all2 := mgr.GetAllSessions()
	if all2[0].Name == "mutated-name" {
		t.Logf("documented failing semantics: GetAllSessions shares pointers (%q -> %q)", origName, all2[0].Name)
	} else {
		t.Log("GetAllSessions returned defensive copies — semantics changed from current baseline")
	}
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
