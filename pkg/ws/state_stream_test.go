package ws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/anh-chu/termyard/pkg/state"
)

func newTestCatalog(t *testing.T) *state.Catalog {
	t.Helper()
	owner := state.NewOwnerID()
	catalog := state.NewCatalog(owner, nil)
	if err := catalog.Load(); err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	return catalog
}

func dialStateStream(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func readTyped(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return msg
}

// TestStateStreamSendsCompleteSnapshotOnConnect verifies that a connecting
// client immediately receives a complete catalog_snapshot before any
// incremental publication, satisfying "one bootstrap-equivalent frame
// contains complete state".
func TestStateStreamSendsCompleteSnapshotOnConnect(t *testing.T) {
	catalog := newTestCatalog(t)
	sessionID := state.NewSessionID()
	if err := catalog.PutSession(state.LocalSessionRecord{
		ID:         sessionID,
		Owner:      catalog.Owner(),
		Ref:        state.SessionRef{Owner: catalog.Owner(), Session: sessionID},
		Phase:      state.SessionPhaseActive,
		Generation: "test-gen",
	}); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	if err := catalog.PutLayout(state.LayoutRecord{
		ID:    state.NewLayoutID(),
		Owner: catalog.Owner(),
		Tree:  state.Leaf(state.SessionRef{Owner: catalog.Owner(), Session: sessionID}),
	}); err != nil {
		t.Fatalf("PutLayout: %v", err)
	}

	hub := NewStateStreamHub(catalog, nil)
	defer hub.Close()

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleState))
	defer srv.Close()

	conn := dialStateStream(t, srv)
	defer conn.Close()

	msg := readTyped(t, conn)
	if msg["type"] != "catalog_snapshot" {
		t.Fatalf("expected catalog_snapshot first, got %v", msg["type"])
	}
	msg = readTyped(t, conn)
	if msg["type"] != "workspace_snapshot" {
		t.Fatalf("expected workspace_snapshot second, got %v", msg["type"])
	}
}

// TestStateStreamCoalescesToLatestRevision verifies that a slow client only
// ever observes the latest catalog revision once it starts reading, never a
// backlog of intermediate revisions, and that a dropped early frame is
// repaired by the later complete snapshot.
func TestStateStreamCoalescesToLatestRevision(t *testing.T) {
	catalog := newTestCatalog(t)
	hub := NewStateStreamHub(catalog, nil)
	defer hub.Close()

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleState))
	defer srv.Close()

	conn := dialStateStream(t, srv)
	defer conn.Close()

	// Drain the initial complete snapshot.
	_ = readTyped(t, conn)

	// Create a test gate to block the writeLoop before draining slots.
	// This ensures multiple publishes accumulate before any flush occurs,
	// forcing deterministic coalescing.
	gate := make(chan struct{})
	hub.setTestWaitBeforeDrain(gate)

	// Apply many mutations back-to-back without reading in between. The
	// writeLoop will wake up on each publish but block on the gate, allowing
	// all publishes to accumulate in the coalescing slots.
	const n = 20
	for i := 0; i < n; i++ {
		params, _ := json.Marshal(state.LabelParams{Label: "irrelevant"})
		id := state.NewSessionID()
		if err := catalog.PutSession(state.LocalSessionRecord{
			ID:         id,
			Owner:      catalog.Owner(),
			Ref:        state.SessionRef{Owner: catalog.Owner(), Session: id},
			Phase:      state.SessionPhaseActive,
			Generation: "test-gen",
		}); err != nil {
			t.Fatalf("PutSession %d: %v", i, err)
		}
		_ = params
	}

	// Close the gate to unblock the writeLoop, allowing it to drain all
	// accumulated revisions. The coalescing logic ensures it sends only the
	// latest catalog revision, not intermediate ones.
	close(gate)

	// Drain every frame the writer actually sent. Coalescing means this must
	// be fewer than n frames, each carrying a strictly increasing, complete
	// revision, and the final frame must reflect all n sessions -- proving a
	// dropped intermediate frame is always repaired by a later complete
	// snapshot.
	var frames int
	var lastCount int
	var lastRevision float64
	for {
		_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var msg map[string]any
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if msg["type"] != "catalog_snapshot" {
			t.Fatalf("expected catalog_snapshot, got %v", msg["type"])
		}
		snap, ok := msg["snapshot"].(map[string]any)
		if !ok {
			t.Fatalf("expected snapshot object, got %v", msg["snapshot"])
		}
		sessions, _ := snap["sessions"].([]any)
		revision, _ := snap["revision"].(float64)
		if revision <= lastRevision {
			t.Fatalf("expected strictly increasing revisions, got %v after %v", revision, lastRevision)
		}
		lastRevision = revision
		lastCount = len(sessions)
		frames++
	}
	if frames == 0 {
		t.Fatal("expected at least one frame")
	}
	if frames >= n {
		t.Fatalf("expected coalescing to yield fewer than %d frames, got %d", n, frames)
	}
	if lastCount != n {
		t.Fatalf("expected the final frame to carry all %d sessions, got %d", n, lastCount)
	}
}

