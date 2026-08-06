package peer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

// makeTestDeps builds a SessionDeps + supervisor wired to an isolated HOME so
// peers.json doesn't collide.
func makeTestDeps(t *testing.T) (SessionDeps, *LinkSupervisor, *identity.PeerStore) {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	_ = os.MkdirAll(filepath.Join(tmpHome, ".config", "termyard"), 0o700)

	id, err := identity.Generate("test-node")
	if err != nil {
		t.Fatal(err)
	}
	ps, err := identity.NewPeerStore()
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(id, ps)
	deps := SessionDeps{
		Manager:     mgr,
		Identity:    id,
		ActTracker:  activity.NewTracker(),
		ToolTracker: toolevents.NewTracker(),
		PeerStore:   ps,
	}
	sup := NewLinkSupervisor(deps)
	return deps, sup, ps
}

func makePeer(t *testing.T, name string, enabled, initiated bool) identity.Peer {
	t.Helper()
	id, err := identity.Generate(name)
	if err != nil {
		t.Fatal(err)
	}
	return identity.Peer{
		Name:          name,
		PublicKey:     id.PublicKey,
		PairedAt:      time.Now(),
		Address:       "127.0.0.1:1", // unreachable; dials fail fast
		Enabled:       enabled,
		InitiatedByUs: initiated,
	}
}

func TestSpawnLinkRespectsDisabled(t *testing.T) {
	_, sup, _ := makeTestDeps(t)
	sup.Start(context.Background())

	p := makePeer(t, "disabled", false, true)
	if err := sup.AddPeer(p); err != nil {
		t.Fatal(err)
	}
	status := sup.Status()
	if len(status) != 1 || status[0].Status != StatusIdle {
		t.Fatalf("disabled peer should be StatusIdle, got %+v", status)
	}
}

func TestDialerSelectionFollowsInitiator(t *testing.T) {
	_, sup, _ := makeTestDeps(t)
	sup.Start(context.Background())

	dialer := makePeer(t, "dialer", true, true)
	listener := makePeer(t, "listener", true, false)
	if err := sup.AddPeer(dialer); err != nil {
		t.Fatal(err)
	}
	if err := sup.AddPeer(listener); err != nil {
		t.Fatal(err)
	}
	statuses := sup.Status()
	var sawListener, sawDialerLike bool
	for _, s := range statuses {
		if s.Name == "listener" && s.Status == StatusListener {
			sawListener = true
		}
		// dialer status will be StatusBackoff or StatusDialing depending on
		// timing; it must not be StatusListener.
		if s.Name == "dialer" && s.Status != StatusListener && s.Status != StatusIdle {
			sawDialerLike = true
		}
	}
	if !sawListener {
		t.Errorf("listener peer should have StatusListener, got %+v", statuses)
	}
	if !sawDialerLike {
		t.Errorf("dialer peer should not be passive, got %+v", statuses)
	}
}

func TestRemovePeerCancelsGoroutine(t *testing.T) {
	_, sup, ps := makeTestDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)

	p := makePeer(t, "removeme", true, true)
	if err := sup.AddPeer(p); err != nil {
		t.Fatal(err)
	}
	if got := len(sup.Status()); got != 1 {
		t.Fatalf("expected 1 link, got %d", got)
	}
	if err := sup.RemovePeer(p.PublicKey); err != nil {
		t.Fatal(err)
	}
	if got := len(sup.Status()); got != 0 {
		t.Fatalf("expected 0 links after remove, got %d", got)
	}
	if ps.GetByPublicKey(p.PublicKey) != nil {
		t.Errorf("peer still in store after RemovePeer")
	}
}

func TestSetEnabledTogglesStatus(t *testing.T) {
	_, sup, _ := makeTestDeps(t)
	sup.Start(context.Background())
	p := makePeer(t, "toggle", true, true)
	if err := sup.AddPeer(p); err != nil {
		t.Fatal(err)
	}
	if err := sup.SetEnabled(p.PublicKey, false); err != nil {
		t.Fatal(err)
	}
	st := sup.Status()
	if len(st) != 1 || st[0].Status != StatusIdle || st[0].Enabled {
		t.Errorf("after disable: %+v", st)
	}
	if err := sup.SetEnabled(p.PublicKey, true); err != nil {
		t.Fatal(err)
	}
	st = sup.Status()
	if len(st) != 1 || !st[0].Enabled || st[0].Status == StatusIdle {
		t.Errorf("after enable: %+v", st)
	}
}

func TestGetHostsForPeerOnlyLocal(t *testing.T) {
	_, _, _ = makeTestDeps(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	_ = os.MkdirAll(filepath.Join(tmpHome, ".config", "termyard"), 0o700)
	id, _ := identity.Generate("local-node")
	ps, _ := identity.NewPeerStore()
	mgr := NewManager(id, ps)

	// Register two fake remote peers.
	mgr.RegisterPeer("fp-a", "remote-a", "pk-a", nil)
	mgr.RegisterPeer("fp-b", "remote-b", "pk-b", nil)

	hosts := mgr.GetHostsForPeer("fp-a")
	if len(hosts) != 1 {
		t.Fatalf("expected 1 host in peer snapshot, got %d", len(hosts))
	}
	if !hosts[0].Local {
		t.Errorf("expected the lone host to be local, got %+v", hosts[0])
	}
	if hosts[0].ID != id.Fingerprint() {
		t.Errorf("expected local id, got %s", hosts[0].ID)
	}
}

func TestPeerConnectionSafeSend(t *testing.T) {
	pc := NewPeerConnection("x", 1)
	msg := &Message{Type: "t"}
	if !pc.Enqueue(msg) {
		t.Fatal("first enqueue should succeed")
	}
	// Drain.
	<-pc.LoLane()
	pc.Close()
	if pc.Enqueue(msg) {
		t.Fatal("enqueue after close should fail")
	}
	// Idempotent close.
	pc.Close()
}
