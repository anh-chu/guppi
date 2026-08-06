package peer

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/state"
)

func TestUnregisterPeerConn_StaleCannotReplaceLive(t *testing.T) {
	mgr := makeTestManager(t)
	remoteID, err := identity.Generate("remote")
	if err != nil {
		t.Fatal(err)
	}
	fp := remoteID.Fingerprint()

	oldConn := NewPeerConnection(fp, 64)
	mgr.RegisterPeer(fp, "remote", remoteID.PublicKey, oldConn)

	// Replacement arrives before the old connection closes.
	newConn := NewPeerConnection(fp, 64)
	mgr.RegisterPeer(fp, "remote", remoteID.PublicKey, newConn)

	if mgr.GetPeerConnection(fp) != newConn {
		t.Fatal("expected new connection to be live")
	}

	// Old connection closes late; it must not unregister the replacement.
	mgr.UnregisterPeerConn(fp, oldConn)
	if mgr.GetPeerConnection(fp) != newConn {
		t.Fatal("stale connection unregistered the replacement")
	}

	// New connection closes with itself; it should unregister.
	mgr.UnregisterPeerConn(fp, newConn)
	if mgr.GetPeerConnection(fp) != nil {
		t.Fatal("expected no live connection after new conn closes")
	}
}

func TestRemoteCatalog_ReplaceNotMerge(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "remotea"
	owner := state.OwnerIDFromFingerprint(peerID) // owner must match the authenticated peer
	s1 := state.LocalSessionRecord{ID: "s1", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s1"}}
	s2 := state.LocalSessionRecord{ID: "s2", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s2"}}
	s3 := state.LocalSessionRecord{ID: "s3", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s3"}}

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "remotea", "", conn)

	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{s1, s2},
	})

	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 2,
		Sessions: []state.LocalSessionRecord{s3},
	})

	snap, ok := mgr.RemoteCatalogSnapshot(owner)
	if !ok {
		t.Fatal("expected remote catalog")
	}
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "s3" {
		t.Fatalf("expected snapshot replace to leave only s3, got %+v", snap.Sessions)
	}
}

func TestRemoteCatalog_PreservedThroughReconnect(t *testing.T) {
	mgr := makeTestManager(t)
	remoteID, err := identity.Generate("remote")
	if err != nil {
		t.Fatal(err)
	}
	fp := remoteID.Fingerprint()
	owner := state.OwnerIDFromFingerprint(fp)
	s1 := state.LocalSessionRecord{ID: "s1", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s1"}}

	conn := NewPeerConnection(fp, 64)
	mgr.RegisterPeer(fp, "remote", remoteID.PublicKey, conn)
	mgr.UpdateRemoteCatalog(fp, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{s1},
	})

	// Connection drops.
	mgr.UnregisterPeerConn(fp, conn)

	// Catalog must survive disconnect.
	snap, ok := mgr.RemoteCatalogSnapshot(owner)
	if !ok {
		t.Fatal("expected catalog to survive disconnect")
	}
	if len(snap.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(snap.Sessions))
	}

	// Replacement registers; old catalog is still visible until a newer
	// snapshot arrives.
	newConn := NewPeerConnection(fp, 64)
	mgr.RegisterPeer(fp, "remote", remoteID.PublicKey, newConn)
	snap, ok = mgr.RemoteCatalogSnapshot(owner)
	if !ok || len(snap.Sessions) != 1 {
		t.Fatal("replacement registration should preserve prior catalog")
	}

	// Newer snapshot overwrites.
	s2 := state.LocalSessionRecord{ID: "s2", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s2"}}
	mgr.UpdateRemoteCatalog(fp, newConn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 2,
		Sessions: []state.LocalSessionRecord{s2},
	})
	snap, _ = mgr.RemoteCatalogSnapshot(owner)
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "s2" {
		t.Fatalf("expected new snapshot to replace prior catalog, got %+v", snap.Sessions)
	}
}