// TestStateStreamEnqueueCloseRaceNoPanic stresses enqueue racing with close.
// Before the done-channel redesign, close() closed the signal channel while a
// concurrent enqueue could still be about to send on it, panicking with
// "send on closed channel" whenever a write failure raced with shutdown.
// signal is now never closed; close() closes done instead, so the race cannot
// panic. This test hammers enqueue from many goroutines while close() runs
// concurrently (simulating a write failure and an external shutdown racing),
// with the peer websocket closed so in-flight client writes fail immediately.
// Run under -race to also verify no data races.
func TestStateStreamEnqueueCloseRaceNoPanic(t *testing.T) {
	serverConns := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc, err := stateStreamUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("server upgrade: %v", err)
			return
		}
		// Hand the upgraded conn to the test goroutine and return: gorilla's
		// Upgrade hijacks the underlying net.Conn from the HTTP server, so the
		// connection stays open after this handler returns. The test below is
		// the sole receiver on serverConns and closes sc itself to force
		// client writes to fail immediately.
		serverConns <- sc
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	sc := <-serverConns
	// Close the server side so any client write (in writeLoop) fails
	// immediately, exercising the write-failure -> close() path concurrently
	// with the external close() calls below.
	_ = sc.Close()

	c := newStateStreamClient(conn, nil)
	defer c.close()

	var wg sync.WaitGroup
	const producers = 8
	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			base := int64(worker) * 10000
			for j := int64(0); j < 2000; j++ {
				c.publishCatalogKeyed(localCatalogSlotKey, base+j, false, []byte("payload"))
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				c.close()
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("enqueue/close stress timed out")
	}
}

// TestStateStreamOneSlowClientDoesNotBlockOthers verifies that publishing to
// one client that never reads does not prevent another client from receiving
// updates -- the encode-and-fan-out path must not block on any single
// client's write.
func TestStateStreamOneSlowClientDoesNotBlockOthers(t *testing.T) {
	catalog := newTestCatalog(t)
	hub := NewStateStreamHub(catalog, nil)
	defer hub.Close()

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleState))
	defer srv.Close()

	slow := dialStateStream(t, srv)
	defer slow.Close()
	fast := dialStateStream(t, srv)
	defer fast.Close()

	// Drain both initial snapshots on the fast client only; the slow client is
	// intentionally never read from again.
	_ = readTyped(t, fast)

	id := state.NewSessionID()
	if err := catalog.PutSession(state.LocalSessionRecord{
		ID:         id,
		Owner:      catalog.Owner(),
		Ref:        state.SessionRef{Owner: catalog.Owner(), Session: id},
		Phase:      state.SessionPhaseActive,
		Generation: "test-gen",
	}); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	msg := readTyped(t, fast)
	if msg["type"] != "catalog_snapshot" {
		t.Fatalf("expected catalog_snapshot, got %v", msg["type"])
	}
}

// firstStateStreamClient returns the sole connected client tracked by h.
// Test-only helper (same package): reaches into hub internals to drive a
// client's coalescing slot directly, so the adversarial interleaving tests
// below can force the exact call order described in the race report without
// depending on goroutine scheduling.
func firstStateStreamClient(t *testing.T, h *StateStreamHub) *stateStreamClient {
	t.Helper()
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		return c
	}
	t.Fatal("no connected client")
	return nil
}

