package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/anh-chu/termyard/pkg/state"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

// TestHubBroadcastArtifactEvent verifies that a Kind:"artifact" event pushed
// through the Tracker's subscriber channel is routed to broadcastArtifactEvent
// (not the plain tool-event wrap), and that connected WebSocket clients
// receive a {"type":"artifacts",...} frame with the artifact payload intact —
// distinct from the {"type":"tool-event",...} shape used for real hook events.
func TestHubBroadcastArtifactEvent(t *testing.T) {
	tracker := toolevents.NewTracker()
	stateMgr := state.NewManager()
	hub := NewHub(stateMgr, tracker)
	go hub.Run(context.Background())

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleEvents))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Drain the welcome frame, then give HandleEvents a moment to finish
	// registering the client into h.clients (registration happens right
	// after the welcome write, on the same goroutine — short, reliable
	// window; widen if this proves flaky in CI).
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	arts := []*toolevents.FileArtifact{{Path: "/tmp/report.pdf", Name: "report.pdf", Source: "regex"}}
	tracker.RecordArtifacts("my-session", arts)

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read artifact broadcast: %v", err)
	}

	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg["type"] != "artifacts" {
		t.Fatalf("expected type=artifacts, got %v (full msg: %s)", msg["type"], data)
	}
	if msg["session"] != "my-session" {
		t.Fatalf("expected session=my-session, got %v", msg["session"])
	}
	if _, hasTool := msg["tool"]; hasTool {
		t.Fatalf("artifact broadcast must not carry a tool-event field, got: %s", data)
	}
	gotArts, ok := msg["artifacts"].([]any)
	if !ok || len(gotArts) != 1 {
		t.Fatalf("expected 1 artifact in payload, got: %s", data)
	}
}