func TestGetHosts_TwoHostsAfterRegister(t *testing.T) {
	mgr := makeTestManager(t)
	remoteID, err := identity.Generate("remote")
	if err != nil {
		t.Fatal(err)
	}
	fp := remoteID.Fingerprint()
	mgr.RegisterPeer(fp, "remote", remoteID.PublicKey, NewPeerConnection(fp, 64))

	hosts := mgr.GetHosts()
	if len(hosts) != 2 { // local + remote
		t.Fatalf("expected 2 hosts, got %d", len(hosts))
	}
}

func TestHandleCatalogSnapshot_UpdatesCache(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "remotea"
	// Owner must equal the authenticated peer identity (peerID).
	owner := state.OwnerIDFromFingerprint(peerID)
	sessionID := state.NewSessionID()
	s1 := state.LocalSessionRecord{ID: sessionID, Owner: owner, Ref: state.SessionRef{Owner: owner, Session: sessionID}}

	payload := CatalogSnapshotPayload{
		Owner:    owner,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{s1},
	}
	msg, err := NewMessage(MsgCatalogSnapshot, payload)
	if err != nil {
		t.Fatal(err)
	}
	pc := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "remotea", "", pc)
	deps := SessionDeps{Manager: mgr}

	// DEBUG: verify payload marshals/unmarshals correctly
	var p CatalogSnapshotPayload
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		t.Fatalf("payload unmarshal failed: %v", err)
	}
	if p.Owner != owner || len(p.Sessions) != 1 || p.Sessions[0].ID != sessionID {
		t.Fatalf("payload mismatch after unmarshal: owner=%v, sessions=%+v", p.Owner, p.Sessions)
	}

	handleCommandMessage(peerID, msg, pc, deps, testLogger(t))

	snap, ok := mgr.RemoteCatalogSnapshot(owner)
	if !ok {
		t.Fatal("expected cache update")
	}
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != sessionID {
		t.Fatalf("unexpected cached sessions: %+v", snap.Sessions)
	}
}

