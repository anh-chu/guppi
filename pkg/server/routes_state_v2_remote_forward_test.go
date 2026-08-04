package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/state"
)

// TestV2SessionCommand_RemoteRefRouting is the HTTP-level proof for Finding
// (remote-command-forwarding gap): handleV2SessionCommand must never
// silently execute a command against its OWN local catalog when the
// request's Ref.Owner names a different (remote) node's catalog. Before this
// fix, the handler unconditionally called opts.V2CommandSvc.ExecuteSessionCommand
// regardless of Ref.Owner -- a kill/label/etc against an already-visible,
// already-attached remote session would either mutate the wrong (local)
// catalog or 404 there, even though the real target session was reachable
// and valid on its owning node.
//
// The peer-manager routing/forwarding decision (does this Ref.Owner belong
// to a live, resolvable peer connection?) is exercised here through the real
// HTTP handler and real *peer.Manager. The actual wire round-trip once a
// peer IS resolved (PeerIDForOwner -> SendCommand -> reply) is proven
// separately and more cheaply in
// pkg/peer/rpc_test.go's TestPeerIDForOwner_RoutesSendCommandToLiveOwnerBoundPeer,
// which exercises the exact two primitives this handler calls.
func TestV2SessionCommand_RemoteRefRouting(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	_ = os.MkdirAll(filepath.Join(tmpHome, ".config", "termyard"), 0o700)

	catalog, svc := newV2TestCatalog(t)
	localOwner := catalog.Owner()
	remoteOwner := state.NewOwnerID()
	remoteSessionID := state.NewSessionID()

	body := func(owner state.OwnerID) []byte {
		b, _ := json.Marshal(map[string]any{
			"ref":    state.SessionRef{Owner: owner, Session: remoteSessionID},
			"action": state.ActionKill,
		})
		return b
	}

	post := func(r http.Handler, payload []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v2/session-commands", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	t.Run("no PeerMgr configured: remote ref must not silently run locally", func(t *testing.T) {
		opts := &Options{V2Catalog: catalog, V2CommandSvc: svc}
		r := newV2TestRouter(opts)
		w := post(r, body(remoteOwner))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 (remote owner unreachable, not silently executed locally), got %d: %s", w.Code, w.Body.String())
		}
		var errResp v2ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
			t.Fatalf("decode error response: %v", err)
		}
		if errResp.Code != "peer_offline" {
			t.Fatalf("expected peer_offline error code, got %q", errResp.Code)
		}
		// Prove the local catalog was NOT mutated -- the actual regression
		// this finding described (label/kill against a remote ref silently
		// mutating or 404-ing against the WRONG, local catalog).
		if _, ok := catalog.Session(remoteSessionID); ok {
			t.Fatal("remote-owned session must never appear in the local catalog")
		}
	})

	t.Run("PeerMgr configured but owner not bound to any live peer: still not silently local", func(t *testing.T) {
		localID, err := identity.Generate("node-a")
		if err != nil {
			t.Fatal(err)
		}
		peerStore, err := identity.NewPeerStore()
		if err != nil {
			t.Fatal(err)
		}
		peerMgr := peer.NewManager(localID, peerStore, state.NewManager())
		opts := &Options{V2Catalog: catalog, V2CommandSvc: svc, PeerMgr: peerMgr}
		r := newV2TestRouter(opts)
		w := post(r, body(remoteOwner))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
		}
		var errResp v2ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
			t.Fatalf("decode error response: %v", err)
		}
		if errResp.Code != "peer_offline" {
			t.Fatalf("expected peer_offline error code, got %q", errResp.Code)
		}
	})

	t.Run("ref.Owner matches this node's own owner: executes locally as before", func(t *testing.T) {
		params, _ := json.Marshal(state.CreateParams{Name: "local-target"})
		created, err := svc.ExecuteSessionCommand(t.Context(), state.SessionCommand{
			ID: state.NewCommandID(), Action: state.ActionCreate, Params: params,
		})
		if err != nil {
			t.Fatalf("local create: %v", err)
		}

		opts := &Options{V2Catalog: catalog, V2CommandSvc: svc}
		r := newV2TestRouter(opts)
		b, _ := json.Marshal(map[string]any{
			"ref":    created.Ref,
			"action": state.ActionLabel,
			"params": state.LabelParams{Label: "renamed-local"},
		})
		w := post(r, b)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for own-owner ref, got %d: %s", w.Code, w.Body.String())
		}
		if got := localOwner; got != created.Ref.Owner {
			t.Fatalf("sanity: created session owner %q != catalog owner %q", created.Ref.Owner, got)
		}
	})
}
