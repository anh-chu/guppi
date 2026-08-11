package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/groupsync"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/namer"
	"github.com/anh-chu/termyard/pkg/state"
)

func TestGroupNaming_CreationAtTwoMembersTriggersGeneration(t *testing.T) {
	c, store, stateMgr, srv, count := setupTestCoordinator(t)
	defer srv.Close()

	addTestSessions(stateMgr, "alpha", "beta")
	after, _ := store.SetTree("g1", tree("alpha", "beta"))

	c.ObserveTreeMutation("g1", groupsync.Group{}, after)

	name := waitName(t, store, "g1")
	if name != "name-2" {
		t.Fatalf("expected generated name-2, got %q", name)
	}
	if got := count.Load(); got != 1 {
		t.Fatalf("expected 1 generation request, got %d", got)
	}
}

func TestGroupNaming_RatioReorderRankMetadataDoNotTrigger(t *testing.T) {
	c, store, stateMgr, srv, count := setupTestCoordinator(t)
	defer srv.Close()

	addTestSessions(stateMgr, "alpha", "beta")
	_, _ = store.SetTree("g1", tree("alpha", "beta"))
	before, _ := store.Get("g1")
	// Swapped leaf order and different split direction/ratio; membership identical.
	after, _ := store.SetTree("g1", json.RawMessage(`{"type":"split","direction":"v","ratio":0.25,"first":{"type":"leaf","sessionKey":"beta"},"second":{"type":"leaf","sessionKey":"alpha"}}`))

	c.ObserveTreeMutation("g1", before, after)
	time.Sleep(150 * time.Millisecond)

	if count.Load() != 0 {
		t.Fatalf("expected no generation for ratio/reorder change, got %d", count.Load())
	}
}

func TestGroupNaming_AddRemoveMembershipTriggersRefresh(t *testing.T) {
	c, store, stateMgr, srv, count := setupTestCoordinator(t)
	defer srv.Close()

	addTestSessions(stateMgr, "alpha", "beta")
	g, _ := store.SetTree("g1", tree("alpha", "beta"))
	c.ObserveTreeMutation("g1", groupsync.Group{}, g)
	waitForNamed(t, store, "g1", "name-2")

	addTestSessions(stateMgr, "alpha", "beta", "gamma")
	before, _ := store.Get("g1")
	after, _ := store.SetTree("g1", tree("alpha", "beta", "gamma"))
	c.ObserveTreeMutation("g1", before, after)
	waitForNamed(t, store, "g1", "name-3")

	final, _ := store.Get("g1")
	if final.Name != "name-3" {
		t.Fatalf("expected name-3 after membership refresh, got %q", final.Name)
	}
	if got := count.Load(); got != 2 {
		t.Fatalf("expected 2 generation requests, got %d", got)
	}
}

func TestGroupNaming_ManualModeBlocksAutomaticTrigger(t *testing.T) {
	c, store, stateMgr, srv, count := setupTestCoordinator(t)
	defer srv.Close()

	addTestSessions(stateMgr, "alpha", "beta")
	g, _ := store.SetTree("g1", tree("alpha", "beta"))
	g, _ = store.SetName("g1", "user-name", groupsync.NameModeManual)

	c.ObserveTreeMutation("g1", groupsync.Group{}, g)
	time.Sleep(150 * time.Millisecond)
	if count.Load() != 0 {
		t.Fatalf("expected no generation in manual mode, got %d", count.Load())
	}

	// Membership change should also be ignored.
	before := g
	after, _ := store.SetTree("g1", tree("alpha", "beta", "gamma"))
	c.ObserveTreeMutation("g1", before, after)
	time.Sleep(150 * time.Millisecond)
	if count.Load() != 0 {
		t.Fatalf("expected no generation after manual mode membership change, got %d", count.Load())
	}
}