func TestHandleCommandReply_DeliversToWaiter(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peer-a"
	pc := NewPeerConnection(peerID, 8)
	done := make(chan commandResult, 1)
	mgr.registerCommandWaiter("cmd-1", peerID, pc, done)

	mgr.deliverCommandReply(peerID, pc, CommandReplyPayload{ID: "cmd-1", Handled: true})

	select {
	case res := <-done:
		if !res.payload.Handled {
			t.Fatal("expected handled")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

// TestHandleCommandReply_WrongPeerRejected proves that a reply arriving
// from a different authenticated peer (or a different connection for the
// same peer, e.g. a stale/superseded connection) cannot satisfy another
// peer's in-flight command waiter, even if it guesses/replays the exact
// CommandID.
func TestHandleCommandReply_WrongPeerRejected(t *testing.T) {
	mgr := makeTestManager(t)
	victimPeer := "peer-victim"
	victimConn := NewPeerConnection(victimPeer, 8)
	done := make(chan commandResult, 1)
	mgr.registerCommandWaiter("cmd-shared", victimPeer, victimConn, done)

	// A different authenticated peer attempts to satisfy the victim's waiter.
	attackerPeer := "peer-attacker"
	attackerConn := NewPeerConnection(attackerPeer, 8)
	if delivered := mgr.deliverCommandReply(attackerPeer, attackerConn, CommandReplyPayload{ID: "cmd-shared", Handled: true}); delivered {
		t.Fatal("expected reply from a different peer to be rejected")
	}

	// The same peer identity but a different (stale/superseded) connection
	// must also be rejected.
	staleConn := NewPeerConnection(victimPeer, 8)
	if delivered := mgr.deliverCommandReply(victimPeer, staleConn, CommandReplyPayload{ID: "cmd-shared", Handled: true}); delivered {
		t.Fatal("expected reply from a stale connection of the same peer to be rejected")
	}

	select {
	case <-done:
		t.Fatal("waiter must not receive a reply from the wrong peer/connection")
	case <-time.After(100 * time.Millisecond):
	}

	// The legitimate reply from the correct peer/connection still works.
	if delivered := mgr.deliverCommandReply(victimPeer, victimConn, CommandReplyPayload{ID: "cmd-shared", Handled: true}); !delivered {
		t.Fatal("expected reply from the correct peer/connection to be delivered")
	}
	select {
	case res := <-done:
		if !res.payload.Handled {
			t.Fatal("expected handled")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for legitimate reply")
	}
}

// Capability advertisement is covered by TestLocalCapabilities_AlwaysAdvertisesCanonicalCaps
// and TestPeerCapsSatisfyCanonical in capability_gate_test.go.

// TestLegacyPeer_NoCatalogFrame proves a peer that never advertises
// canonical capabilities gets no catalog slot frame.
func TestLegacyPeer_NoCatalogFrame(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "legacya"
	pc := NewPeerConnection(peerID, 8)
	pc.Caps = []string{CapPerStream, CapUpload} // no canonical caps
	mgr.RegisterPeer(peerID, "legacya", "", pc)

	deps := SessionDeps{Manager: mgr}

	// No catalog slot frame should be produced for legacy peers.
	sendInitialCatalog(pc, deps)
	select {
	case <-pc.LoLane():
		t.Fatal("unexpected catalog frame for legacy peer")
	case <-time.After(50 * time.Millisecond):
	}
}

func testLogger(t *testing.T) *logrus.Entry {
	return logrus.NewEntry(logrus.New())
}

// TestRemoteSnapshot_OwnerBindingEnforced proves a peer may publish under
// exactly one owner and no two peers may share an owner: a peer switching
// owners or a second peer claiming a bound owner is dropped.
func TestRemoteSnapshot_OwnerBindingEnforced(t *testing.T) {
	mgr := makeTestManager(t)
	idA, err := identity.Generate("peer-a")
	if err != nil {
		t.Fatal(err)
	}
	peerA := idA.Fingerprint()
	ownerA := state.OwnerIDFromFingerprint(peerA)

	idB, err := identity.Generate("peerb")
	if err != nil {
		t.Fatal(err)
	}
	peerB := idB.Fingerprint()
	ownerB := state.OwnerIDFromFingerprint(peerB)

	connA := NewPeerConnection(peerA, 64)
	mgr.RegisterPeer(peerA, "peera", idA.PublicKey, connA)

	connB := NewPeerConnection(peerB, 64)
	mgr.RegisterPeer(peerB, "peerb", idB.PublicKey, connB)

	// First snapshot from peerA binds ownerA.
	mgr.UpdateRemoteCatalog(peerA, connA, state.OwnerCatalogSnapshot{
		Owner:    ownerA,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{{ID: "s1", Owner: ownerA, Ref: state.SessionRef{Owner: ownerA, Session: "s1"}}},
	})
	if _, ok := mgr.RemoteCatalogSnapshot(ownerA); !ok {
		t.Fatal("expected peerA ownerA catalog cached")
	}

	// peerB claiming ownerA is a spoof -> dropped.
	mgr.UpdateRemoteWorkspace(peerB, connB, state.WorkspaceRecord{
		ID:       "layout1",
		Owner:    ownerA,
		Revision: 1,
		Tree:     state.Leaf(state.SessionRef{Owner: ownerA, Session: "s1"}),
	})
	if _, ok := mgr.RemoteWorkspaceSnapshot(ownerA); ok {
		t.Fatal("spoofed workspace under bound owner accepted")
	}
	mgr.UpdateRemoteCatalog(peerB, connB, state.OwnerCatalogSnapshot{Owner: ownerA, Revision: 1})
	if _, ok := mgr.RemoteCatalogSnapshot(ownerA); ok {
		// Still peerA's catalog; peerB's attempt must not replace it.
		snap, _ := mgr.RemoteCatalogSnapshot(ownerA)
		if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "s1" {
			t.Fatalf("peerB overwrote bound owner catalog: %+v", snap.Sessions)
		}
	}

	// peerB may bind its own distinct owner.
	mgr.UpdateRemoteCatalog(peerB, connB, state.OwnerCatalogSnapshot{Owner: ownerB, Revision: 1})
	if _, ok := mgr.RemoteCatalogSnapshot(ownerB); !ok {
		t.Fatal("expected peerB own-owner catalog cached")
	}

	// peerA switching to ownerB is rejected (one owner per peer).
	mgr.UpdateRemoteCatalog(peerA, connA, state.OwnerCatalogSnapshot{Owner: ownerB, Revision: 2})
	snap, _ := mgr.RemoteCatalogSnapshot(ownerB)
	if len(snap.Sessions) != 0 {
		t.Fatalf("peerA published under peerB's owner: %+v", snap.Sessions)
	}
}

func TestRemoteCatalog_StaleRevisionRejected(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)
	s1 := state.LocalSessionRecord{ID: "s1", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s1"}}
	s2 := state.LocalSessionRecord{ID: "s2", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s2"}}

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{Owner: owner, Revision: 1, Sessions: []state.LocalSessionRecord{s1}})

	// Equal revision (delayed duplicate) must not regress the cache.
	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{Owner: owner, Revision: 1, Sessions: []state.LocalSessionRecord{s2}})
	snap, _ := mgr.RemoteCatalogSnapshot(owner)
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "s1" {
		t.Fatalf("equal-revision snapshot regressed cache: %+v", snap.Sessions)
	}

	// Lower revision (stale/delayed) must be dropped too.
	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{Owner: owner, Revision: 0, Sessions: []state.LocalSessionRecord{s2}})
	snap, _ = mgr.RemoteCatalogSnapshot(owner)
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "s1" {
		t.Fatalf("lower-revision snapshot regressed cache: %+v", snap.Sessions)
	}

	// A new connection is a new generation: the baseline resets, so the same
	// numeric revision is accepted again.
	newConn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", newConn)
	mgr.UpdateRemoteCatalog(peerID, newConn, state.OwnerCatalogSnapshot{Owner: owner, Revision: 1, Sessions: []state.LocalSessionRecord{s2}})
	snap, ok := mgr.RemoteCatalogSnapshot(owner)
	if !ok || len(snap.Sessions) != 1 || snap.Sessions[0].ID != "s2" {
		t.Fatalf("reconnect baseline should accept revision 1 again, got %+v (ok=%v)", snap.Sessions, ok)
	}
}

func TestRemoteWorkspace_StaleRevisionRejected(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)
	leafA := state.Leaf(state.SessionRef{Owner: owner, Session: "s1"})
	leafB := state.Leaf(state.SessionRef{Owner: owner, Session: "s2"})

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	mgr.UpdateRemoteWorkspace(peerID, conn, state.WorkspaceRecord{ID: "layout1", Owner: owner, Revision: 5, Tree: leafA})

	// Same layout, equal revision -> rejected.
	mgr.UpdateRemoteWorkspace(peerID, conn, state.WorkspaceRecord{ID: "layout1", Owner: owner, Revision: 5, Tree: leafB})
	ws, _ := mgr.RemoteWorkspaceSnapshot(owner)
	if ws.Tree.Ref == nil || ws.Tree.Ref.Session != "s1" {
		t.Fatalf("equal-revision workspace regressed cache")
	}

	// Same layout, lower revision -> rejected.
	mgr.UpdateRemoteWorkspace(peerID, conn, state.WorkspaceRecord{ID: "layout1", Owner: owner, Revision: 4, Tree: leafB})
	ws, _ = mgr.RemoteWorkspaceSnapshot(owner)
	if ws.Tree.Ref == nil || ws.Tree.Ref.Session != "s1" {
		t.Fatalf("lower-revision workspace regressed cache")
	}

	// A different layout has its own revision baseline.
	mgr.UpdateRemoteWorkspace(peerID, conn, state.WorkspaceRecord{ID: "layout2", Owner: owner, Revision: 1, Tree: leafB})
	ws, ok := mgr.RemoteWorkspaceSnapshot(owner)
	if !ok || ws.ID != "layout2" {
		t.Fatalf("independent workspace revisions should be accepted: %+v", ws)
	}
}

func TestRemoveHost_ForgetsRemoteCatalogPersists(t *testing.T) {
	mgr := makeTestManager(t)
	dir := t.TempDir()
	store, err := state.OpenStore(dir, mgr.localID, state.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetRemoteStore(store)

	remoteID, err := identity.Generate("remote")
	if err != nil {
		t.Fatal(err)
	}
	fp := remoteID.Fingerprint()
	owner := state.OwnerIDFromFingerprint(fp)

	conn := NewPeerConnection(fp, 64)
	mgr.RegisterPeer(fp, "remote", remoteID.PublicKey, conn)
	s1 := state.LocalSessionRecord{ID: "s1", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s1"}}
	mgr.UpdateRemoteCatalog(fp, conn, state.OwnerCatalogSnapshot{Owner: owner, Revision: 1, Sessions: []state.LocalSessionRecord{s1}})

	if _, ok := mgr.RemoteCatalogSnapshot(owner); !ok {
		t.Fatal("expected cached catalog before forget")
	}

	mgr.RemoveHost(fp)
	if _, ok := mgr.RemoteCatalogSnapshot(owner); ok {
		t.Fatal("catalog should be cleared on RemoveHost")
	}

	// Simulated reload from the persisted sidecar: the forget must be durable
	// so the peer does not reappear after a restart.
	if err := mgr.LoadRemoteCatalogCache(); err != nil {
		t.Fatal(err)
	}
	if _, ok := mgr.RemoteCatalogSnapshot(owner); ok {
		t.Fatal("forgotten peer reappeared after reload")
	}
}

// TestRemoteSnapshot_SpoofedSessionOwnerRejected proves that a snapshot
// containing a session with embedded owner != snap.Owner is rejected.
func TestRemoteSnapshot_SpoofedSessionOwnerRejected(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)
	ownerSpoof := state.OwnerID("ownerx")
	sessionID := state.NewSessionID()

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	// Crafted snapshot with session owner != snap.Owner
	spoofedSession := state.LocalSessionRecord{
		ID:    sessionID,
		Owner: ownerSpoof, // SPOOF: different from snapshot owner
		Ref:   state.SessionRef{Owner: ownerSpoof, Session: sessionID},
	}
	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{spoofedSession},
	})

	// Snapshot must be rejected; no cache entry should exist
	if _, ok := mgr.RemoteCatalogSnapshot(owner); ok {
		t.Fatal("spoofed session owner snapshot was accepted")
	}
}

