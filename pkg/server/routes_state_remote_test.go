package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/state"
)

// TestBootstrapIncludesRemoteOwnerCatalog is the closest cheaply-available
// stand-in for a full two-node integration test: it exercises the exact
// production wiring (peer.Manager.UpdateRemoteCatalog -> the same validated,
// cached path a real remote peer's snapshot would take -> registerStateRoutes'
// handleBootstrap -> state.AggregateCatalog) end to end through the real
// HTTP route handler, proving node A's bootstrap response surfaces node B's
// session under B's own owner ID, distinguishable from A's own session.
func TestBootstrapIncludesRemoteOwnerCatalog(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	_ = os.MkdirAll(filepath.Join(tmpHome, ".config", "termyard"), 0o700)

	localID, err := identity.Generate("node-a")
	if err != nil {
		t.Fatal(err)
	}
	peerStore, err := identity.NewPeerStore()
	if err != nil {
		t.Fatal(err)
	}
	peerMgr := peer.NewManager(localID, peerStore)

	// Node A's own local catalog and session, exactly as handleBootstrap
	// reads it today.
	catalog, svc := newStateTestCatalog(t)
	localOwner := catalog.Owner()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = svc.Run(ctx) }()
	params, _ := json.Marshal(state.CreateParams{Name: "local-session"})
	res, err := svc.ExecuteSessionCommand(t.Context(), state.SessionCommand{
		ID: state.NewCommandID(), Action: state.ActionCreate, Params: params,
	})
	if err != nil {
		t.Fatalf("local create: %v", err)
	}
	// The create is durable-before-reply; the daemon start is committed
	// asynchronously by a background worker. Wait for it, matching the
	// pattern used by TestBootstrapIncludesPerSessionDaemonGeneration.
	for start := time.Now(); time.Since(start) < 5*time.Second; {
		if rec, ok := catalog.Session(res.Ref.Session); ok && rec.Phase == state.SessionPhaseActive {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Node B's session, arriving over the peer link exactly as
	// UpdateRemoteCatalog validates and caches it for a real remote peer --
	// this is the same call peer/session_state.go makes when a catalog
	// frame arrives from a connected peer. UpdateRemoteCatalog enforces
	// snap.Owner == state.OwnerIDFromFingerprint(peerID), so the owner must be a valid
	// OwnerID (unlike a raw fingerprint's charset); the peer fingerprint
	// itself only needs to be a stable string identifying the connection,
	// matching the pattern used by pkg/peer's own catalog tests.
	remoteFingerprint := "nodebfingerprint"
	remoteOwner := state.NewOwnerID()
	remoteSessionID := state.NewSessionID()
	conn := peer.NewPeerConnection(remoteFingerprint, 8)
	peerMgr.RegisterPeer(remoteFingerprint, "node-b", "", conn)
	// UpdateRemoteCatalog enforces the authenticated identity: the owner it
	// accepts is derived from the peerID, not from the claimed snapshot. We
	// use remoteFingerprint directly as the accepted owner to keep this test
	// focused on the aggregation/bootstrap wiring rather than re-deriving
	// identity binding already covered by pkg/peer's own tests.
	remoteOwner = state.OwnerIDFromFingerprint(remoteFingerprint)
	peerMgr.UpdateRemoteCatalog(remoteFingerprint, conn, state.OwnerCatalogSnapshot{
		Owner:    remoteOwner,
		Revision: 3,
		Sessions: []state.LocalSessionRecord{
			{
				ID:    remoteSessionID,
				Owner: remoteOwner,
				Ref:   state.SessionRef{Owner: remoteOwner, Session: remoteSessionID},
				Phase: state.SessionPhaseActive,
			},
		},
	})

	opts := &Options{Catalog: catalog, CommandSvc: svc, PeerMgr: peerMgr}
	r := chi.NewRouter()
	registerStateRoutes(r, opts)

	req := httptest.NewRequest(http.MethodGet, "/state/bootstrap", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp bootstrapResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Local.Owner != localOwner {
		t.Fatalf("expected local owner %q, got %q", localOwner, resp.Local.Owner)
	}
	if len(resp.Local.Sessions) != 1 {
		t.Fatalf("expected exactly node A's own session under Local, got %+v", resp.Local.Sessions)
	}

	if len(resp.Remote) != 1 {
		t.Fatalf("expected exactly one remote owner catalog, got %d: %+v", len(resp.Remote), resp.Remote)
	}
	remote := resp.Remote[0]
	if remote.Owner != remoteOwner {
		t.Fatalf("expected remote owner %q (node B's authenticated identity), got %q", remoteOwner, remote.Owner)
	}
	if remote.Owner == localOwner {
		t.Fatal("remote owner must never equal the local owner")
	}
	if len(remote.Sessions) != 1 || remote.Sessions[0].ID != remoteSessionID {
		t.Fatalf("expected node B's session under its own owner, got %+v", remote.Sessions)
	}
	if remote.Revision != 3 {
		t.Fatalf("expected remote owner's independent revision (3) preserved, got %d", remote.Revision)
	}
}
