package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/pty"
)

type testBackend struct {
	mu             sync.Mutex
	startCalls     []pty.StartRequest
	startResult    pty.ReadyInfo
	startErr       error
	liveGen        map[string]string
	terminated     map[string]bool
	terminateCalls []pty.StableBinding
	terminateOut   pty.TerminateOutcome

	// See Start() for how these are used to force deterministic concurrent
	// overlap in tests.
	startEnteredCh chan struct{}
	startReleaseCh chan struct{}
}

func newTestBackend() *testBackend {
	return &testBackend{
		startResult:  pty.ReadyInfo{DaemonPID: 42, ShellPID: 43, Generation: "gen-test"},
		terminateOut: pty.TerminateAcknowledged,
		liveGen:      make(map[string]string),
		terminated:   make(map[string]bool),
	}
}

// startEnteredCh and startReleaseCh let a test deterministically force two
// concurrent Start() calls to overlap without relying on sleeps: if set,
// every Start() call signals entry on startEnteredCh (before recording
// itself in startCalls) and then blocks on startReleaseCh until the test
// releases it. This proves genuine concurrent overlap rather than a timing
// coincidence.
func (b *testBackend) Start(ctx context.Context, req pty.StartRequest) (pty.ReadyInfo, error) {
	b.mu.Lock()
	entered := b.startEnteredCh
	release := b.startReleaseCh
	b.mu.Unlock()

	if entered != nil {
		entered <- struct{}{}
	}
	if release != nil {
		<-release
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.startCalls = append(b.startCalls, req)
	if b.startErr != nil {
		return pty.ReadyInfo{}, b.startErr
	}
	info := b.startResult
	if info.Generation == "" {
		info.Generation = "gen-" + string(req.SessionID)
	}
	if info.DaemonPID == 0 {
		info.DaemonPID = 1001 + len(b.startCalls)
	}
	b.liveGen[req.SessionID] = info.Generation
	b.terminated[req.SessionID] = false
	return info, nil
}

func (b *testBackend) Probe(binding pty.StableBinding) pty.ProbeEvidence {
	b.mu.Lock()
	defer b.mu.Unlock()
	if gen, ok := b.liveGen[binding.SessionID]; ok && !b.terminated[binding.SessionID] {
		return pty.ProbeEvidence{
			Status:    pty.ProbeLive,
			Binding:   pty.StableBinding{Owner: binding.Owner, SessionID: binding.SessionID, Generation: gen, DaemonKey: binding.DaemonKey},
			DaemonPID: 1,
			Reason:    "test live",
		}
	}
	return pty.ProbeEvidence{Status: pty.ProbeUnknown, Binding: binding}
}

func (b *testBackend) Terminate(ctx context.Context, binding pty.StableBinding) pty.TerminateOutcome {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.terminateCalls = append(b.terminateCalls, binding)
	out := b.terminateOut
	if out == pty.TerminateAcknowledged {
		b.terminated[binding.SessionID] = true
	}
	return out
}

func (b *testBackend) startCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.startCalls)
}

func (b *testBackend) terminateCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.terminateCalls)
}

func newTestCommandService(t *testing.T) (*SessionCommandService, *Catalog, *testBackend, *Store, func()) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	catalog := NewCatalog(owner, store)
	if err := catalog.Load(); err != nil {
		t.Fatal(err)
	}
	backend := newTestBackend()
	svc := NewSessionCommandService(catalog, backend, nil, SessionCommandServiceOptions{
		Tick:             50 * time.Millisecond,
		RetryInitial:     10 * time.Millisecond,
		MaxCreateRetries: 2,
	})
	return svc, catalog, backend, store, cleanup
}

func startWorker(ctx context.Context, t *testing.T, svc *SessionCommandService) {
	t.Helper()
	go func() {
		_ = svc.Run(ctx)
	}()
}

func createCmd(name string) SessionCommand {
	params, _ := json.Marshal(CreateParams{
		Name:  name,
		Shell: "/bin/bash",
		Cwd:   "/tmp",
		Cols:  80,
		Rows:  24,
	})
	return SessionCommand{
		ID:     NewCommandID(),
		Action: ActionCreate,
		Params: params,
	}
}