// TestRemoteSnapshot_SpoofedLayoutOwnerRejected proves that a snapshot
// containing a layout with embedded owner != snap.Owner is rejected.
func TestRemoteSnapshot_SpoofedLayoutOwnerRejected(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)
	ownerSpoof := state.OwnerID("ownerx")
	layoutID := state.NewLayoutID()

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	// Crafted snapshot with layout owner != snap.Owner
	spoofedLayout := state.LayoutRecord{
		ID:    layoutID,
		Owner: ownerSpoof, // SPOOF: different from snapshot owner
		Order: 0,
		Tree:  state.Leaf(state.SessionRef{Owner: owner, Session: "s1"}),
	}
	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
		Layouts:  []state.LayoutRecord{spoofedLayout},
	})

	// Snapshot must be rejected; no cache entry should exist
	if _, ok := mgr.RemoteCatalogSnapshot(owner); ok {
		t.Fatal("spoofed layout owner snapshot was accepted")
	}
}

// TestRemoteSnapshot_LayoutLeafForeignOwnerRejected proves that a layout leaf
// whose Ref.Session matches a real session ID belonging to snap.Owner but whose
// Ref.Owner is a different (foreign) owner is rejected. The leaf's owner must
// be bound to the snapshot owner, not just its session ID.
func TestRemoteSnapshot_LayoutLeafForeignOwnerRejected(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)
	ownerSpoof := state.OwnerID("ownerx")
	sessionID := state.NewSessionID()
	layoutID := state.NewLayoutID()

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	// Legitimate session owned by snap.Owner.
	realSession := state.LocalSessionRecord{
		ID:    sessionID,
		Owner: owner,
		Ref:   state.SessionRef{Owner: owner, Session: sessionID},
	}
	// Layout leaf claims a foreign owner while referencing the real session ID.
	spoofedLayout := state.LayoutRecord{
		ID:    layoutID,
		Owner: owner,
		Order: 0,
		Tree:  state.Leaf(state.SessionRef{Owner: ownerSpoof, Session: sessionID}), // SPOOF: leaf owner != snap.Owner
	}
	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{realSession},
		Layouts:  []state.LayoutRecord{spoofedLayout},
	})

	// Snapshot must be rejected; no cache entry should exist
	if _, ok := mgr.RemoteCatalogSnapshot(owner); ok {
		t.Fatal("catalog with foreign-owner layout leaf was accepted")
	}
}