// TestStateStreamAdversarialOrderKeepsHigherRevision reproduces, deterministically,
// the exact race described for this bug: an "initial snapshot" capture at
// revision 8 that is enqueued to a client's coalescing slot AFTER a
// concurrent "live update" at revision 9 has already been enqueued to the
// same slot. Before the fix, coalescing was keyed on a hub-assigned publish
// sequence number (which always favors whichever call reaches the slot
// last, i.e. the stale revision-8 read in this scenario) instead of the
// owner's own authoritative catalog revision. The fix requires the stale,
// later-arriving revision-8 publish to be rejected and the client to retain
// (and eventually receive) revision 9.
func TestStateStreamAdversarialOrderKeepsHigherRevision(t *testing.T) {
	catalog := newTestCatalog(t)
	hub := NewStateStreamHub(catalog, nil)
	defer hub.Close()

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleState))
	defer srv.Close()

	conn := dialStateStream(t, srv)
	defer conn.Close()

	// Drain the initial complete snapshot sent on connect.
	_ = readTyped(t, conn)

	c := firstStateStreamClient(t, hub)

	// Hold the writer's drain open so both adversarial publishes accumulate
	// in the slot before either is flushed to the wire -- this is the
	// existing test-gate pattern used by TestStateStreamCoalescesToLatestRevision.
	gate := make(chan struct{})
	hub.setTestWaitBeforeDrain(gate)

	encode := func(rev int64) []byte {
		b, err := json.Marshal(catalogSnapshotMessage{
			Type:     "catalog_snapshot",
			Snapshot: state.OwnerCatalogSnapshot{Owner: catalog.Owner(), Revision: rev},
			IsLocal:  true,
		})
		if err != nil {
			t.Fatalf("marshal rev %d: %v", rev, err)
		}
		return b
	}

	// Adversarial order: the "concurrent live update" (revision 9) reaches
	// the slot FIRST in real time, then the "initial snapshot" (revision 8,
	// captured earlier but enqueued later, e.g. read before registration
	// completed) reaches the SAME slot second. A hub-sequence-keyed
	// coalescer would let the later call (revision 8) win; a
	// revision-keyed coalescer must reject it.
	c.publishCatalogKeyed(localCatalogSlotKey, 9, false, encode(9))
	c.publishCatalogKeyed(localCatalogSlotKey, 8, false, encode(8))

	close(gate)

	msg := readTyped(t, conn)
	if msg["type"] != "catalog_snapshot" {
		t.Fatalf("expected catalog_snapshot, got %v", msg["type"])
	}
	snap, ok := msg["snapshot"].(map[string]any)
	if !ok {
		t.Fatalf("expected snapshot object, got %v", msg["snapshot"])
	}
	if rev, _ := snap["revision"].(float64); rev != 9 {
		t.Fatalf("expected the client to retain revision 9 despite the later stale revision-8 publish, got %v", rev)
	}
}

// TestStateStreamRemovalTombstoneSurvivesStaleLateSnapshot proves an
// owner-removal (tombstone) cannot be silently overwritten by a stale,
// lower-revision snapshot that is delivered out of order AFTER the removal
// -- and that the tombstone itself is never dropped merely because a
// removal message carries no meaningful revision number of its own.
func TestStateStreamRemovalTombstoneSurvivesStaleLateSnapshot(t *testing.T) {
	catalog := newTestCatalog(t)
	hub := NewStateStreamHub(catalog, nil)
	defer hub.Close()

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleState))
	defer srv.Close()

	conn := dialStateStream(t, srv)
	defer conn.Close()

	_ = readTyped(t, conn)

	c := firstStateStreamClient(t, hub)
	remoteOwner := state.NewOwnerID()
	key := remoteCatalogSlotKey(remoteOwner)

	encodeSnapshot := func(rev int64) []byte {
		b, err := json.Marshal(catalogSnapshotMessage{
			Type:     "catalog_snapshot",
			Snapshot: state.OwnerCatalogSnapshot{Owner: remoteOwner, Revision: rev},
			IsLocal:  false,
		})
		if err != nil {
			t.Fatalf("marshal rev %d: %v", rev, err)
		}
		return b
	}
	encodeRemoval := func() []byte {
		b, err := json.Marshal(catalogOwnerRemovedMessage{Type: "catalog_owner_removed", Owner: remoteOwner})
		if err != nil {
			t.Fatalf("marshal removal: %v", err)
		}
		return b
	}

	gate := make(chan struct{})
	hub.setTestWaitBeforeDrain(gate)

	// The owner is known at revision 9, then removed. A stale snapshot at
	// revision 8 (older than the last known revision) is then delivered out
	// of order, after the removal reached the slot.
	c.publishCatalogKeyed(key, 9, false, encodeSnapshot(9))
	c.publishCatalogKeyed(key, 0, true, encodeRemoval()) // removal carries no meaningful revision of its own
	c.publishCatalogKeyed(key, 8, false, encodeSnapshot(8))

	close(gate)

	msg := readTyped(t, conn)
	if msg["type"] != "catalog_owner_removed" {
		t.Fatalf("expected the tombstone to survive the stale late snapshot, got %v", msg["type"])
	}
	if msg["owner"] != string(remoteOwner) {
		t.Fatalf("expected removal for owner %q, got %v", remoteOwner, msg["owner"])
	}

	// A genuinely newer snapshot (revision 10, greater than the retained
	// revision 9) must still be able to supersede the tombstone -- the
	// removal is not a permanent lock, only a guard against stale replays.
	gate2 := make(chan struct{})
	hub.setTestWaitBeforeDrain(gate2)
	c.publishCatalogKeyed(key, 10, false, encodeSnapshot(10))
	close(gate2)

	msg2 := readTyped(t, conn)
	if msg2["type"] != "catalog_snapshot" {
		t.Fatalf("expected a genuinely newer snapshot to supersede the tombstone, got %v", msg2["type"])
	}
	snap, ok := msg2["snapshot"].(map[string]any)
	if !ok {
		t.Fatalf("expected snapshot object, got %v", msg2["snapshot"])
	}
	if rev, _ := snap["revision"].(float64); rev != 10 {
		t.Fatalf("expected revision 10, got %v", rev)
	}
}
