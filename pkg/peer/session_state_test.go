package peer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/common"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/state"
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
	return NewManager(id, ps, state.NewManager())
}

func TestGetPeerSessions(t *testing.T) {
	mgr := makeTestManager(t)
	want := []*model.Session{{Name: "alpha"}}
	mgr.hosts["peer-a"] = &HostState{ID: "peer-a", Sessions: want}

	got := getPeerSessions(mgr, "peer-a")
	if len(got) != 1 || got[0].Name != "alpha" {
		t.Fatalf("got %+v, want alpha", got)
	}

	if got := getPeerSessions(mgr, "missing"); got != nil {
		t.Fatalf("expected nil for missing peer, got %+v", got)
	}
}

func TestSendStateUpdate(t *testing.T) {
	mgr := makeTestManager(t)
	pc := NewPeerConnection("peer", 1)
	deps := SessionDeps{Manager: mgr, LocalMgr: mgr.localMgr.(*state.Manager)}

	sendStateUpdate(pc, deps)

	select {
	case f := <-pc.LoLane():
		var msg Message
		if err := json.Unmarshal(f.data, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type != MsgStateUpdate {
			t.Fatalf("expected %s, got %s", MsgStateUpdate, msg.Type)
		}
		var p StateUpdatePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.Version != common.VERSION {
			t.Fatalf("expected version %q, got %q", common.VERSION, p.Version)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for state-update")
	}
}

func TestHandleStateMessageUpdate(t *testing.T) {
	mgr := makeTestManager(t)
	mgr.RegisterPeer("peera", "remotea", "", nil)
	pc := NewPeerConnection("peer", 1)
	deps := SessionDeps{Manager: mgr, LocalMgr: mgr.localMgr.(*state.Manager)}
	log := logrus.NewEntry(logrus.New())

	payload := StateUpdatePayload{
		Sessions: []*model.Session{{Name: "remote"}},
		Version:  "v-test",
	}
	msg, err := NewMessage(MsgStateUpdate, payload)
	if err != nil {
		t.Fatal(err)
	}

	handleStateMessage("peera", msg, pc, deps, log)

	got := getPeerSessions(mgr, "peera")
	if len(got) != 1 || got[0].Name != "remote" {
		t.Fatalf("expected remote session, got %+v", got)
	}
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
	deps := SessionDeps{Manager: mgr, LocalMgr: mgr.localMgr.(*state.Manager), ToolTracker: tracker}
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

func TestHandleStateMessageRequestState(t *testing.T) {
	mgr := makeTestManager(t)
	pc := NewPeerConnection("peer", 1)
	deps := SessionDeps{Manager: mgr, LocalMgr: mgr.localMgr.(*state.Manager)}
	log := logrus.NewEntry(logrus.New())

	msg, err := NewMessage(MsgRequestState, struct{}{})
	if err != nil {
		t.Fatal(err)
	}

	handleStateMessage("peera", msg, pc, deps, log)

	select {
	case f := <-pc.LoLane():
		var msg Message
		if err := json.Unmarshal(f.data, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type != MsgStateUpdate {
			t.Fatalf("expected %s, got %s", MsgStateUpdate, msg.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for state-update")
	}
}