// TestRemoteSnapshot_SessionRefOwnerMismatchRejected proves that a session
// record whose Owner matches snap.Owner but whose embedded Ref.Owner is a
// different owner is rejected.
func TestRemoteSnapshot_SessionRefOwnerMismatchRejected(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)
	ownerSpoof := state.OwnerID("ownerx")
	sessionID := state.NewSessionID()

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	// sess.Owner matches snap.Owner, but embedded Ref.Owner is spoofed.
	spoofedSession := state.LocalSessionRecord{
		ID:    sessionID,
		Owner: owner,                                                   // matches snap.Owner
		Ref:   state.SessionRef{Owner: ownerSpoof, Session: sessionID}, // SPOOF: Ref.Owner != sess.Owner
	}
	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{spoofedSession},
	})

	// Snapshot must be rejected; no cache entry should exist
	if _, ok := mgr.RemoteCatalogSnapshot(owner); ok {
		t.Fatal("session with mismatched Ref.Owner was accepted")
	}
}

// TestRemoteSnapshot_WellFormedAccepted proves that a snapshot with no owner
// mismatches (session Owner, session Ref.Owner, layout leaf Ref.Owner all
// bound to snap.Owner) is still accepted, guarding against false positives.
func TestRemoteSnapshot_WellFormedAccepted(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)
	sessionID := state.NewSessionID()
	layoutID := state.NewLayoutID()

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	sess := state.LocalSessionRecord{
		ID:    sessionID,
		Owner: owner,
		Ref:   state.SessionRef{Owner: owner, Session: sessionID},
	}
	layout := state.LayoutRecord{
		ID:    layoutID,
		Owner: owner,
		Order: 0,
		Tree:  state.Leaf(state.SessionRef{Owner: owner, Session: sessionID}),
	}
	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{sess},
		Layouts:  []state.LayoutRecord{layout},
	})

	// Snapshot must be accepted and cached under snap.Owner.
	snap, ok := mgr.RemoteCatalogSnapshot(owner)
	if !ok {
		t.Fatal("well-formed snapshot was rejected")
	}
	if len(snap.Sessions) != 1 || len(snap.Layouts) != 1 {
		t.Fatalf("expected 1 session and 1 layout in cache, got %d sessions, %d layouts", len(snap.Sessions), len(snap.Layouts))
	}
}