func TestCreateReturnsStableIDAndActivates(t *testing.T) {
	svc, catalog, backend, _, cleanup := newTestCommandService(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startWorker(ctx, t, svc)

	cmd := createCmd("alpha")
	res, err := svc.ExecuteSessionCommand(ctx, cmd)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if !res.Accepted || res.ID != cmd.ID {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Ref.Session == "" {
		t.Fatal("expected stable session ref")
	}

	// Before the worker runs, only the pending intent and layout exist.
	if p := catalog.PendingCreates(); len(p) != 1 {
		t.Fatalf("expected 1 pending create, got %d", len(p))
	}
	layouts := catalog.Layouts()
	if len(layouts) != 1 {
		t.Fatalf("expected 1 layout, got %d", len(layouts))
	}

	// Wait for the worker to start the daemon and commit the running record.
	for start := time.Now(); time.Since(start) < 5*time.Second; {
		if r, ok := catalog.Session(res.Ref.Session); ok && r.Phase == SessionPhaseActive {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	rec, ok := catalog.Session(res.Ref.Session)
	if !ok || rec.Phase != SessionPhaseActive {
		t.Fatalf("expected active session: %+v", rec)
	}
	if rec.Generation == "" {
		t.Fatal("expected generation set")
	}
	if backend.startCount() != 1 {
		t.Fatalf("expected one daemon start, got %d", backend.startCount())
	}
	if catalog.PendingCreates(); len(catalog.PendingCreates()) != 0 {
		t.Fatalf("pending create should be removed")
	}
}

func TestCreateDurableBeforeAsyncWork(t *testing.T) {
	svc, catalog, _, store, cleanup := newTestCommandService(t)
	defer cleanup()

	var panicked bool
	svc.opts.CrashHook = func(p CrashPoint) {
		if p == CrashAfterIntentCommit && !panicked {
			panicked = true
			panic("crash after intent commit")
		}
	}

	cmd := createCmd("beta")
	func() {
		defer func() {
			if r := recover(); r != nil {
				// expected
			}
		}()
		_, _ = svc.ExecuteSessionCommand(context.Background(), cmd)
	}()

	pendingList := catalog.PendingCreates()
	if len(pendingList) != 1 {
		t.Fatalf("expected pending create to survive crash, got %d", len(pendingList))
	}

	// Simulate restart by opening a new catalog/service on the same store.
	fresh := NewCatalog(store.Owner(), store)
	_ = fresh.Load()
	recovered := NewSessionCommandService(fresh, newTestBackend(), nil, SessionCommandServiceOptions{Tick: 50 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startWorker(ctx, t, recovered)

	for start := time.Now(); time.Since(start) < 5*time.Second; {
		if _, ok := fresh.Session(pendingList[0].Ref.Session); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := fresh.Session(pendingList[0].Ref.Session); !ok {
		t.Fatal("expected recovered create to finish")
	}
}

func TestDuplicateCommandIDIsIdempotent(t *testing.T) {
	svc, catalog, backend, _, cleanup := newTestCommandService(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startWorker(ctx, t, svc)

	cmd := createCmd("gamma")
	res1, err := svc.ExecuteSessionCommand(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := svc.ExecuteSessionCommand(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if res1.Ref.Session != res2.Ref.Session {
		t.Fatalf("idempotency produced different refs: %v vs %v", res1.Ref.Session, res2.Ref.Session)
	}
	// Wait for the worker to start the daemon and commit the running record.
	for start := time.Now(); time.Since(start) < 5*time.Second; {
		if r, ok := catalog.Session(res1.Ref.Session); ok && r.Phase == SessionPhaseActive {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec, ok := catalog.Session(res1.Ref.Session)
	if !ok || rec.Phase != SessionPhaseActive {
		t.Fatalf("expected active session: %+v", rec)
	}
	if backend.startCount() != 1 {
		t.Fatalf("expected one daemon start, got %d", backend.startCount())
	}
	if len(catalog.Sessions()) != 1 {
		t.Fatalf("expected one session, got %d", len(catalog.Sessions()))
	}
}

func TestConcurrentCreateSameRefProducesOneSession(t *testing.T) {
	svc, catalog, backend, _, cleanup := newTestCommandService(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startWorker(ctx, t, svc)

	ref := SessionRef{Owner: testOwner(), Session: NewSessionID(), Window: 0, Pane: 0}
	cmdID := NewCommandID()
	params, _ := json.Marshal(CreateParams{Name: "delta", Shell: "/bin/bash", Cwd: "/tmp"})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.ExecuteSessionCommand(ctx, SessionCommand{ID: cmdID, Ref: ref, Action: ActionCreate, Params: params})
		}()
	}
	wg.Wait()

	if sessions := catalog.Sessions(); len(sessions)+len(catalog.PendingCreates()) != 1 {
		t.Fatalf("expected one session or pending, got sessions=%d pending=%d", len(sessions), len(catalog.PendingCreates()))
	}
	for start := time.Now(); time.Since(start) < 5*time.Second; {
		if backend.startCount() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if backend.startCount() != 1 {
		t.Fatalf("expected one daemon start across concurrent creates, got %d", backend.startCount())
	}
}

func TestCreateWorktreeCleanedOnPermanentFailure(t *testing.T) {
	repo := initGitRepo(t)
	t.Setenv("HOME", t.TempDir())

	svc, catalog, backend, _, cleanup := newTestCommandService(t)
	defer cleanup()

	backend.startErr = errors.New("spawn failed")
	svc.opts.MaxCreateRetries = 1

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startWorker(ctx, t, svc)

	cmdID := NewCommandID()
	params, _ := json.Marshal(CreateParams{
		Name:           "feat",
		Shell:          "/bin/bash",
		Cwd:            repo,
		WorktreeBranch: "feature",
		Cols:           80,
		Rows:           24,
	})
	res, err := svc.ExecuteSessionCommand(ctx, SessionCommand{ID: cmdID, Action: ActionCreate, Params: params})
	if err != nil {
		t.Fatal(err)
	}

	worktreePath := filepath.Join(repo, ".worktrees", "feature")
	if _, err := os.Stat(worktreePath); err == nil {
		t.Fatal("worktree should not exist before worker prepares it")
	}

	// Wait for worker to exhaust retries.
	for start := time.Now(); time.Since(start) < 3*time.Second; {
		if rec, ok := catalog.Session(res.Ref.Session); ok && rec.Phase == SessionPhaseDismissed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	rec, ok := catalog.Session(res.Ref.Session)
	if !ok || rec.Phase != SessionPhaseDismissed {
		t.Fatalf("expected dismissed record after retries exhausted: %+v", rec)
	}
	// The directory removal happens after the catalog write; give it a moment.
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("failed-create worktree should be removed: %v", err)
	}
}

func TestKillPersistsStopIntentAndTerminates(t *testing.T) {
	svc, catalog, backend, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("killme"), "gen-kill")
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}

	res, err := svc.ExecuteSessionCommand(context.Background(), SessionCommand{
		ID:     NewCommandID(),
		Ref:    rec.Ref,
		Action: ActionKill,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatal("expected kill accepted")
	}

	got, _ := catalog.Session(rec.ID)
	if got.Desired != DesiredStop {
		t.Fatalf("expected DesiredStop, got %q", got.Desired)
	}
	if backend.terminateCount() != 1 {
		t.Fatalf("expected one terminate call, got %d", backend.terminateCount())
	}
	if backend.terminateCalls[0].Generation != "gen-kill" {
		t.Fatalf("expected exact-generation termination, got %q", backend.terminateCalls[0].Generation)
	}
}

func TestKillDoesNotRemoveLiveUnconfirmedGeneration(t *testing.T) {
	svc, catalog, backend, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("liveme"), "gen-live")
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}
	backend.terminateOut = pty.TerminateGenerationMismatch

	_, err := svc.ExecuteSessionCommand(context.Background(), SessionCommand{ID: NewCommandID(), Ref: rec.Ref, Action: ActionKill})
	if err != nil {
		t.Fatal(err)
	}

	got, ok := catalog.Session(rec.ID)
	if !ok || got.Phase != SessionPhaseActive {
		t.Fatalf("live unconfirmed generation should not be deleted: %+v", got)
	}
	if got.Desired != DesiredStop {
		t.Fatalf("expected DesiredStop intent persisted")
	}
}

func TestExecuteSessionCommandFromPeer_KillOwnershipMismatchRejected(t *testing.T) {
	svc, catalog, backend, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("liveme"), "gen-live")
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}

	// Peer B forges Ref.Owner while pointing at a session ID that is genuinely
	// owned locally (owner == testOwner()). Because ExecuteSessionCommand
	// always mutates this node's own catalog, the forged owner must be
	// rejected before the kill is even attempted.
	forgedRef := rec.Ref
	forgedRef.Owner = OwnerID("peer-b")
	_, err := svc.ExecuteSessionCommandFromPeer(context.Background(), SessionCommand{
		ID:     NewCommandID(),
		Ref:    forgedRef,
		Action: ActionKill,
	}, "peer-b")

	var se StateError
	if !errors.As(err, &se) || se.Code != ErrOwnershipMismatch {
		t.Fatalf("expected ownership_mismatch error, got %v", err)
	}
	if backend.terminateCount() != 0 {
		t.Fatal("kill must not be attempted when ownership check fails")
	}
	got, ok := catalog.Session(rec.ID)
	if !ok || got.Phase != SessionPhaseActive || got.Desired != DesiredRun {
		t.Fatalf("session must be unchanged after rejected kill: %+v", got)
	}

	// The same command, with the correct owner, is accepted.
	_, err = svc.ExecuteSessionCommandFromPeer(context.Background(), SessionCommand{
		ID:     NewCommandID(),
		Ref:    rec.Ref,
		Action: ActionKill,
	}, "peer-b")
	if err != nil {
		t.Fatalf("expected correctly-owned kill to succeed, got: %v", err)
	}

	// A local (non-peer) caller using the un-gated entrypoint is unaffected by
	// this check even with a forged owner, proving local paths stay trusted.
	rec2 := activeRecord(SessionID("liveme2"), "gen-live2")
	if err := catalog.PutSession(rec2); err != nil {
		t.Fatal(err)
	}
	forgedRef2 := rec2.Ref
	forgedRef2.Owner = OwnerID("peer-c")
	if _, err := svc.ExecuteSessionCommand(context.Background(), SessionCommand{
		ID:     NewCommandID(),
		Ref:    forgedRef2,
		Action: ActionKill,
	}); err != nil {
		t.Fatalf("local caller must not be affected by the peer ownership check: %v", err)
	}
}

func TestExecuteSessionCommandFromPeer_LabelOwnershipMismatchRejected(t *testing.T) {
	svc, catalog, _, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("labelme"), "gen-label")
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}

	forgedRef := rec.Ref
	forgedRef.Owner = OwnerID("peer-b")
	params, _ := json.Marshal(LabelParams{Label: "renamed"})
	_, err := svc.ExecuteSessionCommandFromPeer(context.Background(), SessionCommand{
		ID:     NewCommandID(),
		Ref:    forgedRef,
		Action: ActionLabel,
		Params: params,
	}, "peer-b")

	var se StateError
	if !errors.As(err, &se) || se.Code != ErrOwnershipMismatch {
		t.Fatalf("expected ownership_mismatch error, got %v", err)
	}
	got, _ := catalog.Session(rec.ID)
	if got.Name == "renamed" {
		t.Fatal("label must not be applied when ownership check fails")
	}
}

func TestLabelOnlyMutatesDisplayName(t *testing.T) {
	svc, catalog, _, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("labelme"), "gen-label")
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}
	origRef := rec.Ref

	params, _ := json.Marshal(LabelParams{Label: "renamed"})
	_, err := svc.ExecuteSessionCommand(context.Background(), SessionCommand{ID: NewCommandID(), Ref: origRef, Action: ActionLabel, Params: params})
	if err != nil {
		t.Fatal(err)
	}

	got, _ := catalog.Session(rec.ID)
	if got.Name != "renamed" {
		t.Fatalf("expected display name changed, got %q", got.Name)
	}
	if got.Ref != origRef {
		t.Fatalf("label changed session ref: %v -> %v", origRef, got.Ref)
	}
	layouts := catalog.Layouts()
	if len(layouts) != 0 && !findLeaf(layouts[0].Tree, origRef) {
		t.Fatal("label removed session ref from layout")
	}
}

func TestRecoverCreatesNewGeneration(t *testing.T) {
	svc, catalog, backend, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("crashme"), "gen-old")
	rec.Phase = SessionPhaseCrashed
	rec.Desired = DesiredStop
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}

	backend.startResult = pty.ReadyInfo{DaemonPID: 200, ShellPID: 201, Generation: "gen-new"}
	_, err := svc.ExecuteSessionCommand(context.Background(), SessionCommand{ID: NewCommandID(), Ref: rec.Ref, Action: ActionRecover})
	if err != nil {
		t.Fatal(err)
	}

	got, _ := catalog.Session(rec.ID)
	if got.Phase != SessionPhaseActive || got.Generation != "gen-new" {
		t.Fatalf("expected active with new generation, got %+v", got)
	}
	if got.Ref != rec.Ref {
		t.Fatal("recover must preserve logical identity/ref")
	}
}

func TestNaturalExitCleansSessionAndWorkspace(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()
	catalog := NewCatalog(owner, store)
	_ = catalog.Load()

	rec := activeRecord(SessionID("exitme"), "gen-exit")
	rec.Desired = DesiredStop
	_ = catalog.PutSession(rec)
	layout := LayoutRecord{ID: NewLayoutID(), Owner: owner, Order: 1, Revision: 1, Tree: Leaf(rec.Ref)}
	_ = catalog.PutLayout(layout)

	backend := newFakeBackend()
	backend.setProbe(bindingForRecord(&rec), pty.ProbeEvidence{
		Status: pty.ProbeClean,
		Binding: pty.StableBinding{
			Owner:      string(owner),
			SessionID:  string(rec.ID),
			Generation: "gen-exit",
		},
	})

	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, ok := catalog.Session(rec.ID); ok {
		t.Fatal("expected clean session removed")
	}
	if len(catalog.Layouts()) != 0 {
		t.Fatalf("expected empty layout removed after clean exit, got %d", len(catalog.Layouts()))
	}
}

func TestCrashAfterDaemonReadyResumesWithoutDuplicate(t *testing.T) {
	svc, catalog, backend, store, cleanup := newTestCommandService(t)
	defer cleanup()

	cmd := createCmd("resume")
	_, err := svc.ExecuteSessionCommand(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	pending := catalog.PendingCreates()[0]

	var panicked bool
	svc.opts.CrashHook = func(p CrashPoint) {
		if p == CrashAfterDaemonReady && !panicked {
			panicked = true
			panic("crash after daemon ready")
		}
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				// expected
			}
		}()
		svc.executePendingCreate(context.Background(), pending)
	}()

	if backend.startCount() != 1 {
		t.Fatalf("expected one start attempt before crash, got %d", backend.startCount())
	}

	fresh := NewCatalog(store.Owner(), store)
	_ = fresh.Load()
	recovered := NewSessionCommandService(fresh, backend, nil, SessionCommandServiceOptions{Tick: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startWorker(ctx, t, recovered)

	for start := time.Now(); time.Since(start) < 5*time.Second; {
		if r, ok := fresh.Session(pending.Ref.Session); ok && r.Phase == SessionPhaseActive {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if backend.startCount() != 1 {
		t.Fatalf("resume should not restart a ready daemon, got %d starts", backend.startCount())
	}
}

func TestSchedulerCommandIDIsStable(t *testing.T) {
	owner := OwnerID("ownertest1234567890")
	now := time.Unix(1_700_000_000, 0)
	id1 := NewCommandIDFromSchedule(owner, "sched-1", now)
	id2 := NewCommandIDFromSchedule(owner, "sched-1", now)
	id3 := NewCommandIDFromSchedule(owner, "sched-2", now)
	if id1 != id2 {
		t.Fatalf("same schedule execution produced different command ids: %q vs %q", id1, id2)
	}
	if id1 == id3 {
		t.Fatalf("different schedules produced same command id")
	}
	if err := id1.Validate(); err != nil {
		t.Fatalf("stable command id invalid: %v", err)
	}
}

// TestReplaySameCommandIDReturnsOwnSessionNotAnother reproduces the historical
// bug where replaying a create command ID looked up its result by scanning
// ALL current sessions for ANY command whose ID matched cmd.ID, returning the
// first session iterated (in sorted-ID order) rather than the session that
// command actually created. With multiple sequential creates in flight, a
// replay of an early command ID could return a LATER command's session.
func TestReplaySameCommandIDReturnsOwnSessionNotAnother(t *testing.T) {
	svc, catalog, _, _, cleanup := newTestCommandService(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startWorker(ctx, t, svc)

	const n = 6
	cmds := make([]SessionCommand, n)
	results := make([]CommandResult, n)
	for i := 0; i < n; i++ {
		cmds[i] = createCmd(fmt.Sprintf("multi-%d", i))
		res, err := svc.ExecuteSessionCommand(ctx, cmds[i])
		if err != nil {
			t.Fatalf("create %d failed: %v", i, err)
		}
		results[i] = res
	}

	// Wait for every create to be promoted from a pending intent to an active
	// session record -- the bug only manifests once all commands' outcomes
	// are readable purely from doc.Sessions (the buggy code's PendingCreates
	// branch matched correctly by IntentID; only the doc.Sessions scan was
	// broken).
	for start := time.Now(); time.Since(start) < 5*time.Second; {
		if len(catalog.PendingCreates()) == 0 && len(catalog.Sessions()) == n {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(catalog.Sessions()); got != n {
		t.Fatalf("expected %d active sessions, got %d", n, got)
	}

	// Replay every command ID (in an order different from creation order) and
	// confirm each one returns EXACTLY its own session, never a different one
	// picked up by scanning current catalog state in some other order.
	for i := n - 1; i >= 0; i-- {
		replay, err := svc.ExecuteSessionCommand(ctx, cmds[i])
		if err != nil {
			t.Fatalf("replay %d failed: %v", i, err)
		}
		if replay.Ref.Session != results[i].Ref.Session {
			t.Fatalf("replay of command %d returned wrong session: got %q, want %q", i, replay.Ref.Session, results[i].Ref.Session)
		}
	}
}

// TestKillIdempotentReplay proves a retried kill (same command ID) returns
// the original result and does not re-issue termination a second time.
func TestKillIdempotentReplay(t *testing.T) {
	svc, catalog, backend, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("killreplay"), "gen-kr")
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}

	cmd := SessionCommand{ID: NewCommandID(), Ref: rec.Ref, Action: ActionKill}
	res1, err := svc.ExecuteSessionCommand(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := svc.ExecuteSessionCommand(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if res1 != res2 {
		t.Fatalf("replayed kill returned different result: %+v vs %+v", res1, res2)
	}
	if backend.terminateCount() != 1 {
		t.Fatalf("expected exactly one terminate call across original + replay, got %d", backend.terminateCount())
	}
}

// TestLabelIdempotentReplay proves a retried label (same command ID) returns
// the original result rather than erroring or re-deriving from current state.
func TestLabelIdempotentReplay(t *testing.T) {
	svc, catalog, _, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("labelreplay"), "gen-lr")
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(LabelParams{Label: "renamed"})
	cmd := SessionCommand{ID: NewCommandID(), Ref: rec.Ref, Action: ActionLabel, Params: params}
	res1, err := svc.ExecuteSessionCommand(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := svc.ExecuteSessionCommand(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if res1 != res2 {
		t.Fatalf("replayed label returned different result: %+v vs %+v", res1, res2)
	}
	if res1.DisplayName != "renamed" {
		t.Fatalf("expected label applied, got %q", res1.DisplayName)
	}
}

// TestRecoverIdempotentReplay proves a retried recover (same command ID)
// returns the original result and does not spawn a second daemon.
func TestRecoverIdempotentReplay(t *testing.T) {
	svc, catalog, backend, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("recoverreplay"), "gen-rr")
	rec.Phase = SessionPhaseCrashed
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}

	cmd := SessionCommand{ID: NewCommandID(), Ref: rec.Ref, Action: ActionRecover}
	res1, err := svc.ExecuteSessionCommand(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := svc.ExecuteSessionCommand(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if res1 != res2 {
		t.Fatalf("replayed recover returned different result: %+v vs %+v", res1, res2)
	}
	if backend.startCount() != 1 {
		t.Fatalf("expected exactly one daemon start across original + replay, got %d", backend.startCount())
	}
}

// TestCreateWithSplitTargetPlacesAtomicallyWithoutDuplicateLeaf proves that a
// create carrying Target/Direction places the new session by splitting the
// target leaf as part of the SAME create command, and that the layout ends
// up with exactly the requested split -- never a duplicate-leaf rejection
// from a separate follow-up placement.
func TestCreateWithSplitTargetPlacesAtomicallyWithoutDuplicateLeaf(t *testing.T) {
	svc, catalog, _, _, cleanup := newTestCommandService(t)
	defer cleanup()

	// Seed an existing layout with one session to split beside.
	existing := activeRecord(SessionID("existingpane"), "gen-ex")
	if err := catalog.PutSession(existing); err != nil {
		t.Fatal(err)
	}
	layoutID := LayoutID("splitlayout1234567")
	if err := catalog.PutLayout(LayoutRecord{
		ID:    layoutID,
		Owner: testOwner(),
		Order: 1,
		Tree:  Leaf(existing.Ref),
	}); err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(CreateParams{
		Name:      "splitcreated",
		Shell:     "/bin/bash",
		Cwd:       "/tmp",
		LayoutID:  layoutID,
		Target:    &existing.Ref,
		Direction: DirectionVertical,
	})
	res, err := svc.ExecuteSessionCommand(context.Background(), SessionCommand{ID: NewCommandID(), Action: ActionCreate, Params: params})
	if err != nil {
		t.Fatalf("create with split target failed: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("expected accepted result: %+v", res)
	}

	layout, ok := catalog.Layout(layoutID)
	if !ok {
		t.Fatal("expected layout to still exist")
	}
	if !layout.Tree.IsSplit() {
		t.Fatalf("expected a split tree after atomic create+split placement, got %+v", layout.Tree)
	}
	if layout.Tree.Direction != DirectionVertical {
		t.Fatalf("expected vertical split direction, got %q", layout.Tree.Direction)
	}
	leaves := leafs(layout.Tree)
	if len(leaves) != 2 {
		t.Fatalf("expected exactly 2 leaves (existing + new), got %d: %v", len(leaves), leaves)
	}
	wantExisting := existing.Ref.MapKey()
	wantNew := res.Ref.MapKey()
	found := map[string]bool{}
	for _, l := range leaves {
		found[l] = true
	}
	if !found[wantExisting] || !found[wantNew] {
		t.Fatalf("expected leaves %q and %q, got %v", wantExisting, wantNew, leaves)
	}

	// Confirm ValidateDocument (specifically the duplicate-leaf check) never
	// tripped -- i.e. a real separate follow-up split command targeting the
	// same new ref WOULD now be rejected as a duplicate, proving create truly
	// already placed it (this documents why the frontend must not send a
	// separate split after this kind of create).
	if findLeaf(layout.Tree, res.Ref) == false {
		t.Fatalf("expected new ref to already be a leaf in the layout")
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2024-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2024-01-01T00:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", args, out)
		}
	}
	return dir
}

// TestConcurrentDuplicateRecoverStartsBackendOnlyOnce is a regression proof
// that two truly concurrent ExecuteSessionCommand calls carrying the SAME
// command ID for a non-idempotent side effect (recover -> backend.Start,
// which spawns a new daemon process/generation) execute that side effect
// exactly once, with the second caller waiting for and reusing the first
// caller's exact result rather than independently invoking Start.
//
// Before the fix, executeRecover's own comment on commitSessionReceipt
// acknowledged this precisely: "It does NOT undo an external side effect...
// that may have already run for both racers... this race only matters for
// kill/label/recover/dismiss, whose side effects are themselves safe to
// invoke more than once." That reasoning was wrong for recover: calling
// backend.Start twice for one logical recover leaks an orphaned daemon
// process that no catalog record ever references (only one of the two
// Start results can win the PutSession/commitSessionReceipt race).
//
// Deterministic proof of genuine overlap (no sleeps): goroutine 1 is
// launched first and blocked *inside* backend.Start via startReleaseCh;
// only once that entry is confirmed is goroutine 2 launched with the same
// command ID, so goroutine 2's call unambiguously overlaps goroutine 1's
// still-in-flight command. With the runSingleFlight fix, goroutine 2 never
// enters backend.Start at all -- it waits for goroutine 1 to finish and
// reuses its result -- so backend.Start is called exactly once.
func TestConcurrentDuplicateRecoverStartsBackendOnlyOnce(t *testing.T) {
	svc, catalog, backend, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("crashconcurrent1234"), "gen-old")
	rec.Phase = SessionPhaseCrashed
	rec.Desired = DesiredStop
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}

	backend.mu.Lock()
	backend.startEnteredCh = make(chan struct{}, 2)
	backend.startReleaseCh = make(chan struct{})
	backend.mu.Unlock()

	cmd := SessionCommand{ID: NewCommandID(), Ref: rec.Ref, Action: ActionRecover}

	type outcome struct {
		result CommandResult
		err    error
	}
	results := make(chan outcome, 2)

	// Goroutine 1: enters backend.Start and blocks there until released.
	go func() {
		res, err := svc.ExecuteSessionCommand(context.Background(), cmd)
		results <- outcome{res, err}
	}()

	select {
	case <-backend.startEnteredCh:
		// Confirmed: goroutine 1 is now genuinely inside backend.Start,
		// blocked on startReleaseCh.
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first recover to enter backend.Start")
	}

	// Goroutine 2: same command ID, launched while goroutine 1's command is
	// still provably in-flight inside backend.Start.
	go func() {
		res, err := svc.ExecuteSessionCommand(context.Background(), cmd)
		results <- outcome{res, err}
	}()

	// Goroutine 2 must NOT independently enter backend.Start (that would be
	// exactly the bug this test guards against): give it a bounded window to
	// prove it doesn't, then release goroutine 1 to let both complete.
	select {
	case <-backend.startEnteredCh:
		t.Fatal("second concurrent recover with the same command ID entered backend.Start independently " +
			"instead of waiting for the first in-flight execution -- this is the leaked-daemon-process bug")
	case <-time.After(150 * time.Millisecond):
		// Expected: goroutine 2 is blocked waiting on the in-flight entry,
		// not inside Start.
	}

	close(backend.startReleaseCh)

	var o1, o2 outcome
	select {
	case o1 = <-results:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first recover to complete")
	}
	select {
	case o2 = <-results:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second recover to complete")
	}

	if o1.err != nil {
		t.Fatalf("first recover returned error: %v", o1.err)
	}
	if o2.err != nil {
		t.Fatalf("second recover returned error: %v", o2.err)
	}
	if o1.result != o2.result {
		t.Fatalf("two racing identical-ID recovers returned different results: %+v vs %+v", o1.result, o2.result)
	}

	if got := backend.startCount(); got != 1 {
		t.Fatalf("LEAKED DAEMON PROCESS: backend.Start was called %d times for two concurrent recovers sharing "+
			"the same command ID, want exactly 1 -- the losing racer's daemon process/generation is now orphaned "+
			"with no catalog record pointing at it", got)
	}
}

// TestSetPresentationUpdatesHiddenAndBackground proves ActionSetPresentation
// is an ordinary durable, receipt-backed session command (same shape as
// ActionLabel): it mutates only the Hidden/Background fields of the target
// session record and leaves everything else (including a field left nil in
// the request) untouched.
func TestSetPresentationUpdatesHiddenAndBackground(t *testing.T) {
	svc, catalog, _, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("presentme"), "gen-pr")
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}

	hidden := true
	params, _ := json.Marshal(PresentationParams{Hidden: &hidden})
	res, err := svc.ExecuteSessionCommand(context.Background(), SessionCommand{
		ID: NewCommandID(), Ref: rec.Ref, Action: ActionSetPresentation, Params: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatal("expected set_presentation accepted")
	}
	got, ok := catalog.Session(rec.ID)
	if !ok {
		t.Fatal("session missing after set_presentation")
	}
	if !got.Hidden {
		t.Fatal("expected Hidden = true")
	}
	if got.Background {
		t.Fatal("expected Background left false (not sent)")
	}

	background := true
	params2, _ := json.Marshal(PresentationParams{Background: &background})
	if _, err := svc.ExecuteSessionCommand(context.Background(), SessionCommand{
		ID: NewCommandID(), Ref: rec.Ref, Action: ActionSetPresentation, Params: params2,
	}); err != nil {
		t.Fatal(err)
	}
	got2, _ := catalog.Session(rec.ID)
	if !got2.Hidden {
		t.Fatal("expected Hidden to remain true after a Background-only update")
	}
	if !got2.Background {
		t.Fatal("expected Background = true")
	}
}

// TestSetPresentationIdempotentReplay proves a retried set_presentation
// (same command ID) returns the original result and does not require the
// target session to still exist in the same shape.
func TestSetPresentationIdempotentReplay(t *testing.T) {
	svc, catalog, _, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("presentreplay"), "gen-prr")
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}

	hidden := true
	params, _ := json.Marshal(PresentationParams{Hidden: &hidden})
	cmd := SessionCommand{ID: NewCommandID(), Ref: rec.Ref, Action: ActionSetPresentation, Params: params}
	res1, err := svc.ExecuteSessionCommand(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := svc.ExecuteSessionCommand(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if res1 != res2 {
		t.Fatalf("replayed set_presentation returned different result: %+v vs %+v", res1, res2)
	}
}

// TestSetPresentationConcurrentDuplicateCommandIDSharesOneResult proves two
// truly concurrent callers presenting the same command ID for
// ActionSetPresentation are serialized by runSingleFlight and return the
// exact same result, matching the guarantee ActionRecover already has.
func TestSetPresentationConcurrentDuplicateCommandIDSharesOneResult(t *testing.T) {
	svc, catalog, _, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("presentconcurrent"), "gen-prc")
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}

	hidden := true
	params, _ := json.Marshal(PresentationParams{Hidden: &hidden})
	cmd := SessionCommand{ID: NewCommandID(), Ref: rec.Ref, Action: ActionSetPresentation, Params: params}

	var wg sync.WaitGroup
	results := make([]CommandResult, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = svc.ExecuteSessionCommand(context.Background(), cmd)
		}()
	}
	wg.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("unexpected errors: %v, %v", errs[0], errs[1])
	}
	if results[0] != results[1] {
		t.Fatalf("concurrent duplicate-ID set_presentation returned different results: %+v vs %+v", results[0], results[1])
	}
}

// TestKillRemoveWorktreeCleansUpWorktreeDirectory proves KillParams.RemoveWorktree
// removes the session's own cwd worktree as part of the same kill command.
func TestKillRemoveWorktreeCleansUpWorktreeDirectory(t *testing.T) {
	repo := initGitRepo(t)
	t.Setenv("HOME", t.TempDir())

	worktreesDir := filepath.Join(repo, ".worktrees")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(worktreesDir, "feature")
	cmdInit := exec.Command("git", "-C", repo, "worktree", "add", "-b", "feature", worktreePath)
	if out, err := cmdInit.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v: %s", err, out)
	}

	svc, catalog, _, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("killworktree"), "gen-kw")
	rec.Cwd = worktreePath
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(KillParams{RemoveWorktree: true})
	res, err := svc.ExecuteSessionCommand(context.Background(), SessionCommand{
		ID: NewCommandID(), Ref: rec.Ref, Action: ActionKill, Params: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatal("expected kill accepted")
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree directory removed, stat err = %v", err)
	}
}

