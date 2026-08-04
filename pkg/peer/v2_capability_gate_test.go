package peer

import (
	"testing"

	"github.com/anh-chu/termyard/pkg/state"
)

// v2CommandSvcForTest returns a non-nil *state.SessionCommandService pointer
// suitable only for identity checks (deps.V2CommandSvc != nil) in these
// tests; it is never invoked, so its internal fields are left zero.
func v2CommandSvcForTest() *state.SessionCommandService {
	return &state.SessionCommandService{}
}

func TestRequiresV2Peer(t *testing.T) {
	if requiresV2Peer(SessionDeps{}) {
		t.Fatal("expected false when V2CommandSvc is nil")
	}
	if !requiresV2Peer(SessionDeps{V2CommandSvc: v2CommandSvcForTest()}) {
		t.Fatal("expected true when V2CommandSvc is set")
	}
}

func TestPeerCapsSatisfyV2(t *testing.T) {
	cases := []struct {
		name string
		caps []string
		want bool
	}{
		{"no caps", nil, false},
		{"only legacy caps", []string{CapPerStream, CapUpload}, false},
		{"only catalog cap", []string{CapV2Catalog}, false},
		{"only command cap", []string{CapV2Command}, false},
		{"both v2 caps", []string{CapV2Catalog, CapV2Command}, true},
		{"both v2 caps plus legacy", []string{CapPerStream, CapV2Catalog, CapUpload, CapV2Command}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := peerCapsSatisfyV2(tc.caps); got != tc.want {
				t.Fatalf("peerCapsSatisfyV2(%v) = %v, want %v", tc.caps, got, tc.want)
			}
		})
	}
}

// TestV2OnlyNodeRejectsNonV2CapabilitiesAtHandshakeGate proves the combined
// gate used at both the listener (handler.go HandlePeer) and dialer
// (supervisor.go dialOnce) handshake sites: a v2-only node's requirement is
// only enforced when it actually constructed the v2 command service, and
// only satisfied by a peer advertising both required capabilities.
func TestV2OnlyNodeRejectsNonV2CapabilitiesAtHandshakeGate(t *testing.T) {
	v2OnlyDeps := SessionDeps{V2CommandSvc: v2CommandSvcForTest()}
	legacyDeps := SessionDeps{}

	legacyPeerCaps := []string{CapPerStream, CapUpload}
	v2PeerCaps := []string{CapPerStream, CapUpload, CapV2Catalog, CapV2Command}

	if !requiresV2Peer(v2OnlyDeps) {
		t.Fatal("v2-only node must require v2 peer capabilities")
	}
	if peerCapsSatisfyV2(legacyPeerCaps) {
		t.Fatal("a peer without v2 caps must not satisfy a v2-only node's requirement")
	}
	if !peerCapsSatisfyV2(v2PeerCaps) {
		t.Fatal("a peer with both v2 caps must satisfy a v2-only node's requirement")
	}
	if requiresV2Peer(legacyDeps) {
		t.Fatal("a legacy-mode node (no V2CommandSvc) must not require v2 peer capabilities")
	}
}