// TestRemoteWorkspace_SpoofedLeafOwnerRejected proves that a workspace
// containing a leaf SessionRef with embedded owner != rec.Owner is rejected.
func TestRemoteWorkspace_SpoofedLeafOwnerRejected(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)
	ownerSpoof := state.OwnerID("ownerx")

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	// Crafted leaf with owner != record owner
	spoofedRef := state.SessionRef{Owner: ownerSpoof, Session: "s1"}
	mgr.UpdateRemoteWorkspace(peerID, conn, state.WorkspaceRecord{
		ID:       "layout1",
		Owner:    owner,
		Revision: 1,
		Tree:     state.Leaf(spoofedRef), // SPOOF: leaf owner != record owner
	})

	// Workspace must be rejected; no cache entry should exist
	if _, ok := mgr.RemoteWorkspaceSnapshot(owner); ok {
		t.Fatal("spoofed leaf owner workspace was accepted")
	}
}

// TestRemoteSnapshot_OutOfOrderDelivery proves that revisions delivered
// out of order within one connection (10, 8, 11) result in 11 being accepted
// as authoritative, never regressing to 8.
func TestRemoteSnapshot_OutOfOrderDelivery(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	// Deliver revision 10
	s1 := state.LocalSessionRecord{ID: "s1", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s1"}}
	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 10,
		Sessions: []state.LocalSessionRecord{s1},
	})
	snap, _ := mgr.RemoteCatalogSnapshot(owner)
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "s1" {
		t.Fatalf("expected s1 in cache after rev 10")
	}

	// Deliver revision 8 (stale, out of order) -> must be rejected
	s2 := state.LocalSessionRecord{ID: "s2", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s2"}}
	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 8,
		Sessions: []state.LocalSessionRecord{s2},
	})
	snap, _ = mgr.RemoteCatalogSnapshot(owner)
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "s1" {
		t.Fatalf("stale revision 8 regressed cache: expected s1, got %+v", snap.Sessions)
	}

	// Deliver revision 11 (newer) -> must be accepted
	s3 := state.LocalSessionRecord{ID: "s3", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s3"}}
	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 11,
		Sessions: []state.LocalSessionRecord{s3},
	})
	snap, _ = mgr.RemoteCatalogSnapshot(owner)
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "s3" {
		t.Fatalf("revision 11 not accepted: got %+v", snap.Sessions)
	}
}

