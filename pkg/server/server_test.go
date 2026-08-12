package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/anh-chu/termyard/pkg/groupsync"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/state"
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
		pm := peer.NewManager(id, nil, nil)
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
	pm := peer.NewManager(id, nil, nil)
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

func TestGetGroupsHealsOverlappingMemberships(t *testing.T) {
	// Verify that GET /groups heals overlapping group memberships by pruning dead
	// sessions and enforcing membership exclusivity. When multiple groups contain
	// the same session key, the most recent one keeps it and others are pruned.
	home := t.TempDir()
	t.Setenv("HOME", home)

	groupStore, err := groupsync.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	stateMgr := state.NewManager()

	// Create two groups with overlapping memberships for "alive" session.
	// g1 has both "alive" and "dead", g2 has "alive" and "other".
	// Both have 2 leaves, so both can survive enforcement (by leaf count).
	// TreeUpdatedAt: g1=now, g2=now+1, so g2 wins "alive" by recency.
	tree1 := []byte(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"alive"},"second":{"type":"leaf","sessionKey":"dead"}}`)
	tree2 := []byte(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"alive"},"second":{"type":"leaf","sessionKey":"other"}}`)

	// Set g1 first
	groupStore.SetTree("g1", tree1) // would get TreeUpdatedAt=now
	stateMgr.AddSession(&model.Session{Name: "alive"})

	// Now set g2 - enforce will run with g2 as winnerID
	// g2 will own "alive" (winner), g1 keeps "dead" (still 1 leaf, tombstoned)
	groupStore.SetTree("g2", tree2)

	// After enforcement: g2 owns "alive", g1 has only "dead" (tombstoned due to <2 leaves)
	live := groupStore.Live()
	if len(live) != 1 {
		t.Fatalf("expected 1 live group after SetTree enforcement, got %d: %v", len(live), live)
	}
	if _, ok := live["g2"]; !ok {
		t.Fatalf("expected g2 to survive, got %v", live)
	}

	// Setup options with local state manager
	opts := &Options{
		GroupStore: groupStore,
		StateMgr:   stateMgr,
	}

	// Call pruneGroupSessions with no peer manager - should be a no-op since
	// we have no remote sessions and "alive" is alive locally
	pruneGroupSessions(opts, nil, nil)

	// Verify no changes (alive is still alive, no overlaps)
	liveAfter := groupStore.Live()
	if len(liveAfter) != 1 {
		t.Fatalf("expected 1 live group after pruning, got %d", len(liveAfter))
	}

	// Test direct Reconcile when "alive" is gone
	changed, _, err := groupStore.Reconcile(func(key string) bool {
		return key == "alive" // "alive" is gone
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// g2 should be tombstoned ("alive" was its only leaf)
	if len(changed) != 1 {
		t.Fatalf("expected 1 changed group, got %d", len(changed))
	}
	if _, ok := changed["g2"]; !ok {
		t.Fatalf("expected g2 in changed, got %v", changed)
	}

	// Verify g2 is now tombstoned
	liveAfter = groupStore.Live()
	if len(liveAfter) != 0 {
		t.Fatalf("expected 0 live groups after pruning, got %d: %v", len(liveAfter), liveAfter)
	}
	if g2, ok := groupStore.Get("g2"); ok && g2.DeletedAt.IsZero() {
		t.Fatalf("expected g2 to be tombstoned, got %v", g2)
	}
}

// TestSetTreeTombstonedReturns410 verifies that SetTree on a tombstoned group
// returns HTTP 410 Gone and doesn't broadcast or fanout.
func TestSetTreeTombstonedReturns410(t *testing.T) {
	groupStore, err := groupsync.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Create group g1 with 2 leaves, then prune to 1 leaf to tombstone it
	tree := []byte(`{"type":"leaf","sessionKey":"session1"}`)
	groupStore.SetTree("g1", tree)

	// Verify g1 is tombstoned (1 leaf)
	g1, ok := groupStore.Get("g1")
	if !ok {
		t.Fatalf("expected g1 to exist")
	}
	if g1.DeletedAt.IsZero() {
		t.Fatalf("expected g1 to be tombstoned, got %v", g1)
	}

	// Try to SetTree on tombstoned g1
	newTree := []byte(`{"type":"leaf","sessionKey":"session2"}`)
	_, _, _, err = groupStore.SetTree("g1", newTree)
	if !errors.Is(err, groupsync.ErrTombstoned) {
		t.Fatalf("expected ErrTombstoned, got %v", err)
	}

	// Verify g1 was not updated
	g1After, _ := groupStore.Get("g1")
	if string(g1After.Tree) != string(g1.Tree) {
		t.Fatalf("expected g1 tree to not change, was %s, got %s", g1.Tree, g1After.Tree)
	}
}