func TestGroupNaming_BurstCoalescesToLatestFingerprint(t *testing.T) {
	c, store, stateMgr, srv, count := setupTestCoordinator(t)
	defer srv.Close()

	addTestSessions(stateMgr, "alpha", "beta", "gamma", "delta")
	g, _ := store.SetTree("g1", tree("alpha", "beta"))
	c.ObserveTreeMutation("g1", groupsync.Group{}, g)
	waitForNamed(t, store, "g1", "name-2")
	waitForCount(t, count, 1)
	count.Store(0)

	before, _ := store.Get("g1")
	mid, _ := store.SetTree("g1", tree("alpha", "beta", "gamma"))
	c.ObserveTreeMutation("g1", before, mid)

	time.Sleep(10 * time.Millisecond)

	after, _ := store.SetTree("g1", tree("alpha", "beta", "delta"))
	c.ObserveTreeMutation("g1", mid, after)

	waitForNamed(t, store, "g1", "name-3")
	waitForCount(t, count, 1)

	final, _ := store.Get("g1")
	if final.Name != "name-3" {
		t.Fatalf("expected burst to coalesce to latest membership name-3, got %q", final.Name)
	}

	// The persisted membership fingerprint should match the latest tree.
	fpAfter, _, _ := groupsync.MembershipFingerprint(after.Tree)
	fpNow, _, _ := groupsync.MembershipFingerprint(final.Tree)
	if fpAfter != fpNow {
		t.Fatalf("coalesced generation applied to stale membership")
	}
}

func TestGroupNaming_StaleResultDiscarded(t *testing.T) {
	var (
		blockFrom int32 = 2
		blockCh         = make(chan struct{})
	)
	c, store, stateMgr, srv, count := setupTestCoordinatorWithBlock(t, &blockFrom, blockCh)
	defer srv.Close()
	c.debounce = 200 * time.Millisecond

	addTestSessions(stateMgr, "alpha", "beta", "gamma", "delta")
	g, _ := store.SetTree("g1", tree("alpha", "beta"))
	c.ObserveTreeMutation("g1", groupsync.Group{}, g)
	waitName(t, store, "g1")

	// Next request will block so we can race a membership change before it completes.
	before, _ := store.Get("g1")
	after, _ := store.SetTree("g1", tree("alpha", "beta", "gamma"))
	c.ObserveTreeMutation("g1", before, after)

	waitForCount(t, count, 2)

	// While the namer is still responding for {alpha,beta,gamma}, change membership.
	_, _ = store.SetTree("g1", tree("alpha", "beta", "delta"))

	close(blockCh)
	waitForCount(t, count, 2)

	final := groupName(store, "g1")
	if final != "name-2" {
		t.Fatalf("stale generation should have been discarded, got name %q", final)
	}
}

func TestGroupNaming_ForceBypassesGateAndSwitchesToAuto(t *testing.T) {
	c, store, stateMgr, srv, count := setupTestCoordinator(t)
	defer srv.Close()

	addTestSessions(stateMgr, "alpha", "beta", "gamma")
	_, _ = store.SetTree("g1", tree("alpha", "beta"))
	_, _ = store.SetName("g1", "manual", groupsync.NameModeManual)

	// Lock the gate so automatic attempts would be blocked.
	c.gate = namer.NewAutomaticGate(namer.AutomaticPolicy{
		NormalInterval: 24 * time.Hour,
		BackoffSteps:   []time.Duration{24 * time.Hour},
	})
	c.gate.Begin("g1")
	c.gate.Success("g1")

	g, err := c.Force(context.Background(), "g1")
	if err != nil {
		t.Fatalf("Force failed: %v", err)
	}
	if g.Name != "name-2" {
		t.Fatalf("Force should apply name-2, got %q", g.Name)
	}
	if groupsync.EffectiveNameMode(g) != groupsync.NameModeAuto {
		t.Fatalf("Force should switch group back to auto mode")
	}

	// After the forced success the gate is reset, so a subsequent automatic
	// mutation should still be able to run.
	before := g
	after, _ := store.SetTree("g1", tree("alpha", "beta", "gamma"))
	c.ObserveTreeMutation("g1", before, after)
	waitForNamed(t, store, "g1", "name-3")

	final, _ := store.Get("g1")
	if final.Name != "name-3" {
		t.Fatalf("expected Force + subsequent auto to end at name-3, got %q", final.Name)
	}
	if got := count.Load(); got != 2 {
		t.Fatalf("expected Force + subsequent auto = 2 generations, got %d", got)
	}
}

// ---------- helpers ----------

type fakeTimer struct{}

