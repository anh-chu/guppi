package server

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/state"
)

// fakeRegistry records List calls and returns a stable snapshot.
type fakeRegistry struct {
	pty.Registry
	listCalls int
	sessions  []pty.SessionInfo
}

func (f *fakeRegistry) List() []pty.SessionInfo {
	f.listCalls++
	return f.sessions
}

func TestRuntimeReadyWithoutPolling(t *testing.T) {
	// The runtime must become ready immediately; any 10 ms hub-polling loop
	// would introduce visible latency here.
	rt, err := newRuntime(&cli.Command{})
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rt.Stop()

	select {
	case <-rt.Ready():
	case <-ctx.Done():
		t.Fatal("runtime did not become ready before context timeout")
	}
}

func TestRuntimeCancellationStops(t *testing.T) {
	rt, err := newRuntime(&cli.Command{})
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	<-rt.Ready()
	cancel()

	select {
	case <-rt.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("runtime context was not cancelled after Stop/cancel")
	}
}

func TestDaemonAdapterSharesSnapshot(t *testing.T) {
	reg := &fakeRegistry{
		sessions: []pty.SessionInfo{
			{ID: "alpha", Cwd: "/tmp/alpha"},
			{ID: "beta", Cwd: "/tmp/beta"},
		},
	}
	adapter := &daemonAdapter{reg: reg}

	// Both interface shapes can be assigned without a second conversion.
	var _ state.DaemonRegistry = adapter
	var _ peer.DaemonRegistry = adapter

	first := adapter.refresh()
	if len(first) != 2 {
		t.Fatalf("refresh returned %d sessions, want 2", len(first))
	}

	// List uses the cached snapshot; it does not call the registry again.
	list := adapter.List()
	if reg.listCalls != 1 {
		t.Fatalf("registry.List called %d times, want 1", reg.listCalls)
	}
	if len(list) != len(first) {
		t.Fatalf("List returned %d sessions, want %d", len(list), len(first))
	}

	// Mutating the returned slice must not affect later callers.
	list[0].ID = "mutated"
	if got := adapter.List()[0].ID; got != "alpha" {
		t.Fatalf("shared slice was mutated through List copy: got %q", got)
	}
}

func TestExecuteReturnsAssemblyError(t *testing.T) {
	// Lock the config directory so identity loading fails and we exercise the
	// error path without starting a server.
	dir := t.TempDir()
	badFile := fmt.Sprintf("%s/.config", dir)
	if err := os.WriteFile(badFile, []byte("x"), 0o600); err == nil {
		t.Setenv("HOME", dir)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Execute(ctx, &cli.Command{}); err == nil {
		t.Fatal("expected Execute to return an assembly error")
	}
}

// TestRefreshDaemonState_ClassifiesBeforePublishing verifies that crash
// classification/cleanup runs before the live state snapshot is published.
// A crashed session must not appear as live in the same refresh cycle.
func TestRefreshDaemonState_ClassifiesBeforePublishing(t *testing.T) {
	var order []string
	reg := &fakeRegistry{sessions: []pty.SessionInfo{{ID: "live"}}}
	adapter := &daemonAdapter{reg: reg}

	rt := &Runtime{
		adapter:  adapter,
		stateMgr: state.NewManager(),
		detectCrashesFn: func() []pty.LifecycleRecord {
			order = append(order, "classify")
			return []pty.LifecycleRecord{{ID: "crashed"}}
		},
	}

	rt.refreshDaemonState()

	if len(order) != 1 || order[0] != "classify" {
		t.Fatalf("expected classify call, got %v", order)
	}
	if reg.listCalls != 1 {
		t.Fatalf("expected adapter.List after classify, listCalls=%d", reg.listCalls)
	}

	sessions := rt.stateMgr.GetSessions()
	if len(sessions) != 1 || sessions[0].Name != "live" {
		t.Fatalf("expected only live session in state, got %+v", sessions)
	}
}

// TestV2RuntimeEnricherPreviewReturnsImmediately verifies that previewFor returns
// cached values immediately without blocking on PTY captures. The enricher must
// not delay catalog snapshot publication by waiting for capture operations.
func TestV2RuntimeEnricherPreviewReturnsImmediately(t *testing.T) {
	denricher := &v2RuntimeEnricher{
		adapter:    &daemonAdapter{reg: &fakeRegistry{sessions: []pty.SessionInfo{{ID: "s1", Pid: 100, ShellPid: 101}}}},
		actTracker: nil,
	}

	// First call to previewFor returns immediately with cached (empty) value.
	start := time.Now()
	preview1 := denricher.previewFor("s1")
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Fatalf("previewFor took %v, should be instant", elapsed)
	}
	if preview1 != "" {
		t.Fatalf("first previewFor should return empty cache, got %q", preview1)
	}

	// Concurrent calls also return immediately.
	start = time.Now()
	for i := 0; i < 5; i++ {
		preview := denricher.previewFor("s1")
		if preview != "" {
			t.Fatalf("previewFor should return empty cache, got %q", preview)
		}
	}
	elapsed = time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Fatalf("concurrent previewFor calls took %v, should be instant", elapsed)
	}
}

