package ws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if err := catalog.PutLayout(state.LayoutRecord{
		ID:    state.NewLayoutID(),
		Owner: catalog.Owner(),
		Order: 1,
		Tree:  state.Leaf(state.SessionRef{Owner: catalog.Owner(), Session: state.NewSessionID()}),
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
			ID:    id,
			Owner: catalog.Owner(),
			Ref:   state.SessionRef{Owner: catalog.Owner(), Session: id},
			Phase: state.SessionPhaseActive,
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
		ID:    id,
		Owner: catalog.Owner(),
		Ref:   state.SessionRef{Owner: catalog.Owner(), Session: id},
		Phase: state.SessionPhaseActive,
	}); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	msg := readTyped(t, fast)
	if msg["type"] != "catalog_snapshot" {
		t.Fatalf("expected catalog_snapshot, got %v", msg["type"])
	}
}
