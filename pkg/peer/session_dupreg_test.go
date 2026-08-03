package peer

import (
	"testing"

	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/state"
)

// TestRunSession_RejectedDuplicateLeavesNoDanglingWorkspaceSubscription
// reproduces the ordering runSession uses on connect: register the peer
// connection first, and only subscribe to workspace snapshots on success.
// A rejected duplicate registration (simultaneous-connect race) must not
// leave an orphaned subscription enqueuing snapshots into a PeerConnection
// nobody will ever drain.
func TestRunSession_RejectedDuplicateLeavesNoDanglingWorkspaceSubscription(t *testing.T) {
	mgr := makeV2Manager(t)
	cat := state.NewCatalog("owner-a", nil)
	mgr.localMgr.EnableV2Shadow(cat, nil, nil)

	remoteID, err := identity.Generate("remote")
	if err != nil {
		t.Fatal(err)
	}
	peerID := remoteID.Fingerprint()

	// First connection registers fine and (per the fixed order in
	// runSession) subscribes to workspace snapshots afterwards.
	pc1 := NewPeerConnection(peerID, 64)
	if !mgr.TryRegisterPeer(peerID, "remote", remoteID.PublicKey, "", pc1) {
		t.Fatal("expected first registration to succeed")
	}
	unsub1 := cat.SubscribeWorkspace(func(state.LayoutID, state.WorkspaceRecord) {})
	defer unsub1()

	if got := cat.WorkspaceSubscriberCount(); got != 1 {
		t.Fatalf("expected 1 subscriber after first connection, got %d", got)
	}

	// Simultaneous duplicate connection: registration must be attempted
	// (and rejected) BEFORE any subscription is created, so the rejected
	// path never subscribes at all.
	pc2 := NewPeerConnection(peerID, 64)
	registered := mgr.TryRegisterPeer(peerID, "remote", remoteID.PublicKey, "", pc2)
	if registered {
		t.Fatal("expected duplicate registration to be rejected")
	}
	// Mirrors runSession's fixed order: on rejection it returns before ever
	// calling SubscribeWorkspace, so no subscription should exist here.

	if got := cat.WorkspaceSubscriberCount(); got != 1 {
		t.Fatalf("rejected duplicate leaked a workspace subscription: got %d subscribers, want 1", got)
	}
}