// TestV2RuntimeEnricherLastActivityFromTracker verifies that enricher only sets
// LastActivity based on real activity evidence, never from wall-clock time.
func TestV2RuntimeEnricherLastActivityFromTracker(t *testing.T) {
	tracker := activity.NewTracker()

	ref := state.SessionRef{
		Owner:   state.OwnerID("testowner"),
		Session: state.SessionID("s1"),
		Window:  0,
		Pane:    0,
	}

	enricher := &v2RuntimeEnricher{
		adapter:    &daemonAdapter{reg: &fakeRegistry{sessions: []pty.SessionInfo{{ID: "s1", Pid: 100, ShellPid: 101}}}},
		actTracker: tracker,
	}

	rec := state.LocalSessionRecord{
		ID:    state.SessionID("s1"),
		Owner: ref.Owner,
		Ref:   ref,
	}

	// Refresh adapter snapshot so sessions are visible.
	enricher.adapter.refresh()

	// Without activity evidence, LastActivity should be zero.
	rt := enricher.Enrich(ref, rec)
	if !rt.LastActivity.IsZero() {
		t.Fatalf("without activity evidence, LastActivity should be zero, got %v", rt.LastActivity)
	}

	// Record activity bytes. The tracker records activity with current time.
	tracker.Record("s1", 100)

	// Enricher should now report LastActivity based on tracker evidence.
	rt = enricher.Enrich(ref, rec)
	if rt.LastActivity.IsZero() {
		t.Fatal("with activity evidence, LastActivity should not be zero")
	}

	// Verify time is recent (within last second).
	now := time.Now()
	diff := now.Sub(rt.LastActivity)
	if diff < 0 || diff > time.Second {
		t.Fatalf("LastActivity should be recent, got %v (diff from now: %v)", rt.LastActivity, diff)
	}
}

// TestV2RuntimeEnricherLastActivityNotBumpedByReEnrichment verifies that a stale
// session's LastActivity doesn't get refreshed just by being enriched repeatedly.
func TestV2RuntimeEnricherLastActivityNotBumpedByReEnrichment(t *testing.T) {
	tracker := activity.NewTracker()

	ref := state.SessionRef{
		Owner:   state.OwnerID("testowner"),
		Session: state.SessionID("stale"),
		Window:  0,
		Pane:    0,
	}

	enricher := &v2RuntimeEnricher{
		adapter:    &daemonAdapter{reg: &fakeRegistry{sessions: []pty.SessionInfo{{ID: "stale", Pid: 100, ShellPid: 101}}}},
		actTracker: tracker,
	}

	rec := state.LocalSessionRecord{
		ID:    state.SessionID("stale"),
		Owner: ref.Owner,
		Ref:   ref,
	}

	// Refresh adapter snapshot so sessions are visible.
	enricher.adapter.refresh()

	// Record activity once.
	tracker.Record("stale", 1)

	// Enrich once, get the LastActivity.
	rt1 := enricher.Enrich(ref, rec)
	if rt1.LastActivity.IsZero() {
		t.Fatal("expected LastActivity from tracker")
	}
	firstTime := rt1.LastActivity

	// Wait a bit, then enrich again without recording new activity.
	time.Sleep(100 * time.Millisecond)
	rt2 := enricher.Enrich(ref, rec)

	// LastActivity should NOT have been bumped to time.Now(). The second
	// enrichment should report the same (or very close) time since no new
	// activity was recorded. The tracker's IdleSeconds will be higher, but
	// the reported LastActivity time should remain stable.
	diff := rt2.LastActivity.Sub(firstTime).Abs()
	if diff > 50*time.Millisecond {
		t.Fatalf("LastActivity should not change on re-enrichment without new activity: %v vs %v (diff: %v)",
			firstTime, rt2.LastActivity, diff)
	}
}