// TestKillRemoveWorktreeReplayDoesNotDoubleRun proves a retried kill (same
// command ID) with RemoveWorktree does not attempt a second removal -- the
// idempotent-replay receipt short-circuits before removeWorktreeForCwd runs
// again.
func TestKillRemoveWorktreeReplayDoesNotDoubleRun(t *testing.T) {
	repo := initGitRepo(t)
	t.Setenv("HOME", t.TempDir())

	worktreesDir := filepath.Join(repo, ".worktrees")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(worktreesDir, "feature")
	cmdInit := exec.Command("git", "-C", repo, "worktree", "add", "-b", "feature", worktreePath)
	if out, err := cmdInit.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v: %s", err, out)
	}

	svc, catalog, _, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("killworktreereplay"), "gen-kwr")
	rec.Cwd = worktreePath
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(KillParams{RemoveWorktree: true})
	cmd := SessionCommand{ID: NewCommandID(), Ref: rec.Ref, Action: ActionKill, Params: params}
	res1, err := svc.ExecuteSessionCommand(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	// Replay must not error even though the worktree directory (and its git
	// registration) is already gone.
	res2, err := svc.ExecuteSessionCommand(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if res1 != res2 {
		t.Fatalf("replayed kill returned different result: %+v vs %+v", res1, res2)
	}
}

// TestScheduleIDCarriesThroughToLocalSessionRecord proves CreateParams.ScheduleID
// survives from the pending create all the way to the materialized
// LocalSessionRecord, so schedule-cap enforcement can query the canonical
// catalog by ScheduleID instead of a separate attribute store.
func TestScheduleIDCarriesThroughToLocalSessionRecord(t *testing.T) {
	svc, catalog, _, _, cleanup := newTestCommandService(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startWorker(ctx, t, svc)

	params, _ := json.Marshal(CreateParams{
		Name:       "scheduled",
		Shell:      "/bin/bash",
		Cwd:        "/tmp",
		ScheduleID: "sched-123",
	})
	res, err := svc.ExecuteSessionCommand(ctx, SessionCommand{ID: NewCommandID(), Action: ActionCreate, Params: params})
	if err != nil {
		t.Fatal(err)
	}

	var rec LocalSessionRecord
	var ok bool
	for start := time.Now(); time.Since(start) < 2*time.Second; {
		if rec, ok = catalog.Session(res.Ref.Session); ok && rec.Phase == SessionPhaseActive {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ok || rec.Phase != SessionPhaseActive {
		t.Fatalf("session did not become active: %+v", rec)
	}
	if rec.ScheduleID != "sched-123" {
		t.Fatalf("expected ScheduleID carried through, got %q", rec.ScheduleID)
	}

	byID := catalog.SessionsByScheduleID("sched-123")
	if len(byID) != 1 || byID[0].ID != rec.ID {
		t.Fatalf("SessionsByScheduleID = %+v; want [%v]", byID, rec.ID)
	}
	if len(catalog.SessionsByScheduleID("nope")) != 0 {
		t.Fatal("expected no sessions for unrelated schedule id")
	}
}

// TestSnapshotProjectionIncludesPresentationAndScheduleFields proves the
// canonical LocalCatalogSnapshot carries the new Hidden/Background/ScheduleID
// fields verbatim, so downstream browser/peer projections have a single
// source of truth instead of a separate attrs store.
func TestSnapshotProjectionIncludesPresentationAndScheduleFields(t *testing.T) {
	svc, catalog, _, _, cleanup := newTestCommandService(t)
	defer cleanup()

	rec := activeRecord(SessionID("snapme"), "gen-sn")
	rec.ScheduleID = "sched-snap"
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}
	hidden := true
	params, _ := json.Marshal(PresentationParams{Hidden: &hidden})
	if _, err := svc.ExecuteSessionCommand(context.Background(), SessionCommand{
		ID: NewCommandID(), Ref: rec.Ref, Action: ActionSetPresentation, Params: params,
	}); err != nil {
		t.Fatal(err)
	}

	snap := catalog.LocalCatalogSnapshot()
	var found *LocalSessionRecord
	for i := range snap.Sessions {
		if snap.Sessions[i].ID == rec.ID {
			found = &snap.Sessions[i]
			break
		}
	}
	if found == nil {
		t.Fatal("session missing from snapshot")
	}
	if !found.Hidden {
		t.Fatal("expected snapshot Hidden = true")
	}
	if found.ScheduleID != "sched-snap" {
		t.Fatalf("expected snapshot ScheduleID = sched-snap, got %q", found.ScheduleID)
	}
}
