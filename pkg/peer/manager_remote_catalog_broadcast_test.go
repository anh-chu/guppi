package peer

import (
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/state"
)

// TestAllRemoteCatalogSnapshots_ReturnsEveryCachedOwner proves the read-only
// accessor the browser-facing aggregation (state.AggregateCatalog) depends on
// actually surfaces every owner Manager has accepted a snapshot from -- this
// is the piece that was previously never read outside of peer.Manager
// itself.
func TestAllRemoteCatalogSnapshots_ReturnsEveryCachedOwner(t *testing.T) {
	mgr := makeV2Manager(t)

	ownerA := state.OwnerIDFromFingerprint("peera")
	connA := NewPeerConnection("peera", 64)
	mgr.RegisterPeer("peera", "peer-a", "", connA)
	mgr.UpdateRemoteCatalog("peera", connA, state.OwnerCatalogSnapshot{
		Owner:    ownerA,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{
			{ID: "s1", Owner: ownerA, Ref: state.SessionRef{Owner: ownerA, Session: "s1"}},
		},
	})

	ownerB := state.OwnerIDFromFingerprint("peerb")
	connB := NewPeerConnection("peerb", 64)
	mgr.RegisterPeer("peerb", "peer-b", "", connB)
	mgr.UpdateRemoteCatalog("peerb", connB, state.OwnerCatalogSnapshot{
		Owner:    ownerB,
		Revision: 5,
		Sessions: []state.LocalSessionRecord{
			{ID: "s2", Owner: ownerB, Ref: state.SessionRef{Owner: ownerB, Session: "s2"}},
		},
	})

	all := mgr.AllRemoteCatalogSnapshots()
	if len(all) != 2 {
		t.Fatalf("expected 2 cached remote owners, got %d: %+v", len(all), all)
	}
	byOwner := map[state.OwnerID]state.OwnerCatalogSnapshot{}
	for _, s := range all {
		byOwner[s.Owner] = s
	}
	if got, ok := byOwner[ownerA]; !ok || got.Revision != 1 || len(got.Sessions) != 1 || got.Sessions[0].ID != "s1" {
		t.Fatalf("owner A snapshot wrong: %+v (ok=%v)", got, ok)
	}
	if got, ok := byOwner[ownerB]; !ok || got.Revision != 5 || len(got.Sessions) != 1 || got.Sessions[0].ID != "s2" {
		t.Fatalf("owner B snapshot wrong: %+v (ok=%v)", got, ok)
	}
}

// TestSubscribeRemoteCatalogs_NotifiesUpdateAndRemoval proves observers (the
// browser state stream) are told about both accepted updates AND explicit
// removals (peer forgotten), not merely able to poll -- removal must be a
// signal, not inferred from silence, for a live subscriber.
func TestSubscribeRemoteCatalogs_NotifiesUpdateAndRemoval(t *testing.T) {
	mgr := makeV2Manager(t)

	type event struct {
		owner   state.OwnerID
		rev     int64
		removed bool
	}
	events := make(chan event, 8)
	unsubscribe := mgr.SubscribeRemoteCatalogs(func(owner state.OwnerID, snap state.OwnerCatalogSnapshot, removed bool) {
		events <- event{owner: owner, rev: snap.Revision, removed: removed}
	})
	defer unsubscribe()

	owner := state.OwnerIDFromFingerprint("peerc")
	conn := NewPeerConnection("peerc", 64)
	mgr.RegisterPeer("peerc", "peer-c", "", conn)
	mgr.UpdateRemoteCatalog("peerc", conn, state.OwnerCatalogSnapshot{Owner: owner, Revision: 1})

	select {
	case e := <-events:
		if e.owner != owner || e.rev != 1 || e.removed {
			t.Fatalf("expected update event for owner %q rev 1, got %+v", owner, e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for update notification")
	}

	mgr.ForgetRemoteCatalog(owner)

	select {
	case e := <-events:
		if e.owner != owner || !e.removed {
			t.Fatalf("expected explicit removal event for owner %q, got %+v", owner, e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for removal notification")
	}

	// After unsubscribing, no further events should be delivered.
	unsubscribe()
	mgr.UpdateRemoteCatalog("peerc", conn, state.OwnerCatalogSnapshot{Owner: owner, Revision: 2})
	select {
	case e := <-events:
		t.Fatalf("expected no events after unsubscribe, got %+v", e)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestForgetRemoteCatalogsForPeer_NotifiesRemovalPerOwner proves the
// peer-disconnect path (forgetRemoteCatalogsForPeer, exercised indirectly via
// UnregisterPeer/RemovePeer flows) also emits an explicit removal signal per
// forgotten owner, not just the direct ForgetRemoteCatalog call.
func TestForgetRemoteCatalogsForPeer_NotifiesRemovalPerOwner(t *testing.T) {
	mgr := makeV2Manager(t)

	owner := state.OwnerIDFromFingerprint("peerd")
	conn := NewPeerConnection("peerd", 64)
	mgr.RegisterPeer("peerd", "peer-d", "", conn)
	mgr.UpdateRemoteCatalog("peerd", conn, state.OwnerCatalogSnapshot{Owner: owner, Revision: 1})

	removed := make(chan state.OwnerID, 4)
	unsubscribe := mgr.SubscribeRemoteCatalogs(func(o state.OwnerID, _ state.OwnerCatalogSnapshot, isRemoved bool) {
		if isRemoved {
			removed <- o
		}
	})
	defer unsubscribe()

	mgr.forgetRemoteCatalogsForPeer("peerd")

	select {
	case o := <-removed:
		if o != owner {
			t.Fatalf("expected removal for owner %q, got %q", owner, o)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for forgetRemoteCatalogsForPeer removal notification")
	}

	if _, ok := mgr.RemoteCatalogSnapshot(owner); ok {
		t.Fatal("expected catalog to be forgotten")
	}
}
