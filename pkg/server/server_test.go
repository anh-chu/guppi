package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/peer"
)

// TestHandleRemoteSessionPreUpgradeErrors verifies that handleRemoteSession
// returns the correct HTTP error codes *before* WebSocket upgrade for
// missing-peer and missing-capability scenarios. A regular HTTP GET (no
// Upgrade header) is used so we can inspect the response.
func TestHandleRemoteSessionPreUpgradeErrors(t *testing.T) {
	// --- missing peer ---
	t.Run("missing peer", func(t *testing.T) {
		opts := &Options{} // PeerMgr is nil, so GetPeerConnection returns nil
		req := httptest.NewRequest(http.MethodGet, "/ws/session?name=test&host=unknown", nil)
		rec := httptest.NewRecorder()
		handleRemoteSession(rec, req, opts, "unknown")
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("expected %d, got %d", http.StatusBadGateway, rec.Code)
		}
	})

	// --- missing CapPerStream ---
	t.Run("missing capability", func(t *testing.T) {
		// Build a minimal peer connection without CapPerStream.
		pc := peer.NewPeerConnection("peer-1", 128)
		// Create a minimal identity and manager so the peer is registered.
		id, err := identity.Generate("test-local")
		if err != nil {
			t.Fatalf("identity.Generate: %v", err)
		}
		pm := peer.NewManager(id, nil)
		pm.RegisterPeer("peer-1", "peer-one", "pubkey", pc)

		opts := &Options{PeerMgr: pm}
		req := httptest.NewRequest(http.MethodGet, "/ws/session?name=test&host=peer-1", nil)
		rec := httptest.NewRecorder()
		handleRemoteSession(rec, req, opts, "peer-1")
		if rec.Code != http.StatusUpgradeRequired {
			t.Fatalf("expected %d (UpgradeRequired), got %d", http.StatusUpgradeRequired, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "per-stream") {
			t.Fatalf("expected error body mentioning per-stream, got: %q", body)
		}
	})
}

// TestHandleRemoteSessionPostUpgradeCloseCode verifies that when
// serveViewerPerStream fails *after* the browser WebSocket has been upgraded,
// Termyard sends a close frame with application code 4000 and reason
// "per-stream setup failed".
func TestHandleRemoteSessionPostUpgradeCloseCode(t *testing.T) {
	// Build a peer that advertises CapPerStream so the pre-upgrade checks pass.
	pc := peer.NewPeerConnection("peer-1", 128)
	pc.Caps = append(pc.Caps, peer.CapPerStream)

	id, err := identity.Generate("test-local")
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	pm := peer.NewManager(id, nil)
	pm.RegisterPeer("peer-1", "peer-one", "pubkey", pc)

	// opts.Identity is nil so serveViewerPerStream returns false after upgrade.
	opts := &Options{PeerMgr: pm}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleRemoteSession(w, r, opts, "peer-1")
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "?name=test&cols=80&rows=24"

	// Dial may return a CloseError directly, or the close may arrive on first Read.
	ws, _, dialErr := websocket.DefaultDialer.Dial(wsURL, nil)
	if dialErr != nil {
		var ce *websocket.CloseError
		if errors.As(dialErr, &ce) {
			if ce.Code != 4000 {
				t.Fatalf("expected close code 4000, got %d", ce.Code)
			}
			if ce.Text != "per-stream setup failed" {
				t.Fatalf("expected reason %q, got %q", "per-stream setup failed", ce.Text)
			}
			return
		}
		t.Fatalf("dial error: %v", dialErr)
	}
	defer ws.Close()

	_, _, readErr := ws.ReadMessage()
	if readErr == nil {
		t.Fatal("expected a close error, got none")
	}
	var ce *websocket.CloseError
	if !errors.As(readErr, &ce) {
		t.Fatalf("expected CloseError, got %v", readErr)
	}
	if ce.Code != 4000 {
		t.Fatalf("expected close code 4000, got %d", ce.Code)
	}
	if ce.Text != "per-stream setup failed" {
		t.Fatalf("expected reason %q, got %q", "per-stream setup failed", ce.Text)
	}
}
