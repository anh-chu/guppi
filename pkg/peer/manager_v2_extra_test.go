package peer

import (
	"testing"

	"github.com/anh-chu/termyard/pkg/state"
)

// TestRemoteCatalog_SessionRefSessionMismatchRejected proves that a session with
// Ref.Session != its own record ID is rejected.
func TestRemoteCatalog_SessionRefSessionMismatchRejected(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)
	sessionID := state.NewSessionID()
	wrongSessionID := state.NewSessionID()

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	// Crafted session where Ref.Session != ID
	malformedSession := state.LocalSessionRecord{
		ID:    sessionID,
		Owner: owner,
		Ref:   state.SessionRef{Owner: owner, Session: wrongSessionID}, // MISMATCH
	}
	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{malformedSession},
	})

	// Snapshot must be rejected; no cache entry should exist
	if _, ok := mgr.RemoteCatalogSnapshot(owner); ok {
		t.Fatal("session with mismatched Ref.Session was accepted")
	}
}

// TestRemoteCatalog_DuplicateLeavesRejected proves that a layout tree with
// duplicate leaf references is rejected.
func TestRemoteCatalog_DuplicateLeavesRejected(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)
	sessionID := state.NewSessionID()

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	// Create a session
	s1 := state.LocalSessionRecord{ID: sessionID, Owner: owner, Ref: state.SessionRef{Owner: owner, Session: sessionID}}

	// Create a tree with duplicate leaves using Split helper
	ref := state.SessionRef{Owner: owner, Session: sessionID}
	duplicateTree := state.Split(state.DirectionHorizontal, 0.5, state.Leaf(ref), state.Leaf(ref))

	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{s1},
		Layouts: []state.LayoutRecord{{
			ID:    state.NewLayoutID(),
			Owner: owner,
			Order: 0,
			Tree:  duplicateTree,
		}},
	})

	// Snapshot must be rejected; no cache entry should exist
	if _, ok := mgr.RemoteCatalogSnapshot(owner); ok {
		t.Fatal("layout tree with duplicate leaves was accepted")
	}
}

// TestRemoteCatalog_MalformedSplitNodeRejected proves that a layout tree with
// missing children is rejected.
func TestRemoteCatalog_MalformedSplitNodeRejected(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)
	sessionID := state.NewSessionID()

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	// Create a session
	s1 := state.LocalSessionRecord{ID: sessionID, Owner: owner, Ref: state.SessionRef{Owner: owner, Session: sessionID}}

	// Create a malformed tree with split that has only one child
	malformedTree := state.PaneNode{
		Type:      "split",
		Direction: state.DirectionHorizontal,
		Ratio:     0.5,
		First:     &state.PaneNode{Type: "leaf", Ref: &state.SessionRef{Owner: owner, Session: sessionID}},
		Second:    nil, // MALFORMED: missing second child
	}

	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{s1},
		Layouts: []state.LayoutRecord{{
			ID:    state.NewLayoutID(),
			Owner: owner,
			Order: 0,
			Tree:  malformedTree,
		}},
	})

	// Snapshot must be rejected; no cache entry should exist
	if _, ok := mgr.RemoteCatalogSnapshot(owner); ok {
		t.Fatal("malformed split tree was accepted")
	}
}

// TestRemoteCatalog_InvalidRatioRejected proves that a layout tree with an
// invalid split ratio is rejected.
func TestRemoteCatalog_InvalidRatioRejected(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)
	sessionID := state.NewSessionID()

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	// Create a session
	s1 := state.LocalSessionRecord{ID: sessionID, Owner: owner, Ref: state.SessionRef{Owner: owner, Session: sessionID}}

	// Create a tree with invalid ratio (>= 1.0)
	ref := state.SessionRef{Owner: owner, Session: sessionID}
	invalidRatioTree := state.PaneNode{
		Type:      "split",
		Direction: state.DirectionHorizontal,
		Ratio:     1.0, // INVALID: must be in (0,1)
		First:     &state.PaneNode{Type: "leaf", Ref: &ref},
		Second:    &state.PaneNode{Type: "leaf", Ref: &ref},
	}

	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{s1},
		Layouts: []state.LayoutRecord{{
			ID:    state.NewLayoutID(),
			Owner: owner,
			Order: 0,
			Tree:  invalidRatioTree,
		}},
	})

	// Snapshot must be rejected; no cache entry should exist
	if _, ok := mgr.RemoteCatalogSnapshot(owner); ok {
		t.Fatal("tree with invalid ratio was accepted")
	}
}

