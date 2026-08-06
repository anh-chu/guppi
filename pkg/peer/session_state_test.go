package peer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

func makeTestManager(t *testing.T) *Manager {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	_ = os.MkdirAll(filepath.Join(t.TempDir(), ".config", "termyard"), 0o700)
	id, err := identity.Generate("test-node")
	if err != nil {
		t.Fatal(err)
	}
	ps, err := identity.NewPeerStore()
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(id, ps)
}

// TestHandleSessionMessage_ToolEventStampsAuthenticatedHost drives a real
// MsgToolEvent frame through the actual top-level session dispatcher
// (handleSessionMessage, the exact function runSession's read loop calls for
// every inbound frame) with a real Manager and a real toolevents.Tracker.
// The wire payload claims a bogus/spoofed host; the test asserts the
// authenticated peerID of the connection overwrites it before the event is
// recorded, and that it lands in the tracker via the same Record/broadcast
// path local tool events use.
func TestHandleSessionMessage_ToolEventStampsAuthenticatedHost(t *testing.T) {
	mgr := makeTestManager(t)
	mgr.RegisterPeer("peera", "remote-a-display-name", "", nil)
	tracker := toolevents.NewTracker()
	pc := NewPeerConnection("peer", 1)
	deps := SessionDeps{Manager: mgr, ToolTracker: tracker}
	log := logrus.NewEntry(logrus.New())

	// Subscribe before recording so we can also assert the broadcast fires,
	// the same path the WS hub relies on for local events.
	sub := tracker.Subscribe()
	defer tracker.Unsubscribe(sub)

	evt := &toolevents.Event{
		Tool:      toolevents.ToolClaude,
		Status:    toolevents.StatusWaiting,
		Host:      "spoofed-attacker-claimed-host", // must never be trusted
		Session:   "remote-session",
		SessionID: "remote-session-id",
		Window:    0,
		Pane:      "pane-1",
		Message:   "needs attention",
	}
	msg, err := NewMessage(MsgToolEvent, ToolEventPayload{Event: evt})
	if err != nil {
		t.Fatal(err)
	}

	// Go through the actual top-level dispatcher, not a handler called
	// directly, to prove the MsgToolEvent case is wired into runSession's
	// real message-type switch.
	handleSessionMessage("peera", msg, pc, deps, log)

	select {
	case got := <-sub:
		if got.Host != "peera" {
			t.Fatalf("expected broadcast event host to be authenticated peerID %q, got %q", "peera", got.Host)
		}
		if got.HostName != "remote-a-display-name" {
			t.Fatalf("expected host name %q, got %q", "remote-a-display-name", got.HostName)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for tool event broadcast")
	}

	recorded := tracker.GetForSession("remote-session-id")
	if len(recorded) != 1 {
		t.Fatalf("expected 1 recorded event, got %d", len(recorded))
	}
	if recorded[0].Host != "peera" {
		t.Fatalf("tracker recorded spoofed host %q instead of authenticated peerID %q", recorded[0].Host, "peera")
	}
}


