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

func (f *fakeGroupSink) ApplyRemoteDelta(id string, group Group) (bool, map[string]Group, map[string]Group, error) { 
	return true, nil, nil, nil 
}

func (f *fakeGroupSink) ApplyRemoteSnapshot(groups map[string]Group) ([]string, map[string]Group, map[string]Group, error) {
	return []string{"g"}, nil, nil, nil
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

// TestPeerEnforcementPropagation verifies that peer deltas triggering group
// enforcement fanout loser records to peers.
func TestPeerEnforcementPropagation(t *testing.T) {
	deps := makeLocalDeps(t)

	// Setup enforcing group sink that returns enforced/prior groups
	fanoutedGroups := []string{}

	deps.GroupFanoutCallback = func(id string, g Group) {
		fanoutedGroups = append(fanoutedGroups, id)
	}

	// Create a sink that returns enforced groups (tombstoned g2 loses to g1)
	sink := &testEnforcingGroupSink{}
	deps.GroupSink = sink
	deps.BrowserHub = &fakeBrowserHub{}

	pc := NewPeerConnection("peer", 1)

	// Build a delta that enforces g2 as loser (tombstoned)
	loserGroup := Group{
		Tree:          []byte(`{"type":"leaf","sessionKey":"loser"}`),
		TreeUpdatedAt: time.Now(),
		DeletedAt:     time.Now(), // tombstoned
	}
	// Build enforced prior state (live)
	priorGroup := Group{
		Tree:          []byte(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":"loser"},"second":{"type":"leaf","sessionKey":"other"}}`),
		TreeUpdatedAt: time.Now().Add(-time.Hour),
	}

	sink.enforcedGroups = map[string]Group{"g2": loserGroup}
	sink.enforcedPrior = map[string]Group{"g2": priorGroup}

	// Send a group delta from remote that triggers enforcement
	msg, _ := NewMessage(MsgGroupDelta, GroupDeltaPayload{
		Origin: "remote-host",
		ID:     "g1",
		Group: Group{
			Tree:          []byte(`{"type":"leaf","sessionKey":"loser"}`),
			TreeUpdatedAt: time.Now(),
		},
	})

	// Process the message
	handleAttrsMessage("peer", msg, pc, deps, logrus.NewEntry(logrus.New()))

	// Verify loser was fanned out to peers
	if len(fanoutedGroups) == 0 {
		t.Fatalf("expected fanout callback to be called for enforced groups, got %d", len(fanoutedGroups))
	}
	if fanoutedGroups[0] != "g2" {
		t.Fatalf("expected g2 to be fanned out, got %v", fanoutedGroups)
	}
}

// testEnforcingGroupSink simulates a sink that returns enforced groups due to
// membership enforcement (e.g. key wins in another group, so losers are pruned).
// The test uses this to inject enforced groups as if enforcement had occurred.
type testEnforcingGroupSink struct {
	enforcedGroups map[string]Group
	enforcedPrior  map[string]Group
}

func (s *testEnforcingGroupSink) ApplyRemoteDelta(id string, group Group) (bool, map[string]Group, map[string]Group, error) {
	return true, s.enforcedGroups, s.enforcedPrior, nil
}

func (s *testEnforcingGroupSink) ApplyRemoteSnapshot(groups map[string]Group) ([]string, map[string]Group, map[string]Group, error) {
	return []string{"g"}, s.enforcedGroups, s.enforcedPrior, nil
}

func (s *testEnforcingGroupSink) SnapshotGroups() map[string]Group {
	return nil
}
