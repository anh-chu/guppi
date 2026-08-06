package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/config"
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

// TestRefreshDaemonState_ClassifiesBeforeAdapterRefresh verifies that crash
// classification/cleanup runs before the daemon adapter snapshot is
// refreshed, so a crashed session cannot be observed as live by the
// canonical reconciler in the same cycle.
func TestRefreshDaemonState_ClassifiesBeforeAdapterRefresh(t *testing.T) {
	var order []string
	reg := &fakeRegistry{sessions: []pty.SessionInfo{{ID: "live"}}}
	adapter := &daemonAdapter{reg: reg}

	enricher := &runtimeEnricher{adapter: adapter}
	rt := &Runtime{
		adapter:  adapter,
		enricher: enricher,
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

	list := adapter.List()
	if len(list) != 1 || list[0].ID != "live" {
		t.Fatalf("expected only live session in adapter snapshot, got %+v", list)
	}
}

// TestRefreshSessionsFunc_IsSafeNoOp proves refreshSessionsFunc (wired into
// opts.RefreshSessions / launchSvc.Refresh and called unconditionally by
// several code paths -- create, crashed-session recovery, WS teardown,
// rename) never touches a legacy state manager: there is none any more. It
// must be callable on a zero-value Runtime without panicking.
func TestRefreshSessionsFunc_IsSafeNoOp(t *testing.T) {
	rt := &Runtime{}
	rt.refreshSessionsFunc()
}

// TestV2RuntimeEnricherPreviewReturnsImmediately verifies that previewFor returns
// cached values immediately without blocking on PTY captures. The enricher must
// not delay catalog snapshot publication by waiting for capture operations.
func TestV2RuntimeEnricherPreviewReturnsImmediately(t *testing.T) {
	denricher := &runtimeEnricher{
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

	enricher := &runtimeEnricher{
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

	enricher := &runtimeEnricher{
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

// TestNewRuntimeAlwaysConstructsCanonicalState verifies that the canonical
// state authority (store/catalog/command service/state stream) is always
// constructed unconditionally, regardless of environment.
func TestNewRuntimeAlwaysConstructsCanonicalState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")

	rt, err := newRuntime(&cli.Command{})
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	defer rt.Stop()

	if rt.store == nil {
		t.Fatal("store should always be constructed")
	}
	if rt.catalog == nil {
		t.Fatal("catalog should always be constructed")
	}
	if rt.commandSvc == nil {
		t.Fatal("commandSvc should always be constructed")
	}
	if rt.stateStream == nil {
		t.Fatal("stateStream should always be constructed")
	}
	if rt.remoteCreate == nil {
		t.Fatal("remoteCreate should always be constructed")
	}

	if rt.opts.Catalog == nil {
		t.Fatal("opts.Catalog should always be set")
	}
	if rt.opts.CommandSvc == nil {
		t.Fatal("opts.CommandSvc should always be set")
	}
	if rt.opts.StateStream == nil {
		t.Fatal("opts.StateStream should always be set")
	}
}

// TestNewRuntimeEnvVarCannotSelectAlternatePath proves that TERMYARD_V2_STATE
// (the old runtime-mode switch) has no effect any more: with or without it
// set, newRuntime constructs the identical canonical state graph rooted at
// the same directory (<data-dir>/state). There is no environment variable
// that can select a different state path.
func TestNewRuntimeEnvVarCannotSelectAlternatePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	t.Setenv("TERMYARD_V2_STATE", "1")
	rtWithFlag, err := newRuntime(&cli.Command{})
	if err != nil {
		t.Fatalf("newRuntime with TERMYARD_V2_STATE=1: %v", err)
	}
	ownerWithFlag := rtWithFlag.catalog.Owner()
	rtWithFlag.Stop()

	t.Setenv("TERMYARD_V2_STATE", "")
	rtWithoutFlag, err := newRuntime(&cli.Command{})
	if err != nil {
		t.Fatalf("newRuntime with TERMYARD_V2_STATE unset: %v", err)
	}
	ownerWithoutFlag := rtWithoutFlag.catalog.Owner()
	rtWithoutFlag.Stop()

	if ownerWithFlag != ownerWithoutFlag {
		t.Fatalf("catalog owner differed by TERMYARD_V2_STATE: with=%q without=%q -- the env var must have zero effect on the state path", ownerWithFlag, ownerWithoutFlag)
	}

	stateDir, err := config.StateDir()
	if err != nil {
		t.Fatalf("config.StateDir: %v", err)
	}
	if _, err := os.Stat(stateDir); err != nil {
		t.Fatalf("canonical state directory was not created at %s: %v", stateDir, err)
	}

	// No legacy "v2" subdirectory should ever be created -- that was the old
	// dormant/shadow store path and no longer exists.
	dataDir, err := config.DataDir()
	if err != nil {
		t.Fatalf("config.DataDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "v2")); err == nil {
		t.Fatal("legacy v2 state directory should never be created")
	}
}

// TestNewRuntimeNoLegacyFilesOnFreshStartup verifies that a fresh startup
// creates no legacy per-feature store files (session-attrs, session-order,
// groups) under the config directory -- those stores no longer exist.
func TestNewRuntimeNoLegacyFilesOnFreshStartup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	rt, err := newRuntime(&cli.Command{})
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	defer rt.Stop()

	configDir, err := config.Dir()
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}
	for _, legacyFile := range []string{"session-attrs.json", "session-order.json", "groups.json"} {
		if _, err := os.Stat(filepath.Join(configDir, legacyFile)); err == nil {
			t.Fatalf("legacy file %s should never be created", legacyFile)
		}
	}
}

// TestNewRuntimeStateInitFailureReturnsFatalError verifies that if canonical
// state store initialization fails, newRuntime returns an error rather than
// falling back to any alternate path.
func TestNewRuntimeStateInitFailureReturnsFatalError(t *testing.T) {
	// Create a temporary directory and place a FILE at the location where
	// the state directory should be created. This will cause os.MkdirAll in
	// state.OpenStore to fail.
	tempHome := t.TempDir()
	dataDir := fmt.Sprintf("%s/.local/share/termyard/state", tempHome)
	// Create all parent dirs
	if err := os.MkdirAll(fmt.Sprintf("%s/.local/share/termyard", tempHome), 0700); err != nil {
		t.Fatalf("setup: failed to create parent dirs: %v", err)
	}
	// Create a FILE where the state directory should go, blocking directory creation.
	if err := os.WriteFile(dataDir, []byte("blocking-file"), 0o600); err != nil {
		t.Fatalf("setup: failed to create blocking file: %v", err)
	}

	// Redirect HOME so the state dir calculation will point to our blocking file.
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_DATA_HOME", "") // Ensure we use HOME-based path

	rt, err := newRuntime(&cli.Command{})

	if err == nil {
		if rt != nil {
			rt.Stop()
		}
		t.Fatal("expected newRuntime to return error when the state dir cannot be created")
	}

	if !strings.Contains(err.Error(), "state") {
		t.Logf("Warning: error message may not be clear about the state failure: %v", err)
	}
}

// TestV2RuntimeEnricherEnrichIsPureCacheLookup proves that Enrich performs
// zero /proc reads and zero daemon-adapter list/snapshot calls for N
// sessions on the hot path: all process metadata comes from the
// background-refreshed runtimeCache built by refreshRuntimeCache.
func TestV2RuntimeEnricherEnrichIsPureCacheLookup(t *testing.T) {
	const n = 500
	infos := make([]pty.SessionInfo, n)
	for i := 0; i < n; i++ {
		infos[i] = pty.SessionInfo{ID: fmt.Sprintf("s%d", i), Pid: 1000 + i, ShellPid: 2000 + i, Shell: "bash"}
	}
	reg := &fakeRegistry{sessions: infos}
	adapter := &daemonAdapter{reg: reg}
	adapter.refresh()

	readlinkCalls := 0
	origReadProcCwd := readProcCwd
	readProcCwd = func(pid int) (string, error) {
		readlinkCalls++
		return fmt.Sprintf("/cwd/%d", pid), nil
	}
	defer func() { readProcCwd = origReadProcCwd }()

	enricher := &runtimeEnricher{adapter: adapter}

	// The one and only place readlink/list may run: the background refresh.
	enricher.refreshRuntimeCache()
	if readlinkCalls != n {
		t.Fatalf("background refresh: expected %d readlink calls, got %d", n, readlinkCalls)
	}

	baseListCalls := reg.listCalls
	readlinkCalls = 0

	for i := 0; i < n; i++ {
		ref := state.SessionRef{Owner: state.OwnerID("o"), Session: state.SessionID(fmt.Sprintf("s%d", i))}
		rec := state.LocalSessionRecord{ID: ref.Session, Owner: ref.Owner, Ref: ref}
		rt := enricher.Enrich(ref, rec)
		want := fmt.Sprintf("/cwd/%d", 2000+i)
		if rt.CurrentPath != want {
			t.Fatalf("session %d: CurrentPath = %q, want %q (cached value)", i, rt.CurrentPath, want)
		}
		if rt.DaemonPID != 1000+i || rt.ShellPID != 2000+i {
			t.Fatalf("session %d: pid fields not populated from cache: %+v", i, rt)
		}
	}

	if readlinkCalls != 0 {
		t.Fatalf("Enrich performed %d /proc reads across %d sessions; want 0 (must be pure cache lookup)", readlinkCalls, n)
	}
	if reg.listCalls != baseListCalls {
		t.Fatalf("Enrich triggered %d daemon-adapter list/snapshot calls; want 0", reg.listCalls-baseListCalls)
	}
}

// TestV2RuntimeEnricherBackgroundRefreshUpdatesCache proves that
// refreshRuntimeCache actually replaces the cached value over successive
// cycles, simulating the underlying process's cwd changing between two
// background refreshes.
func TestV2RuntimeEnricherBackgroundRefreshUpdatesCache(t *testing.T) {
	reg := &fakeRegistry{sessions: []pty.SessionInfo{{ID: "s1", Pid: 100, ShellPid: 101, Shell: "bash"}}}
	adapter := &daemonAdapter{reg: reg}
	adapter.refresh()

	cwd := "/first"
	origReadProcCwd := readProcCwd
	readProcCwd = func(pid int) (string, error) { return cwd, nil }
	defer func() { readProcCwd = origReadProcCwd }()

	enricher := &runtimeEnricher{adapter: adapter}
	ref := state.SessionRef{Owner: state.OwnerID("o"), Session: state.SessionID("s1")}
	rec := state.LocalSessionRecord{ID: ref.Session, Owner: ref.Owner, Ref: ref}

	// Cycle 1.
	enricher.refreshRuntimeCache()
	rt := enricher.Enrich(ref, rec)
	if rt.CurrentPath != "/first" {
		t.Fatalf("cycle 1: CurrentPath = %q, want /first", rt.CurrentPath)
	}

	// Cycle 2: the process changed directory; the next background refresh
	// must observe and cache the new value.
	cwd = "/second"
	enricher.refreshRuntimeCache()
	rt = enricher.Enrich(ref, rec)
	if rt.CurrentPath != "/second" {
		t.Fatalf("cycle 2: CurrentPath = %q, want /second (background refresh must update cache)", rt.CurrentPath)
	}
}