// TestNewRuntimeV2ModeSkipsLegacyStores verifies that when TERMYARD_V2_STATE=1,
// the legacy state stores (AttrsStore, OrderStore, GroupStore) are not constructed
// and remain nil in the server options.
func TestNewRuntimeV2ModeSkipsLegacyStores(t *testing.T) {
	t.Setenv("TERMYARD_V2_STATE", "1")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")

	rt, err := newRuntime(&cli.Command{})
	if err != nil {
		t.Fatalf("newRuntime in v2 mode: %v", err)
	}
	defer rt.Stop()

	// Legacy stores must be nil in v2 mode.
	if rt.opts.AttrsStore != nil {
		t.Fatal("AttrsStore should be nil in v2 mode")
	}
	if rt.opts.OrderStore != nil {
		t.Fatal("OrderStore should be nil in v2 mode")
	}
	if rt.opts.GroupStore != nil {
		t.Fatal("GroupStore should be nil in v2 mode")
	}

	// V2 components must be non-nil.
	if rt.v2Catalog == nil {
		t.Fatal("v2Catalog should not be nil in v2 mode")
	}
	if rt.v2CommandSvc == nil {
		t.Fatal("v2CommandSvc should not be nil in v2 mode")
	}
	if rt.v2StateStream == nil {
		t.Fatal("v2StateStream should not be nil in v2 mode")
	}

	// Verify v2 components wired into opts.
	if rt.opts.V2Catalog == nil {
		t.Fatal("opts.V2Catalog should be set in v2 mode")
	}
	if rt.opts.V2CommandSvc == nil {
		t.Fatal("opts.V2CommandSvc should be set in v2 mode")
	}
	if rt.opts.V2StateStream == nil {
		t.Fatal("opts.V2StateStream should be set in v2 mode")
	}
}

// TestNewRuntimeLegacyModeConstructsLegacyStores verifies that with
// TERMYARD_V2_STATE unset (default), legacy stores are constructed and v2
// components remain nil.
func TestNewRuntimeLegacyModeConstructsLegacyStores(t *testing.T) {
	// Ensure env var is NOT set.
	t.Setenv("TERMYARD_V2_STATE", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")

	rt, err := newRuntime(&cli.Command{})
	if err != nil {
		t.Fatalf("newRuntime in legacy mode: %v", err)
	}
	defer rt.Stop()

	// In legacy mode, v2 components must be nil.
	if rt.v2Catalog != nil {
		t.Fatal("v2Catalog should be nil in legacy mode")
	}
	if rt.v2CommandSvc != nil {
		t.Fatal("v2CommandSvc should be nil in legacy mode")
	}
	if rt.v2StateStream != nil {
		t.Fatal("v2StateStream should be nil in legacy mode")
	}

	// opts should not have v2 services wired.
	if rt.opts.V2Catalog != nil {
		t.Fatal("opts.V2Catalog should be nil in legacy mode")
	}
	if rt.opts.V2CommandSvc != nil {
		t.Fatal("opts.V2CommandSvc should be nil in legacy mode")
	}
	if rt.opts.V2StateStream != nil {
		t.Fatal("opts.V2StateStream should be nil in legacy mode")
	}

	// Legacy stores MAY be nil if their constructors fail (which is OK),
	// but they should at least be attempted. We just verify the runtime
	// doesn't explode with a nil opts.StateMgr.
	if rt.opts.StateMgr == nil {
		t.Fatal("opts.StateMgr should always be set")
	}
}

// TestNewRuntimeV2InitFailureReturnsFatalError verifies that if v2 store
// initialization fails in v2 mode, newRuntime returns an error (not a fallback).
func TestNewRuntimeV2InitFailureReturnsFatalError(t *testing.T) {
	t.Setenv("TERMYARD_V2_STATE", "1")

	// Create a temporary directory and place a FILE at the location where
	// the v2 state directory should be created. This will cause os.MkdirAll
	// in state.OpenStore to fail.
	tempHome := t.TempDir()
	dataDir := fmt.Sprintf("%s/.local/share/termyard/v2", tempHome)
	// Create all parent dirs
	if err := os.MkdirAll(fmt.Sprintf("%s/.local/share/termyard", tempHome), 0700); err != nil {
		t.Fatalf("setup: failed to create parent dirs: %v", err)
	}
	// Create a FILE where the v2 directory should go, blocking directory creation.
	if err := os.WriteFile(dataDir, []byte("blocking-file"), 0o600); err != nil {
		t.Fatalf("setup: failed to create blocking file: %v", err)
	}

	// Redirect HOME so the v2 state dir calculation will point to our blocking file.
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_DATA_HOME", "") // Ensure we use HOME-based path

	// In v2 mode with a blocking file, newRuntime should fail with a fatal error,
	// not silently continue with legacy mode.
	rt, err := newRuntime(&cli.Command{})

	if err == nil {
		if rt != nil {
			rt.Stop()
		}
		t.Fatal("expected newRuntime to return error in v2 mode when v2 dir cannot be created")
	}

	// Error should mention either "v2" or "state" to be clear it's
	// a v2 initialization failure, not a generic startup problem.
	if !strings.Contains(err.Error(), "v2") && !strings.Contains(err.Error(), "state") {
		t.Logf("Warning: error message may not be clear about v2 failure: %v", err)
	}
}
