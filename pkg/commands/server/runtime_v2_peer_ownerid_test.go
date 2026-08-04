package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/state"
)

// setIsolatedHome points HOME (and clears XDG_DATA_HOME so DataDir/Dir both
// derive from HOME) at a fresh temp directory, so each constructed runtime
// gets its own identity file and v2 state store.
func setIsolatedHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	if err := os.MkdirAll(filepath.Join(dir, ".config", "termyard"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".local", "state", "termyard", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// newV2TestRuntime constructs one real, fully-wired Runtime (via the actual
// production newRuntime path -- not a hand-built fake) with v2 mode enabled
// and an isolated HOME, so its identity, v2 store, and v2 catalog are all
// real, independent instances.
func newV2TestRuntime(t *testing.T, homeDir string) *Runtime {
	t.Helper()
	setIsolatedHome(t, homeDir)
	rt, err := newRuntime(&cli.Command{})
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	if rt.v2Catalog == nil {
		t.Fatal("expected v2 catalog to be constructed in v2 mode")
	}
	return rt
}

// TestTwoRuntimes_RemoteSnapshotAcceptedUnderRealFingerprintConversion is the
// critical proof for the fresh-node peer-authentication fix: two real,
// independently-constructed Runtimes (each opening its own v2 store via the
// actual production newRuntime path, TERMYARD_V2_STATE=1) are paired as
// peers, and node A's real, wired v2Catalog.Owner() (produced by opening its
// store with StoreOptions.Owner = state.OwnerIDFromFingerprint(A's own
// identity.Fingerprint())) is published to node B's real peer.Manager via
// UpdateRemoteCatalog using A's actual, authenticated identity.Fingerprint()
// as the peerID -- exactly the value session_state.go threads through from a
// real connection. Before the fix, A's store opened with no Owner (a random
// OwnerID) while B's validation compared against the raw, un-converted
// fingerprint: these could never match, and every remote snapshot from a
// fresh node was rejected as an owner-mismatch spoof. This test would have
// failed against that code (see the reproduction note below) and must pass
// now that both sides use the single canonical
// state.OwnerIDFromFingerprint conversion.
func TestTwoRuntimes_RemoteSnapshotAcceptedUnderRealFingerprintConversion(t *testing.T) {
	t.Setenv("TERMYARD_V2_STATE", "1")

	rtA := newV2TestRuntime(t, t.TempDir())
	fpA := rtA.identity.Fingerprint()
	ownerA := rtA.v2Catalog.Owner()

	// Sanity: A's own catalog owner must be exactly the conversion of its own
	// fingerprint. If this ever regresses, the rest of the test's assertion
	// (acceptance) would be meaningless, so pin it explicitly.
	if want := state.OwnerIDFromFingerprint(fpA); ownerA != want {
		t.Fatalf("node A's own v2Catalog.Owner() = %q, want %q (OwnerIDFromFingerprint(own fingerprint))", ownerA, want)
	}

	// Node A's own real (empty) local catalog snapshot, exactly as produced
	// by the real store/catalog constructed in newRuntime. Session content is
	// deliberately not exercised here (that would require spawning a real PTY
	// daemon process, which is an orthogonal concern to what this test
	// proves): the owner-authentication defect this test targets rejects
	// snapshots purely on their Owner field, before any session/layout
	// content is even inspected, so an empty-but-real snapshot is a complete
	// and non-flaky reproduction.
	snapshotFromA := rtA.v2Catalog.LocalCatalogSnapshot()
	if snapshotFromA.Owner != ownerA {
		t.Fatalf("snapshot owner %q != catalog owner %q", snapshotFromA.Owner, ownerA)
	}

	rtB := newV2TestRuntime(t, t.TempDir())

	// Register A as a connected peer of B using A's real, authenticated
	// fingerprint as the peer ID -- exactly what the real peer-link layer
	// (pkg/peer/session.go) would supply from the authenticated handshake,
	// not a synthetic/mocked identity.
	conn := peer.NewPeerConnection(fpA, 64)
	rtB.peerMgr.RegisterPeer(fpA, "node-a", "", conn)

	// This is the exact call the real peer-link layer makes when a v2
	// catalog frame arrives from a connected peer (pkg/peer/session_state.go
	// -> handleV2Message -> UpdateRemoteCatalog), with peerID sourced from
	// the authenticated connection identity.
	rtB.peerMgr.UpdateRemoteCatalog(fpA, conn, snapshotFromA)

	got, ok := rtB.peerMgr.RemoteCatalogSnapshot(ownerA)
	if !ok {
		t.Fatal("node B rejected node A's real catalog snapshot as an owner mismatch -- fresh v2 nodes cannot authenticate each other (Finding 1 regression)")
	}
	if got.Revision != snapshotFromA.Revision {
		t.Fatalf("accepted snapshot revision %d != published revision %d", got.Revision, snapshotFromA.Revision)
	}
	if got.Owner != ownerA {
		t.Fatalf("accepted snapshot owner %q != published owner %q", got.Owner, ownerA)
	}
}

// TestV2Mode_NoLegacyStateManagerConstructed is the structural proof for
// Finding 8: in v2 mode, newRuntime must not construct a legacy
// *state.Manager at all -- not merely construct one and gate every call site.
// rt.stateMgr is a concrete *state.Manager field; this asserts it is a
// genuine nil pointer (never assigned) whenever v2 mode is active, and
// (as a control) non-nil in legacy mode, so a future regression that starts
// constructing it unconditionally again is caught here directly rather than
// by some downstream symptom.
func TestV2Mode_NoLegacyStateManagerConstructed(t *testing.T) {
	t.Run("v2 mode", func(t *testing.T) {
		t.Setenv("TERMYARD_V2_STATE", "1")
		rt := newV2TestRuntime(t, t.TempDir())
		if rt.stateMgr != nil {
			t.Fatal("expected rt.stateMgr to be nil in v2 mode (no legacy state.Manager may be constructed)")
		}
		// The narrow interfaces/fields that replace it must still be wired
		// from the real v2 catalog, not left as a nil-derived no-op.
		if rt.peerMgr == nil {
			t.Fatal("expected peerMgr to be constructed")
		}
		if rt.hub == nil {
			t.Fatal("expected hub to be constructed")
		}
	})

	t.Run("legacy mode control", func(t *testing.T) {
		t.Setenv("TERMYARD_V2_STATE", "0")
		setIsolatedHome(t, t.TempDir())
		rt, err := newRuntime(&cli.Command{})
		if err != nil {
			t.Fatalf("newRuntime: %v", err)
		}
		if rt.stateMgr == nil {
			t.Fatal("expected rt.stateMgr to be constructed in legacy mode")
		}
		if rt.v2Catalog != nil {
			t.Fatal("expected no v2 catalog in legacy mode")
		}
	})
}
