package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

func newTestOpts(t *testing.T) *Options {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	_ = os.MkdirAll(filepath.Join(tmpHome, ".config", "termyard"), 0o700)

	id, _ := identity.Generate("local-host")
	ps, _ := identity.NewPeerStore()
	pStore, _ := auth.NewPasswordStore()
	_ = pStore.SetPassword("supersecret")
	mgr := peer.NewManager(id, ps)
	deps := peer.SessionDeps{
		Manager:     mgr,
		Identity:    id,
		ActTracker:  activity.NewTracker(),
		ToolTracker: toolevents.NewTracker(),
		PeerStore:   ps,
	}
	sup := peer.NewLinkSupervisor(deps)
	return &Options{
		Identity:       id,
		PeerStore:      ps,
		PeerMgr:        mgr,
		LinkSupervisor: sup,
		PasswordStore:  pStore,
	}
}

func TestPostPeersBootstrapRejectsBadPassword(t *testing.T) {
	opts := newTestOpts(t)
	req := peer.BootstrapRequest{
		Password: "wrong", Name: "remote",
		PublicKey: "remote-pk", Fingerprint: "remote-fp",
	}
	body, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/api/peers/bootstrap", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handlePeersBootstrap(w, r, opts)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestPostPeersBootstrapRejectsSelf(t *testing.T) {
	opts := newTestOpts(t)
	req := peer.BootstrapRequest{
		Password: "supersecret", Name: "self",
		PublicKey: opts.Identity.PublicKey, Fingerprint: opts.Identity.Fingerprint(),
	}
	body, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/api/peers/bootstrap", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handlePeersBootstrap(w, r, opts)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestPostPeersBootstrapNoPasswordReturns503(t *testing.T) {
	opts := newTestOpts(t)
	// Clear password.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	_ = os.MkdirAll(filepath.Join(tmpHome, ".config", "termyard"), 0o700)
	freshStore, _ := auth.NewPasswordStore()
	opts.PasswordStore = freshStore

	req := peer.BootstrapRequest{Password: "anything", PublicKey: "pk"}
	body, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/api/peers/bootstrap", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handlePeersBootstrap(w, r, opts)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestPostPeersBootstrapIdempotent(t *testing.T) {
	opts := newTestOpts(t)
	remote, _ := identity.Generate("remote-node")
	req := peer.BootstrapRequest{
		Password:    "supersecret",
		Name:        "remote",
		PublicKey:   remote.PublicKey,
		Fingerprint: remote.Fingerprint(),
	}
	body, _ := json.Marshal(req)

	// First call.
	r := httptest.NewRequest(http.MethodPost, "/api/peers/bootstrap", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handlePeersBootstrap(w, r, opts)
	if w.Code != http.StatusOK {
		t.Fatalf("first call: expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	// Second call with same pubkey.
	r2 := httptest.NewRequest(http.MethodPost, "/api/peers/bootstrap", bytes.NewReader(body))
	w2 := httptest.NewRecorder()
	handlePeersBootstrap(w2, r2, opts)
	if w2.Code != http.StatusOK {
		t.Fatalf("second call: expected 200, got %d", w2.Code)
	}
	if got := len(opts.PeerStore.List()); got != 1 {
		t.Errorf("expected 1 peer after idempotent re-bootstrap, got %d", got)
	}
}

func TestGetPeers(t *testing.T) {
	opts := newTestOpts(t)
	// Inject a peer.
	remote, _ := identity.Generate("remote-node")
	_ = opts.LinkSupervisor.AddPeer(identity.Peer{
		Name: "remote", PublicKey: remote.PublicKey,
		Address: "127.0.0.1:1", Enabled: true, InitiatedByUs: true,
	})

	r := httptest.NewRequest(http.MethodGet, "/api/peers", nil)
	w := httptest.NewRecorder()
	handleGetPeers(w, r, opts)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	var resp struct {
		Self  selfInfo            `json:"self"`
		Peers []peer.LinkSnapshot `json:"peers"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Self.Fingerprint != opts.Identity.Fingerprint() {
		t.Errorf("self fingerprint mismatch: %+v", resp.Self)
	}
	if len(resp.Peers) != 1 {
		t.Errorf("expected 1 peer, got %d", len(resp.Peers))
	}
}

func TestDeletePeerRemovesFromStore(t *testing.T) {
	opts := newTestOpts(t)
	remote, _ := identity.Generate("rm-target")
	_ = opts.LinkSupervisor.AddPeer(identity.Peer{
		Name: "rm-target", PublicKey: remote.PublicKey,
		Address: "127.0.0.1:1", Enabled: true, InitiatedByUs: true,
	})

	// Route via chi to bind {fp} param.
	router := chi.NewRouter()
	router.Delete("/api/peers/{fp}", func(w http.ResponseWriter, r *http.Request) {
		handleDeletePeer(w, r, opts)
	})

	r := httptest.NewRequest(http.MethodDelete, "/api/peers/"+remote.Fingerprint(), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		body, _ := io.ReadAll(w.Body)
		t.Fatalf("delete: got %d body=%s", w.Code, body)
	}
	if got := opts.PeerStore.GetByPublicKey(remote.PublicKey); got != nil {
		t.Errorf("peer still in store after DELETE")
	}
}
