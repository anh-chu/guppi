package state

import (
	"context"
	"encoding/json"
	"errors"
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
}

func newTestBackend() *testBackend {
	return &testBackend{
		startResult:  pty.ReadyInfo{DaemonPID: 42, ShellPID: 43, Generation: "gen-test"},
		terminateOut: pty.TerminateAcknowledged,
		liveGen:      make(map[string]string),
		terminated:   make(map[string]bool),
	}
}

func (b *testBackend) Start(ctx context.Context, req pty.StartRequest) (pty.ReadyInfo, error) {
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
	if rec.Compat.Generation == "" {
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

func TestLabelOnlyMutatesCompatName(t *testing.T) {
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
	if got.Compat.Name != "renamed" {
		t.Fatalf("expected display name changed, got %q", got.Compat.Name)
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
	if got.Phase != SessionPhaseActive || got.Compat.Generation != "gen-new" {
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
