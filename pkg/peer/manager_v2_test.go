package peer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/state"
)

func makeV2Manager(t *testing.T) *Manager {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	_ = os.MkdirAll(filepath.Join(t.TempDir(), ".config", "termyard"), 0o700)
	id, err := identity.Generate("test-node")
	if err != nil {
		t.Fatal(err)
	}
	ps, err := identity.NewPeerStore()
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(id, ps, state.NewManager())
}

func TestUnregisterPeerConn_StaleCannotReplaceLive(t *testing.T) {
	mgr := makeV2Manager(t)
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
	mgr := makeV2Manager(t)
	peerID := "remote-a"
	owner := state.OwnerID(peerID) // owner must match the authenticated peer
	s1 := state.LocalSessionRecord{ID: "s1", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s1"}}
	s2 := state.LocalSessionRecord{ID: "s2", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s2"}}
	s3 := state.LocalSessionRecord{ID: "s3", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s3"}}

	mgr.UpdateRemoteCatalog(peerID, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{s1, s2},
	})

	mgr.UpdateRemoteCatalog(peerID, state.OwnerCatalogSnapshot{
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
	mgr := makeV2Manager(t)
	remoteID, err := identity.Generate("remote")
	if err != nil {
		t.Fatal(err)
	}
	fp := remoteID.Fingerprint()
	owner := state.OwnerID(fp)
	s1 := state.LocalSessionRecord{ID: "s1", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s1"}}

	conn := NewPeerConnection(fp, 64)
	mgr.RegisterPeer(fp, "remote", remoteID.PublicKey, conn)
	mgr.UpdateRemoteCatalog(fp, state.OwnerCatalogSnapshot{
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
	mgr.UpdateRemoteCatalog(fp, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 2,
		Sessions: []state.LocalSessionRecord{s2},
	})
	snap, _ = mgr.RemoteCatalogSnapshot(owner)
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "s2" {
		t.Fatalf("expected new snapshot to replace prior catalog, got %+v", snap.Sessions)
	}
}

func TestGetAllSessions_DefensiveCopy(t *testing.T) {
	mgr := makeV2Manager(t)
	remoteID, err := identity.Generate("remote")
	if err != nil {
		t.Fatal(err)
	}
	fp := remoteID.Fingerprint()
	mgr.RegisterPeer(fp, "remote", remoteID.PublicKey, NewPeerConnection(fp, 64))
	mgr.UpdatePeerSessions(fp, []*model.Session{{Name: "alpha"}})

	all := mgr.GetAllSessions()
	if len(all) != 1 {
		t.Fatalf("expected 1 session, got %d", len(all))
	}
	all[0].Name = "mutated"

	all2 := mgr.GetAllSessions()
	if all2[0].Name != "alpha" {
		t.Fatalf("mutation leaked into manager: got %q", all2[0].Name)
	}
}

func TestGetHosts_DefensiveCopy(t *testing.T) {
	mgr := makeV2Manager(t)
	remoteID, err := identity.Generate("remote")
	if err != nil {
		t.Fatal(err)
	}
	fp := remoteID.Fingerprint()
	mgr.RegisterPeer(fp, "remote", remoteID.PublicKey, NewPeerConnection(fp, 64))
	mgr.UpdatePeerSessions(fp, []*model.Session{{Name: "alpha"}})

	hosts := mgr.GetHosts()
	if len(hosts) != 2 { // local + remote
		t.Fatalf("expected 2 hosts, got %d", len(hosts))
	}
	for i := range hosts {
		if hosts[i].ID == fp && len(hosts[i].Sessions) > 0 {
			hosts[i].Sessions[0].Name = "mutated"
		}
	}

	hosts2 := mgr.GetHosts()
	for _, h := range hosts2 {
		if h.ID == fp && len(h.Sessions) > 0 {
			if h.Sessions[0].Name != "alpha" {
				t.Fatalf("GetHosts session mutation leaked: %q", h.Sessions[0].Name)
			}
		}
	}
}

func TestHandleV2CatalogSnapshot_UpdatesCache(t *testing.T) {
	mgr := makeV2Manager(t)
	peerID := "remote-a"
	// The wire round-trip requires a canonical (lowercase base32) OwnerID; it
	// need not equal the peer id -- the manager binds peer -> owner instead.
	owner := state.OwnerID("remotea123")
	sessionID := state.NewSessionID()
	s1 := state.LocalSessionRecord{ID: sessionID, Owner: owner, Ref: state.SessionRef{Owner: owner, Session: sessionID}}

	payload := V2CatalogSnapshotPayload{
		Owner:    owner,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{s1},
	}
	msg, err := NewMessage(MsgV2CatalogSnapshot, payload)
	if err != nil {
		t.Fatal(err)
	}
	pc := NewPeerConnection(peerID, 64)
	deps := SessionDeps{Manager: mgr, LocalMgr: mgr.localMgr}

	handleV2Message(peerID, msg, pc, deps, testLogger(t))

	snap, ok := mgr.RemoteCatalogSnapshot(owner)
	if !ok {
		t.Fatal("expected cache update")
	}
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != sessionID {
		t.Fatalf("unexpected cached sessions: %+v", snap.Sessions)
	}
}

func TestHandleV2CommandReply_DeliversToWaiter(t *testing.T) {
	mgr := makeV2Manager(t)
	done := make(chan commandResult, 1)
	mgr.registerCommandWaiter("cmd-1", done)

	mgr.deliverCommandReply(V2CommandReplyPayload{ID: "cmd-1", Handled: true})

	select {
	case res := <-done:
		if !res.payload.Handled {
			t.Fatal("expected handled")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestLocalCapabilities_IncludesV2(t *testing.T) {
	deps := SessionDeps{V2CommandSvc: &state.SessionCommandService{}}
	for _, c := range capabilitiesFor(deps) {
		if c == CapV2Catalog {
			return
		}
	}
	t.Fatal("expected CapV2Catalog in capabilitiesFor when v2 command service is set")
}

func TestLocalCapabilities_ExcludesV2WhenDisabled(t *testing.T) {
	deps := SessionDeps{}
	for _, c := range capabilitiesFor(deps) {
		if c == CapV2Catalog || c == CapV2Command {
			t.Fatalf("expected no v2 capabilities when V2CommandSvc is nil, got %q", c)
		}
	}
}

func TestLegacyPeer_InitialStateUpdateStillSent(t *testing.T) {
	mgr := makeV2Manager(t)
	peerID := "legacy-a"
	pc := NewPeerConnection(peerID, 8)
	pc.Caps = []string{CapPerStream, CapUpload} // no v2 caps
	mgr.RegisterPeer(peerID, "legacy", "", pc)

	deps := SessionDeps{Manager: mgr, LocalMgr: mgr.localMgr}
	sendStateUpdate(pc, deps)

	select {
	case f := <-pc.LoLane():
		var msg Message
		if err := json.Unmarshal(f.data, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type != MsgStateUpdate {
			t.Fatalf("expected legacy %s, got %s", MsgStateUpdate, msg.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for legacy state-update")
	}

	// No v2 catalog slot frame should be produced for legacy peers.
	sendInitialV2Catalog(pc, deps)
	select {
	case <-pc.LoLane():
		t.Fatal("unexpected v2 catalog frame for legacy peer")
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
	mgr := makeV2Manager(t)
	peerA := "peer-a"
	peerB := "peer-b"
	ownerA := state.OwnerID("ownera1")
	ownerB := state.OwnerID("ownerb2")

	// First snapshot from peerA binds ownerA.
	mgr.UpdateRemoteCatalog(peerA, state.OwnerCatalogSnapshot{
		Owner:    ownerA,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{{ID: "s1", Owner: ownerA, Ref: state.SessionRef{Owner: ownerA, Session: "s1"}}},
	})
	if _, ok := mgr.RemoteCatalogSnapshot(ownerA); !ok {
		t.Fatal("expected peerA ownerA catalog cached")
	}

	// peerB claiming ownerA is a spoof -> dropped.
	mgr.UpdateRemoteWorkspace(peerB, state.WorkspaceRecord{
		ID:       "layout1",
		Owner:    ownerA,
		Revision: 1,
		Tree:     state.Leaf(state.SessionRef{Owner: ownerA, Session: "s1"}),
	})
	if _, ok := mgr.RemoteWorkspaceSnapshot(ownerA); ok {
		t.Fatal("spoofed workspace under bound owner accepted")
	}
	mgr.UpdateRemoteCatalog(peerB, state.OwnerCatalogSnapshot{Owner: ownerA, Revision: 1})
	if _, ok := mgr.RemoteCatalogSnapshot(ownerA); ok {
		// Still peerA's catalog; peerB's attempt must not replace it.
		snap, _ := mgr.RemoteCatalogSnapshot(ownerA)
		if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "s1" {
			t.Fatalf("peerB overwrote bound owner catalog: %+v", snap.Sessions)
		}
	}

	// peerB may bind its own distinct owner.
	mgr.UpdateRemoteCatalog(peerB, state.OwnerCatalogSnapshot{Owner: ownerB, Revision: 1})
	if _, ok := mgr.RemoteCatalogSnapshot(ownerB); !ok {
		t.Fatal("expected peerB own-owner catalog cached")
	}

	// peerA switching to ownerB is rejected (one owner per peer).
	mgr.UpdateRemoteCatalog(peerA, state.OwnerCatalogSnapshot{Owner: ownerB, Revision: 2})
	snap, _ := mgr.RemoteCatalogSnapshot(ownerB)
	if len(snap.Sessions) != 0 {
		t.Fatalf("peerA published under peerB's owner: %+v", snap.Sessions)
	}
}

func TestRemoteCatalog_StaleRevisionRejected(t *testing.T) {
	mgr := makeV2Manager(t)
	peerID := "peer-a"
	owner := state.OwnerID(peerID)
	s1 := state.LocalSessionRecord{ID: "s1", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s1"}}
	s2 := state.LocalSessionRecord{ID: "s2", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s2"}}

	mgr.UpdateRemoteCatalog(peerID, state.OwnerCatalogSnapshot{Owner: owner, Revision: 1, Sessions: []state.LocalSessionRecord{s1}})

	// Equal revision (delayed duplicate) must not regress the cache.
	mgr.UpdateRemoteCatalog(peerID, state.OwnerCatalogSnapshot{Owner: owner, Revision: 1, Sessions: []state.LocalSessionRecord{s2}})
	snap, _ := mgr.RemoteCatalogSnapshot(owner)
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "s1" {
		t.Fatalf("equal-revision snapshot regressed cache: %+v", snap.Sessions)
	}

	// Lower revision (stale/delayed) must be dropped too.
	mgr.UpdateRemoteCatalog(peerID, state.OwnerCatalogSnapshot{Owner: owner, Revision: 0, Sessions: []state.LocalSessionRecord{s2}})
	snap, _ = mgr.RemoteCatalogSnapshot(owner)
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "s1" {
		t.Fatalf("lower-revision snapshot regressed cache: %+v", snap.Sessions)
	}

	// A new connection is a new generation: the baseline resets, so the same
	// numeric revision is accepted again.
	mgr.RegisterPeer(peerID, "peer-a", "", NewPeerConnection(peerID, 64))
	mgr.UpdateRemoteCatalog(peerID, state.OwnerCatalogSnapshot{Owner: owner, Revision: 1, Sessions: []state.LocalSessionRecord{s2}})
	snap, ok := mgr.RemoteCatalogSnapshot(owner)
	if !ok || len(snap.Sessions) != 1 || snap.Sessions[0].ID != "s2" {
		t.Fatalf("reconnect baseline should accept revision 1 again, got %+v (ok=%v)", snap.Sessions, ok)
	}
}

func TestRemoteWorkspace_StaleRevisionRejected(t *testing.T) {
	mgr := makeV2Manager(t)
	peerID := "peer-a"
	owner := state.OwnerID(peerID)
	leafA := state.Leaf(state.SessionRef{Owner: owner, Session: "s1"})
	leafB := state.Leaf(state.SessionRef{Owner: owner, Session: "s2"})

	mgr.UpdateRemoteWorkspace(peerID, state.WorkspaceRecord{ID: "layout1", Owner: owner, Revision: 5, Tree: leafA})

	// Same layout, equal revision -> rejected.
	mgr.UpdateRemoteWorkspace(peerID, state.WorkspaceRecord{ID: "layout1", Owner: owner, Revision: 5, Tree: leafB})
	ws, _ := mgr.RemoteWorkspaceSnapshot(owner)
	if ws.Tree.Ref == nil || ws.Tree.Ref.Session != "s1" {
		t.Fatalf("equal-revision workspace regressed cache")
	}

	// Same layout, lower revision -> rejected.
	mgr.UpdateRemoteWorkspace(peerID, state.WorkspaceRecord{ID: "layout1", Owner: owner, Revision: 4, Tree: leafB})
	ws, _ = mgr.RemoteWorkspaceSnapshot(owner)
	if ws.Tree.Ref == nil || ws.Tree.Ref.Session != "s1" {
		t.Fatalf("lower-revision workspace regressed cache")
	}

	// A different layout has its own revision baseline.
	mgr.UpdateRemoteWorkspace(peerID, state.WorkspaceRecord{ID: "layout2", Owner: owner, Revision: 1, Tree: leafB})
	ws, ok := mgr.RemoteWorkspaceSnapshot(owner)
	if !ok || ws.ID != "layout2" {
		t.Fatalf("independent workspace revisions should be accepted: %+v", ws)
	}
}

func TestRemoveHost_ForgetsRemoteCatalogPersists(t *testing.T) {
	mgr := makeV2Manager(t)
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
	owner := state.OwnerID(fp)

	mgr.RegisterPeer(fp, "remote", remoteID.PublicKey, NewPeerConnection(fp, 64))
	s1 := state.LocalSessionRecord{ID: "s1", Owner: owner, Ref: state.SessionRef{Owner: owner, Session: "s1"}}
	mgr.UpdateRemoteCatalog(fp, state.OwnerCatalogSnapshot{Owner: owner, Revision: 1, Sessions: []state.LocalSessionRecord{s1}})

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