// TestRemoteSnapshot_NewGenerationAcceptsLowerRevision proves that a new peer
// connection (generation) allows the same revision counter to be re-accepted,
// and even lower revisions if they are authoritative from a fresh connection.
func TestRemoteSnapshot_NewGenerationAcceptsLowerRevision(t *testing.T) {
	mgr := makeTestManager(t)
	remoteID, err := identity.Generate("remote")
	if err != nil {
		t.Fatal(err)
	}
	fp := remoteID.Fingerprint()
	owner := state.OwnerIDFromFingerprint(fp)

	// First connection sends revision 100
	conn1 := NewPeerConnection(fp, 64)
	mgr.RegisterPeer(fp, "remote", remoteID.PublicKey, conn1)
	s1 := state.LocalSessionRecord{ID: "s1", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s1"}}
	mgr.UpdateRemoteCatalog(fp, conn1, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 100,
		Sessions: []state.LocalSessionRecord{s1},
	})

	// Reconnect with a new connection
	conn2 := NewPeerConnection(fp, 64)
	mgr.RegisterPeer(fp, "remote", remoteID.PublicKey, conn2)

	// Fresh connection sends revision 5 (lower than 100) -> should be accepted
	s2 := state.LocalSessionRecord{ID: "s2", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s2"}}
	mgr.UpdateRemoteCatalog(fp, conn2, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 5,
		Sessions: []state.LocalSessionRecord{s2},
	})

	snap, ok := mgr.RemoteCatalogSnapshot(owner)
	if !ok || len(snap.Sessions) != 1 || snap.Sessions[0].ID != "s2" {
		t.Fatalf("new generation should accept lower revision, got %+v (ok=%v)", snap.Sessions, ok)
	}
}
