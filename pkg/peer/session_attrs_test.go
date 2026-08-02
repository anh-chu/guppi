package peer

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

type fakeAttrSink struct {
	attrs map[string]SessionAttr
	delta bool
}

func (f *fakeAttrSink) ApplyRemoteDelta(key string, background, hidden bool, scheduleID string, updatedAt time.Time) (bool, error) {
	f.delta = true
	return true, nil
}

func (f *fakeAttrSink) ApplyRemoteSnapshot(attrs map[string]SessionAttr) ([]string, error) {
	return []string{"k"}, nil
}

func (f *fakeAttrSink) SnapshotAttrs() map[string]SessionAttr { return f.attrs }

func (f *fakeAttrSink) SetScheduleID(key, scheduleID string) error { return nil }

type fakeOrderSink struct {
	orders map[string]SessionOrder
}

func (f *fakeOrderSink) ApplyRemoteDelta(key, rank string, updatedAt time.Time) (bool, error) {
	return true, nil
}

func (f *fakeOrderSink) ApplyRemoteSnapshot(orders map[string]SessionOrder) ([]string, error) {
	return []string{"k"}, nil
}

func (f *fakeOrderSink) SnapshotOrders() map[string]SessionOrder { return f.orders }

type fakeGroupSink struct {
	groups map[string]Group
}

func (f *fakeGroupSink) ApplyRemoteDelta(id string, group Group) (bool, error) { return true, nil }

func (f *fakeGroupSink) ApplyRemoteSnapshot(groups map[string]Group) ([]string, error) {
	return []string{"g"}, nil
}

func (f *fakeGroupSink) SnapshotGroups() map[string]Group { return f.groups }

type fakeBrowserHub struct {
	calls []map[string]interface{}
}

func (h *fakeBrowserHub) BroadcastJSON(v interface{}) {
	if m, ok := v.(map[string]interface{}); ok {
		h.calls = append(h.calls, m)
	}
}

func makeLocalDeps(t *testing.T) SessionDeps {
	t.Helper()
	mgr := makeTestManager(t)
	return SessionDeps{
		Manager:    mgr,
		LocalMgr:   mgr.localMgr,
		Identity:   mgr.identity,
		BrowserHub: &fakeBrowserHub{},
	}
}

func TestSendInitialSessionAttrs(t *testing.T) {
	deps := makeLocalDeps(t)
	pc := NewPeerConnection("peer", 1)
	sink := &fakeAttrSink{attrs: map[string]SessionAttr{"k": {UpdatedAt: time.Now()}}}
	deps.AttrsSink = sink

	sendInitialSessionAttrs(pc, deps)

	assertMessageType(t, pc, MsgSessionAttrsSnapshot)
}

func TestSendInitialSessionOrder(t *testing.T) {
	deps := makeLocalDeps(t)
	pc := NewPeerConnection("peer", 1)
	sink := &fakeOrderSink{orders: map[string]SessionOrder{"k": {Rank: "1"}}}
	deps.OrderSink = sink

	sendInitialSessionOrder(pc, deps)

	assertMessageType(t, pc, MsgSessionOrderSnapshot)
}

func TestSendInitialGroups(t *testing.T) {
	deps := makeLocalDeps(t)
	pc := NewPeerConnection("peer", 1)
	sink := &fakeGroupSink{groups: map[string]Group{"g": {Name: "grp"}}}
	deps.GroupSink = sink

	sendInitialGroups(pc, deps)

	assertMessageType(t, pc, MsgGroupSnapshot)
}

func TestHandleAttrsMessageSnapshot(t *testing.T) {
	deps := makeLocalDeps(t)
	pc := NewPeerConnection("peer", 1)
	sink := &fakeAttrSink{attrs: map[string]SessionAttr{"k": {}}}
	deps.AttrsSink = sink

	msg, err := NewMessage(MsgSessionAttrsSnapshot, SessionAttrsSnapshotPayload{
		Origin: "other-node",
		Attrs:  sink.attrs,
	})
	if err != nil {
		t.Fatal(err)
	}

	handleAttrsMessage("peer", msg, pc, deps, logrus.NewEntry(logrus.New()))

	hub := deps.BrowserHub.(*fakeBrowserHub)
	if len(hub.calls) != 1 || hub.calls[0]["type"] != "session-attrs-updated" {
		t.Fatalf("expected attrs update broadcast, got %+v", hub.calls)
	}
}

func TestHandleAttrsMessageDelta(t *testing.T) {
	deps := makeLocalDeps(t)
	pc := NewPeerConnection("peer", 1)
	sink := &fakeAttrSink{}
	deps.AttrsSink = sink

	msg, err := NewMessage(MsgSessionAttrsDelta, SessionAttrsDeltaPayload{
		Origin: "other-node",
		Key:    "k",
		Attr:   SessionAttr{UpdatedAt: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}

	handleAttrsMessage("peer", msg, pc, deps, logrus.NewEntry(logrus.New()))

	if !sink.delta {
		t.Fatal("expected delta to be applied")
	}
	hub := deps.BrowserHub.(*fakeBrowserHub)
	if len(hub.calls) != 1 || hub.calls[0]["type"] != "session-attrs-updated" {
		t.Fatalf("expected attrs update broadcast, got %+v", hub.calls)
	}
}

func TestHandleAttrsMessageIgnoresOwnOrigin(t *testing.T) {
	deps := makeLocalDeps(t)
	pc := NewPeerConnection("peer", 1)
	sink := &fakeAttrSink{attrs: map[string]SessionAttr{"k": {}}}
	deps.AttrsSink = sink

	msg, err := NewMessage(MsgSessionAttrsSnapshot, SessionAttrsSnapshotPayload{
		Origin: deps.Identity.Fingerprint(),
		Attrs:  sink.attrs,
	})
	if err != nil {
		t.Fatal(err)
	}

	handleAttrsMessage("peer", msg, pc, deps, logrus.NewEntry(logrus.New()))

	hub := deps.BrowserHub.(*fakeBrowserHub)
	if len(hub.calls) != 0 {
		t.Fatalf("expected no broadcast for own origin, got %+v", hub.calls)
	}
}

func assertMessageType(t *testing.T, pc *PeerConnection, want string) {
	t.Helper()
	select {
	case f := <-pc.LoLane():
		var msg Message
		if err := json.Unmarshal(f.data, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type != want {
			t.Fatalf("expected %s, got %s", want, msg.Type)
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for %s", want)
	}
}