func (fakeTimer) Stop() bool { return true }

func testSessions(names ...string) []*model.Session {
	sessions := make([]*model.Session, len(names))
	for i, n := range names {
		sessions[i] = &model.Session{
			Name:        n,
			DisplayName: fmt.Sprintf("Display-%s", n),
			AgentType:   "claude",
			ProjectPath: "/project/" + n,
			UserPrompt:  "prompt " + n,
		}
	}
	return sessions
}

// addTestSessions adds test sessions to the state manager.
func addTestSessions(stateMgr *state.Manager, names ...string) {
	for _, sess := range testSessions(names...) {
		stateMgr.AddSession(sess)
	}
}

func tree(members ...string) json.RawMessage {
	if len(members) == 0 {
		return json.RawMessage(`{}`)
	}
	if len(members) == 1 {
		b, _ := json.Marshal(map[string]interface{}{
			"type":       "leaf",
			"sessionKey": members[0],
		})
		return b
	}
	if len(members) == 2 {
		b, _ := json.Marshal(map[string]interface{}{
			"type":      "split",
			"direction": "h",
			"ratio":     0.5,
			"first": map[string]interface{}{
				"type":       "leaf",
				"sessionKey": members[0],
			},
			"second": map[string]interface{}{
				"type":       "leaf",
				"sessionKey": members[1],
			},
		})
		return b
	}
	return json.RawMessage(fmt.Sprintf(`{"type":"split","direction":"h","ratio":0.5,"first":{"type":"leaf","sessionKey":%q},"second":%s}`, members[0], string(tree(members[1:]...))))
}

func setupTestCoordinator(t *testing.T) (*groupNamingCoordinator, *groupsync.Store, *state.Manager, *httptest.Server, *atomic.Int32) {
	return setupTestCoordinatorWithBlock(t, nil, nil)
}

func setupTestCoordinatorWithBlock(t *testing.T, blockFrom *int32, blockCh chan struct{}) (*groupNamingCoordinator, *groupsync.Store, *state.Manager, *httptest.Server, *atomic.Int32) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	stateMgr := state.NewManager()
	store, err := groupsync.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if blockFrom != nil && n >= *blockFrom && blockCh != nil {
			<-blockCh
		}
		members := countMembersInRequest(r)
		resp := fmt.Sprintf(`{"choices":[{"message":{"content":"{\"name\": \"name-%d\"}"}}]}`, members)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))

	cfg := namer.Config{Endpoint: srv.URL, Model: "test"}
	stateMgr.SetNamer(namer.New(cfg))

	opts := &Options{StateMgr: stateMgr, GroupStore: store}
	c := newGroupNamingCoordinator(context.Background(), opts, nil)
	c.debounce = 30 * time.Millisecond
	c.gate = namer.NewAutomaticGate(namer.AutomaticPolicy{
		NormalInterval: 1 * time.Millisecond,
		BackoffSteps:   []time.Duration{1 * time.Millisecond},
	})
	return c, store, stateMgr, srv, &count
}

func waitName(t *testing.T, store *groupsync.Store, id string) string {
	t.Helper()
	return waitForNamed(t, store, id, "")
}

// waitForNamed waits until the group name equals want. An empty want matches
// any non-empty name.
func waitForNamed(t *testing.T, store *groupsync.Store, id, want string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		g, _ := store.Get(id)
		if want != "" && g.Name == want {
			return g.Name
		}
		if want == "" && g.Name != "" {
			return g.Name
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for group %s name %q", id, want)
	return ""
}

func waitForCount(t *testing.T, count *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if count.Load() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for generation count %d, got %d", want, count.Load())
}

func groupName(store *groupsync.Store, id string) string {
	g, _ := store.Get(id)
	return g.Name
}

// countMembersInRequest reads the last user message from a namer request and
// counts the member bullet lines so tests can tell which membership snapshot
// was sent.
func countMembersInRequest(r *http.Request) int {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var body struct {
		Messages []msg `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return 0
	}
	var content string
	for i := len(body.Messages) - 1; i >= 0; i-- {
		if body.Messages[i].Role == "user" {
			content = body.Messages[i].Content
			break
		}
	}
	n := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "  - ") {
			n++
		}
	}
	return n
}
