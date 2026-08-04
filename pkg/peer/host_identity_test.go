package peer

import (
	"testing"

	"github.com/anh-chu/termyard/pkg/state"
)

// TestResolveHostParam_LocalOwnerID is the real-boundary proof for Finding 1
// (OwnerID vs peer-fingerprint identity confusion): a `host` request
// parameter carrying THIS node's own v2 OwnerID (what a v2-routed terminal
// attach actually sends -- see state/v2/paneTreeAdapter.ts sessionRefToKey
// and TiledView.tsx's hostId prop) must resolve as local, exactly like the
// legacy fingerprint value did before v2 existed. Before ResolveHostParam,
// every such call site compared the raw host value against fingerprints via
// IsLocal() unconditionally, so a real OwnerID (a different string encoding
// than its owner's fingerprint, see state.OwnerIDFromFingerprint) was never
// recognized as local and was misrouted to handleRemoteSession with no live
// peer connection to satisfy it.
func TestResolveHostParam_LocalOwnerID(t *testing.T) {
	mgr := makeTestV2Manager(t)
	owner := state.NewOwnerID()
	cat := state.NewCatalog(owner, nil)
	if err := cat.Load(); err != nil {
		t.Fatal(err)
	}
	mgr.SetV2Catalog(cat)

	peerID, isLocal := mgr.ResolveHostParam(string(owner))
	if !isLocal || peerID != "" {
		t.Fatalf("ResolveHostParam(local OwnerID) = (%q, %v), want (\"\", true)", peerID, isLocal)
	}
}

// TestResolveHostParam_RemoteOwnerResolvesToLivePeerConnection is the
// real-boundary proof that a `host` parameter carrying a REMOTE peer's v2
// OwnerID resolves to that peer's actual live connection fingerprint via the
// same PeerIDForOwner lookup handleV2SessionCommand uses (pkg/server/
// routes_state_v2.go), established the same way a real inbound catalog
// snapshot would (UpdateRemoteCatalog), not a preconstructed/short-circuited
// OwnerID that skips the boundary. Before this fix, terminal attach and file
// routes compared the OwnerID directly against fingerprints via IsLocal /
// GetPeerConnection, which never matched.
func TestResolveHostParam_RemoteOwnerResolvesToLivePeerConnection(t *testing.T) {
	mgr := makeTestV2Manager(t)
	peerID := "remote-node-b"
	pc := newV2PeerConnection(peerID)
	mgr.RegisterPeer(peerID, "node-b", "", pc)

	owner := state.OwnerIDFromFingerprint(peerID)
	mgr.UpdateRemoteCatalog(peerID, pc, state.OwnerCatalogSnapshot{
		Owner:    owner,
		Revision: 1,
	})

	resolvedPeerID, isLocal := mgr.ResolveHostParam(string(owner))
	if isLocal {
		t.Fatalf("ResolveHostParam(remote OwnerID) reported local, want remote peer %q", peerID)
	}
	if resolvedPeerID != peerID {
		t.Fatalf("ResolveHostParam(remote OwnerID) = %q, want %q", resolvedPeerID, peerID)
	}
	if conn := mgr.GetPeerConnection(resolvedPeerID); conn != pc {
		t.Fatalf("resolved peerID %q did not resolve to the registered live connection", resolvedPeerID)
	}
}

// TestResolveHostParam_LegacyFingerprintFallback proves pre-v2 callers (whose
// `host` value is a legacy peer fingerprint, e.g. pre-v2 model.Session.Host)
// keep working unchanged: when host does not resolve as any known OwnerID,
// ResolveHostParam falls back to the legacy fingerprint interpretation.
func TestResolveHostParam_LegacyFingerprintFallback(t *testing.T) {
	mgr := makeTestV2Manager(t)
	peerID := "legacy-peer"
	pc := newV2PeerConnection(peerID)
	mgr.RegisterPeer(peerID, "legacy", "", pc)

	resolvedPeerID, isLocal := mgr.ResolveHostParam(peerID)
	if isLocal {
		t.Fatalf("ResolveHostParam(legacy fingerprint) reported local, want remote peer %q", peerID)
	}
	if resolvedPeerID != peerID {
		t.Fatalf("ResolveHostParam(legacy fingerprint) = %q, want %q", resolvedPeerID, peerID)
	}
}

// TestOwnerIDForPeer_ForwardMapping proves the forward (peer fingerprint ->
// OwnerID) mapping GetHosts threads into HostInfo.OwnerID: the local peer's
// OwnerID is the node's own v2 catalog owner, and a remote peer's OwnerID is
// the canonical deterministic conversion of its fingerprint.
func TestOwnerIDForPeer_ForwardMapping(t *testing.T) {
	mgr := makeTestV2Manager(t)
	owner := state.NewOwnerID()
	cat := state.NewCatalog(owner, nil)
	if err := cat.Load(); err != nil {
		t.Fatal(err)
	}
	mgr.SetV2Catalog(cat)

	if got, ok := mgr.OwnerIDForPeer(mgr.LocalID()); !ok || got != owner {
		t.Fatalf("OwnerIDForPeer(local) = (%q, %v), want (%q, true)", got, ok, owner)
	}

	remoteFingerprint := "remote-peer-xyz"
	want := state.OwnerIDFromFingerprint(remoteFingerprint)
	if got, ok := mgr.OwnerIDForPeer(remoteFingerprint); !ok || got != want {
		t.Fatalf("OwnerIDForPeer(remote) = (%q, %v), want (%q, true)", got, ok, want)
	}
}

// TestGetHosts_IncludesOwnerID proves the HostInfo returned by GetHosts
// (serialized to the browser at GET /api/hosts) carries OwnerID alongside
// the legacy fingerprint ID, so a v2-enabled frontend can select the correct
// identity domain for target_owner / terminal-attach host params instead of
// conflating the two.
func TestGetHosts_IncludesOwnerID(t *testing.T) {
	mgr := makeTestV2Manager(t)
	owner := state.NewOwnerID()
	cat := state.NewCatalog(owner, nil)
	if err := cat.Load(); err != nil {
		t.Fatal(err)
	}
	mgr.SetV2Catalog(cat)

	hosts := mgr.GetHosts()
	var found bool
	for _, h := range hosts {
		if h.ID == mgr.LocalID() {
			found = true
			if h.OwnerID != string(owner) {
				t.Fatalf("local HostInfo.OwnerID = %q, want %q", h.OwnerID, owner)
			}
			if h.OwnerID == h.ID {
				t.Fatalf("HostInfo.OwnerID must not equal HostInfo.ID (different identity domains), got both %q", h.ID)
			}
		}
	}
	if !found {
		t.Fatal("GetHosts did not include the local host")
	}
}