// TestRemoteCatalog_UnknownSessionRefRejected proves that a layout tree
// referencing a session not in the catalog is rejected.
func TestRemoteCatalog_UnknownSessionRefRejected(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)
	sessionID := state.NewSessionID()
	unknownSessionID := state.NewSessionID()

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	// Create a session
	s1 := state.LocalSessionRecord{ID: sessionID, Owner: owner, Ref: state.SessionRef{Owner: owner, Session: sessionID}}

	// Create a tree that references an unknown session
	unknownRefTree := state.Leaf(state.SessionRef{Owner: owner, Session: unknownSessionID})

	mgr.UpdateRemoteCatalog(peerID, conn, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
		Sessions: []state.LocalSessionRecord{s1},
		Layouts: []state.LayoutRecord{{
			ID:    state.NewLayoutID(),
			Owner: owner,
			Order: 0,
			Tree:  unknownRefTree,
		}},
	})

	// Snapshot must be rejected; no cache entry should exist
	if _, ok := mgr.RemoteCatalogSnapshot(owner); ok {
		t.Fatal("tree referencing unknown session was accepted")
	}
}

// TestRemoteWorkspace_MalformedTreeRejected proves that a workspace with an
// invalid pane tree is rejected.
func TestRemoteWorkspace_MalformedTreeRejected(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)
	sessionID := state.NewSessionID()

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	// Malformed tree with split missing a child
	malformedTree := state.PaneNode{
		Type:      "split",
		Direction: state.DirectionHorizontal,
		Ratio:     0.5,
		First:     &state.PaneNode{Type: "leaf", Ref: &state.SessionRef{Owner: owner, Session: sessionID}},
		Second:    nil,
	}

	mgr.UpdateRemoteWorkspace(peerID, conn, state.WorkspaceRecord{
		ID:       state.NewLayoutID(),
		Owner:    owner,
		Revision: 1,
		Tree:     malformedTree,
	})

	// Workspace must be rejected; no cache entry should exist
	if _, ok := mgr.RemoteWorkspaceSnapshot(owner); ok {
		t.Fatal("workspace with malformed tree was accepted")
	}
}

// TestRemoteWorkspace_DuplicateLeavesRejected proves that a workspace tree
// with duplicate leaves is rejected.
func TestRemoteWorkspace_DuplicateLeavesRejected(t *testing.T) {
	mgr := makeTestManager(t)
	peerID := "peera"
	owner := state.OwnerIDFromFingerprint(peerID)
	sessionID := state.NewSessionID()

	conn := NewPeerConnection(peerID, 64)
	mgr.RegisterPeer(peerID, "peera", "", conn)

	// Create a tree with duplicate leaves
	ref := state.SessionRef{Owner: owner, Session: sessionID}
	duplicateTree := state.Split(state.DirectionHorizontal, 0.5, state.Leaf(ref), state.Leaf(ref))

	mgr.UpdateRemoteWorkspace(peerID, conn, state.WorkspaceRecord{
		ID:       state.NewLayoutID(),
		Owner:    owner,
		Revision: 1,
		Tree:     duplicateTree,
	})

	// Workspace must be rejected; no cache entry should exist
	if _, ok := mgr.RemoteWorkspaceSnapshot(owner); ok {
		t.Fatal("workspace with duplicate leaves was accepted")
	}
}
