package peer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/state"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

// makeTestDepsWithCatalog builds a SessionDeps + supervisor + real catalog
// wired together BEFORE the LinkSupervisor is constructed (NewLinkSupervisor
// takes SessionDeps by value, so CommandSvc/Manager.Catalog must be set
// first or the supervisor's dialer would advertise stale capabilities).
func makeTestDepsWithCatalog(t *testing.T, name string) (SessionDeps, *LinkSupervisor, *identity.PeerStore, *state.Catalog) {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	_ = os.MkdirAll(filepath.Join(tmpHome, ".config", "termyard"), 0o700)

	id, err := identity.Generate(name)
	if err != nil {
		t.Fatal(err)
	}
	ps, err := identity.NewPeerStore()
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(id, ps)
	owner := state.OwnerIDFromFingerprint(id.Fingerprint())
	cat := state.NewCatalog(owner, nil)
	mgr.SetCatalog(cat)
	deps := SessionDeps{
		Manager:      mgr,
		Catalog:      cat,
		Identity:     id,
		ActTracker:   activity.NewTracker(),
		ToolTracker:  toolevents.NewTracker(),
		PeerStore:    ps,
		CommandSvc: canonicalCommandSvcForTest(),
	}
	sup := NewLinkSupervisor(deps)
	return deps, sup, ps, cat
}

// TestRunSession_LocalCatalogMutationAfterConnectReachesConnectedPeer proves
// the real production defect found by the Task 15 multi-node E2E harness
// (case 2/3/4/5's "seeded session on B never replicated to A remote
// catalog" symptom): a local catalog mutation made AFTER a peer connection
// is already established must be pushed to that connected peer, not just
// the one-time snapshot sent at connect time by sendInitialCatalog.
//
// This drives two real peer.Manager instances joined by a real, fully
// authenticated (ed25519 challenge-response) websocket connection --
// runSession runs for real on both the dialer and listener sides, exactly
// as it does in production (pkg/peer/handler.go's HandlePeer and
// pkg/peer/supervisor.go's dialOnce). Nothing here is mocked.
func TestRunSession_LocalCatalogMutationAfterConnectReachesConnectedPeer(t *testing.T) {
	depsA, supA, psA, _ := makeTestDepsWithCatalog(t, "A")
	depsB, _, psB, catB := makeTestDepsWithCatalog(t, "B")

	ownerB := state.OwnerIDFromFingerprint(depsB.Identity.Fingerprint())

	// B is the listener: stand up a real HTTP+WS server serving the real
	// production handler.
	handlerB := NewHandler(depsB, NewStreamRegistry())
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/peer", handlerB.HandlePeer)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	addr := mustHostPort(t, srv.URL)

	// Each side must know the other's identity, exactly as real pairing
	// (POST /api/peers -> POST /api/peers/bootstrap) leaves behind in
	// peers.json.
	if err := psB.Add(identity.Peer{
		Name:      "A",
		PublicKey: depsA.Identity.PublicKey,
		Enabled:   true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := psA.Add(identity.Peer{
		Name:          "B",
		PublicKey:     depsB.Identity.PublicKey,
		Address:       addr,
		Enabled:       true,
		InitiatedByUs: true,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supA.Start(ctx)
	bPeer := psA.GetByPublicKey(depsB.Identity.PublicKey)
	if bPeer == nil {
		t.Fatal("B peer not found in A's peer store after Add")
	}
	if err := supA.AddPeer(*bPeer); err != nil {
		t.Fatal(err)
	}

	fpB := depsB.Identity.Fingerprint()
	fpA := depsA.Identity.Fingerprint()

	// Wait for the real connection to establish (real dial + real
	// challenge-response auth), proven by the manager reporting a live
	// connection on both sides -- not an arbitrary sleep.
	waitFor(t, 10*time.Second, func() bool {
		return depsA.Manager.HasLiveConnection(fpB) && depsB.Manager.HasLiveConnection(fpA)
	}, "peers never reached a live connection")

	// A's remote-catalog cache should already have B's (empty) initial
	// snapshot from the connect-time push (sendInitialCatalog).
	waitFor(t, 5*time.Second, func() bool {
		_, ok := depsA.Manager.RemoteCatalogSnapshot(ownerB)
		return ok
	}, "A never received B's initial catalog snapshot at connect time")

	// The regression: mutate B's LOCAL catalog *after* the connection is
	// already fully established -- exactly what the E2E harness's
	// b.createLocalSession(name) does after pairing completes.
	const newSessionID = state.SessionID("sessafterconnect")
	if err := catB.PutSession(state.LocalSessionRecord{
		ID:    newSessionID,
		Owner: ownerB,
		Ref:   state.SessionRef{Owner: ownerB, Session: newSessionID},
	}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 10*time.Second, func() bool {
		snap, ok := depsA.Manager.RemoteCatalogSnapshot(ownerB)
		if !ok {
			return false
		}
		for _, s := range snap.Sessions {
			if s.ID == newSessionID {
				return true
			}
		}
		return false
	}, "session created on B after pairing never replicated to A's remote-catalog cache "+
		"(sendInitialCatalog only fires once at connect; the local Catalog's own "+
		"SubscribeCatalog change-notification was never wired to push a follow-up "+
		"snapshot to already-connected peers)")
}

// waitFor polls cond every 20ms until it returns true or timeout elapses,
// then fails the test with msg. No arbitrary fixed sleeps: the caller only
// waits as long as it actually takes, bounded by timeout.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}
