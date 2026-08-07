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
	deps := SessionDeps{Manager: mgr, LocalMgr: mgr.localMgr}

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
	mgr.RegisterPeer("peer-a", "remote-a", "", nil)
	pc := NewPeerConnection("peer", 1)
	deps := SessionDeps{Manager: mgr, LocalMgr: mgr.localMgr}
	log := logrus.NewEntry(logrus.New())

	payload := StateUpdatePayload{
		Sessions: []*model.Session{{Name: "remote"}},
		Version:  "v-test",
	}
	msg, err := NewMessage(MsgStateUpdate, payload)
	if err != nil {
		t.Fatal(err)
	}

	handleStateMessage("peer-a", msg, pc, deps, log)

	got := getPeerSessions(mgr, "peer-a")
	if len(got) != 1 || got[0].Name != "remote" {
		t.Fatalf("expected remote session, got %+v", got)
	}
}
