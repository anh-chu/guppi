package state

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/pty"
)

func testOwner() OwnerID { return OwnerID("owtestaa") }

func testRef(id SessionID) SessionRef {
	return SessionRef{Owner: testOwner(), Session: id, Window: 0, Pane: 0}
}

func activeRecord(id SessionID, gen string) LocalSessionRecord {
	return LocalSessionRecord{
		ID:      id,
		Owner:   testOwner(),
		Ref:     testRef(id),
		Phase:   SessionPhaseActive,
		Desired: DesiredRun,
		Created: time.Now(),
		Compat:  CompatLocalSession{Name: string(id), Generation: gen, Shell: "/bin/bash", Cwd: "/tmp"},
	}
}

type fakeBackend struct {
	mu             sync.Mutex
	probes         map[string]pty.ProbeEvidence
	starts         map[string]pty.ReadyInfo
	startErrors    map[string]error
	terminateCalls int32
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		probes:      make(map[string]pty.ProbeEvidence),
		starts:      make(map[string]pty.ReadyInfo),
		startErrors: make(map[string]error),
	}
}

func (f *fakeBackend) key(b pty.StableBinding) string { return b.SessionID }

func (f *fakeBackend) setProbe(b pty.StableBinding, ev pty.ProbeEvidence) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes[f.key(b)] = ev
}

func (f *fakeBackend) setStart(b pty.StableBinding, info pty.ReadyInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts[f.key(b)] = info
}

func (f *fakeBackend) setStartError(b pty.StableBinding, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startErrors[f.key(b)] = err
}

func (f *fakeBackend) Probe(b pty.StableBinding) pty.ProbeEvidence {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ev, ok := f.probes[f.key(b)]; ok {
		if ev.Binding.SessionID == "" {
			ev.Binding = b
		}
		return ev
	}
	return pty.ProbeEvidence{Status: pty.ProbeUnknown, Binding: b, Reason: "test default unknown"}
}

func (f *fakeBackend) Start(ctx context.Context, req pty.StartRequest) (pty.ReadyInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := f.key(req.StableBinding)
	if err, ok := f.startErrors[key]; ok {
		return pty.ReadyInfo{}, err
	}
	if info, ok := f.starts[key]; ok {
		return info, nil
	}
	return pty.ReadyInfo{}, errors.New("start not configured")
}

func (f *fakeBackend) Terminate(ctx context.Context, b pty.StableBinding) pty.TerminateOutcome {
	atomic.AddInt32(&f.terminateCalls, 1)
	return pty.TerminateAcknowledged
}

func (f *fakeBackend) terminateCount() int { return int(atomic.LoadInt32(&f.terminateCalls)) }

func newTestStore(t *testing.T, owner OwnerID) (*Store, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "termyard-state-test")
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir, "test-node", StoreOptions{Owner: owner})
	if err != nil {
		t.Fatal(err)
	}
	return store, func() { _ = os.RemoveAll(dir) }
}

func TestReconcilerLiveKeepsActive(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	if err := catalog.Load(); err != nil {
		t.Fatal(err)
	}
	backend := newFakeBackend()
	rec := activeRecord(SessionID("sesslive"), "gen1")
	if err := catalog.PutSession(rec); err != nil {
		t.Fatal(err)
	}

	backend.setProbe(bindingForRecord(&rec), pty.ProbeEvidence{
		Status: pty.ProbeLive,
		Binding: pty.StableBinding{
			Owner:      string(owner),
			SessionID:  "sesslive",
			Generation: "gen1",
		},
		DaemonPID: 42,
		Reason:    "live identity handshake",
	})

	var published OwnerCatalogSnapshot
	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	r.Subscribe(func(s OwnerCatalogSnapshot) { published = s })

	before := catalog.Revision()
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if catalog.Revision() != before {
		t.Fatalf("expected no revision change for unchanged live record, before=%d after=%d", before, catalog.Revision())
	}
	got, ok := catalog.Session(rec.ID)
	if !ok || got.Phase != SessionPhaseActive {
		t.Fatalf("expected active, got %v", got)
	}
	if published.Owner != "" {
		t.Fatal("no-change reconciliation should not publish snapshot")
	}
}

func TestReconcilerCleanExitRemovesWhenGenerationMatches(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	backend := newFakeBackend()
	rec := activeRecord(SessionID("sessclean"), "gen1")
	rec.Desired = DesiredStop
	_ = catalog.PutSession(rec)

	backend.setProbe(bindingForRecord(&rec), pty.ProbeEvidence{
		Status: pty.ProbeClean,
		Binding: pty.StableBinding{
			Owner:      string(owner),
			SessionID:  "sessclean",
			Generation: "gen1",
		},
		Reason: "daemon exited cleanly",
	})

	var published OwnerCatalogSnapshot
	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	r.Subscribe(func(s OwnerCatalogSnapshot) { published = s })

	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.Session(rec.ID); ok {
		t.Fatal("expected clean session to be removed")
	}
	if published.Owner == "" {
		t.Fatal("expected snapshot publish after removal")
	}
}

func TestReconcilerCrashedStays(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	backend := newFakeBackend()
	rec := activeRecord(SessionID("sesscrash"), "gen1")
	_ = catalog.PutSession(rec)

	backend.setProbe(bindingForRecord(&rec), pty.ProbeEvidence{
		Status: pty.ProbeCrashed,
		Binding: pty.StableBinding{
			Owner:      string(owner),
			SessionID:  "sesscrash",
			Generation: "gen1",
		},
		Reason: "process dead with active lifecycle",
	})

	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := catalog.Session(rec.ID)
	if !ok || got.Phase != SessionPhaseCrashed {
		t.Fatalf("expected crashed, got %v", got)
	}
}

func TestReconcilerUnknownPreservesSession(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	backend := newFakeBackend()
	rec := activeRecord(SessionID("sesscanerr"), "gen1")
	_ = catalog.PutSession(rec)

	// Default unknown is enough, no explicit probe needed.
	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := catalog.Session(rec.ID)
	if !ok || got.Phase != SessionPhaseActive {
		t.Fatalf("expected session preserved as active, got %v", got)
	}
}

func TestReconcilerPIDReuseClassifiesCrashed(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	backend := newFakeBackend()
	rec := activeRecord(SessionID("sesspidreuse"), "gen1")
	_ = catalog.PutSession(rec)

	backend.setProbe(bindingForRecord(&rec), pty.ProbeEvidence{
		Status: pty.ProbeCrashed,
		Binding: pty.StableBinding{
			Owner:      string(owner),
			SessionID:  "sesspidreuse",
			Generation: "gen1",
		},
		Reason: "PID reused by different process",
	})

	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := catalog.Session(rec.ID)
	if got.Phase != SessionPhaseCrashed {
		t.Fatalf("expected crashed for PID reuse, got %v", got.Phase)
	}
}

func TestReconcilerStaleGenerationKeepsClean(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	backend := newFakeBackend()
	rec := activeRecord(SessionID("sessstale"), "gen-old")
	rec.Desired = DesiredStop
	_ = catalog.PutSession(rec)

	backend.setProbe(bindingForRecord(&rec), pty.ProbeEvidence{
		Status: pty.ProbeClean,
		Binding: pty.StableBinding{
			Owner:      string(owner),
			SessionID:  "sessstale",
			Generation: "gen-new",
		},
		Reason: "clean exit of a newer generation",
	})

	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := catalog.Session(rec.ID)
	if !ok || got.Phase != SessionPhaseCleanlyEnded {
		t.Fatalf("expected cleanly ended preserved, got %v", got)
	}
}

func TestReconcilerMissingLifecycleTreatsActiveAsLive(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	backend := newFakeBackend()
	rec := activeRecord(SessionID("sessnolc"), "")
	_ = catalog.PutSession(rec)

	backend.setProbe(bindingForRecord(&rec), pty.ProbeEvidence{
		Status: pty.ProbeLive,
		Binding: pty.StableBinding{
			Owner:      string(owner),
			SessionID:  "sessnolc",
			Generation: "",
		},
		Reason: "legacy socket live",
	})

	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := catalog.Session(rec.ID)
	if got.Phase != SessionPhaseActive {
		t.Fatalf("expected active for legacy live, got %v", got.Phase)
	}
}

func TestReconcilerStartingBecomesActive(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	backend := newFakeBackend()
	rec := activeRecord(SessionID("sessstart"), "gen1")
	rec.Phase = SessionPhaseStarting
	rec.Compat.Generation = "gen1"
	_ = catalog.PutSession(rec)

	backend.setProbe(bindingForRecord(&rec), pty.ProbeEvidence{
		Status: pty.ProbeLive,
		Binding: pty.StableBinding{
			Owner:      string(owner),
			SessionID:  "sessstart",
			Generation: "gen1",
		},
		DaemonPID: 100,
		Reason:    "identity handshake matches",
	})

	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := catalog.Session(rec.ID)
	if got.Phase != SessionPhaseActive {
		t.Fatalf("expected starting->active transition, got %v", got.Phase)
	}
}

func TestReconcilerPendingCreateAdoptsLiveDaemon(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	backend := newFakeBackend()

	ref := testRef(SessionID("sesspendinglive"))
	pending := PendingCreateRecord{
		IntentID: NewCommandID(),
		Ref:      ref,
		Inserted: time.Now(),
		Shell:    "/bin/zsh",
		Cwd:      "/home/u",
		Cols:     80,
		Rows:     24,
	}
	_ = catalog.PutPendingCreate(pending)

	backend.setProbe(bindingForRef(ref), pty.ProbeEvidence{
		Status: pty.ProbeLive,
		Binding: pty.StableBinding{
			Owner:      string(owner),
			SessionID:  "sesspendinglive",
			Generation: "gen-pending",
		},
		DaemonPID: 200,
		Reason:    "already running",
	})

	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.Session(ref.Session); !ok {
		t.Fatal("expected pending create to be adopted as active record")
	}
	pendingList := catalog.PendingCreates()
	if len(pendingList) != 0 {
		t.Fatalf("expected pending create removed, got %v", pendingList)
	}
}

func TestReconcilerPendingCreateAdoptsLiveDaemonPreservesDisplayName(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	backend := newFakeBackend()

	ref := testRef(SessionID("sesspendingdisplay"))
	pending := PendingCreateRecord{
		IntentID:    NewCommandID(),
		Ref:         ref,
		Inserted:    time.Now(),
		Shell:       "/bin/zsh",
		Cwd:         "/home/u",
		Cols:        80,
		Rows:        24,
		DisplayName: "manualtest",
	}
	_ = catalog.PutPendingCreate(pending)

	backend.setProbe(bindingForRef(ref), pty.ProbeEvidence{
		Status: pty.ProbeLive,
		Binding: pty.StableBinding{
			Owner:      string(owner),
			SessionID:  "sesspendingdisplay",
			Generation: "gen-pending",
		},
		DaemonPID: 200,
		Reason:    "already running",
	})

	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := catalog.Session(ref.Session)
	if !ok {
		t.Fatal("expected pending create to be adopted as active record")
	}
	if got.Compat.Name != "manualtest" {
		t.Fatalf("expected Compat.Name to preserve user-requested display name %q, got %q", "manualtest", got.Compat.Name)
	}
}

// TestReconcilerDisablePendingCreatesSkipsProbeAdoption covers a real defect:
// every production caller of NewReconciler (pkg/commands/server/runtime.go)
// sets DisablePendingCreates: true specifically to hand off ALL pending-
// create resolution to SessionCommandService, but ReconcileOnce's own
// probe-and-adopt loop (unlike ResolveIntents, which already checked this
// flag) never checked it -- so the reconciler kept independently probing and
// adopting pending creates anyway, racing SessionCommandService's own
// in-flight executePendingCreate/Start/waitReady call for the exact same
// pending record. This test asserts that with DisablePendingCreates: true,
// ReconcileOnce leaves a live-daemon pending create alone: it must not
// adopt it into an active session record, and the pending record must
// survive untouched.
func TestReconcilerDisablePendingCreatesSkipsProbeAdoption(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	backend := newFakeBackend()

	ref := testRef(SessionID("sessdisabledpending"))
	pending := PendingCreateRecord{
		IntentID: NewCommandID(),
		Ref:      ref,
		Inserted: time.Now(),
		Shell:    "/bin/zsh",
		Cwd:      "/home/u",
		Cols:     80,
		Rows:     24,
	}
	_ = catalog.PutPendingCreate(pending)

	// A real command service's spawn for THIS exact pending record could
	// still be in flight (e.g. mid-waitReady); backend reporting the daemon
	// live here mirrors that in-flight attempt's own daemon having already
	// come up.
	backend.setProbe(bindingForRef(ref), pty.ProbeEvidence{
		Status: pty.ProbeLive,
		Binding: pty.StableBinding{
			Owner:      string(owner),
			SessionID:  "sessdisabledpending",
			Generation: "gen-pending",
		},
		DaemonPID: 200,
		Reason:    "already running",
	})

	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{DisablePendingCreates: true})
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, ok := catalog.Session(ref.Session); ok {
		t.Fatal("expected DisablePendingCreates to prevent ReconcileOnce from adopting the pending create into a session record, but it was adopted")
	}
	pendingList := catalog.PendingCreates()
	if len(pendingList) != 1 {
		t.Fatalf("expected the pending create to remain untouched, got %d pending records", len(pendingList))
	}
}

func TestReconcilerStartPendingUsesDisplayNameWithSessionIDFallback(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	backend := newFakeBackend()

	// Case 1: user-requested display name must survive startPending.
	refNamed := testRef(SessionID("sessstartnamed"))
	pendingNamed := PendingCreateRecord{
		IntentID:    NewCommandID(),
		Ref:         refNamed,
		Inserted:    time.Now(),
		Shell:       "/bin/bash",
		Cwd:         "/tmp",
		DisplayName: "manualtest",
	}
	_ = catalog.PutPendingCreate(pendingNamed)
	backend.setStart(bindingForRef(refNamed), pty.ReadyInfo{
		Owner:      string(owner),
		SessionID:  "sessstartnamed",
		Generation: "gen1",
		DaemonPID:  111,
	})

	// Case 2: no display name requested -> falls back to raw session ID.
	refUnnamed := testRef(SessionID("sessstartunnamed"))
	pendingUnnamed := PendingCreateRecord{
		IntentID: NewCommandID(),
		Ref:      refUnnamed,
		Inserted: time.Now(),
		Shell:    "/bin/bash",
		Cwd:      "/tmp",
	}
	_ = catalog.PutPendingCreate(pendingUnnamed)
	backend.setStart(bindingForRef(refUnnamed), pty.ReadyInfo{
		Owner:      string(owner),
		SessionID:  "sessstartunnamed",
		Generation: "gen1",
		DaemonPID:  112,
	})

	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	if err := r.ResolveIntents(context.Background()); err != nil {
		t.Fatal(err)
	}

	gotNamed, ok := catalog.Session(refNamed.Session)
	if !ok {
		t.Fatal("expected named pending create to be started as active record")
	}
	if gotNamed.Compat.Name != "manualtest" {
		t.Fatalf("expected Compat.Name to preserve user-requested display name %q, got %q", "manualtest", gotNamed.Compat.Name)
	}

	gotUnnamed, ok := catalog.Session(refUnnamed.Session)
	if !ok {
		t.Fatal("expected unnamed pending create to be started as active record")
	}
	if gotUnnamed.Compat.Name != string(refUnnamed.Session) {
		t.Fatalf("expected Compat.Name fallback to raw session ID %q, got %q", string(refUnnamed.Session), gotUnnamed.Compat.Name)
	}
}

func TestReconcilerStopDesiredCallsTerminate(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	backend := newFakeBackend()
	rec := activeRecord(SessionID("sessstop"), "gen1")
	rec.Desired = DesiredStop
	_ = catalog.PutSession(rec)

	backend.setProbe(bindingForRecord(&rec), pty.ProbeEvidence{
		Status: pty.ProbeLive,
		Binding: pty.StableBinding{
			Owner:      string(owner),
			SessionID:  "sessstop",
			Generation: "gen1",
		},
		DaemonPID: 5,
		Reason:    "live",
	})

	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if backend.terminateCount() != 1 {
		t.Fatalf("expected terminate called once, got %d", backend.terminateCount())
	}
	got, _ := catalog.Session(rec.ID)
	if got.Phase != SessionPhaseActive || got.Desired != DesiredStop {
		t.Fatalf("expected active+desired stop while waiting for clean exit, got %v", got)
	}
}

func TestReconcilerResolveIntentRecoversCrashed(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	backend := newFakeBackend()
	rec := activeRecord(SessionID("sessrestart"), "gen1")
	rec.Phase = SessionPhaseCrashed
	rec.Desired = DesiredRestart
	_ = catalog.PutSession(rec)

	backend.setStart(bindingForRecord(&rec), pty.ReadyInfo{
		Owner:      string(owner),
		SessionID:  "sessrestart",
		Generation: "gen2",
		DaemonPID:  77,
		ShellPID:   78,
	})

	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	if err := r.ResolveIntents(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := catalog.Session(rec.ID)
	if got.Phase != SessionPhaseActive || got.Desired != DesiredRun || got.Compat.Generation != "gen2" {
		t.Fatalf("expected recovered active record, got %v", got)
	}
}

func TestReconcilerNoChangeAvoidsDurableWrite(t *testing.T) {
	owner := testOwner()
	dir, err := os.MkdirTemp("", "termyard-state-nowrite")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	var writes int32
	var syncs int32
	store, err := OpenStore(dir, "test-node", StoreOptions{
		Owner: owner,
		WriteHook: func(f *os.File, p []byte) error {
			atomic.AddInt32(&writes, 1)
			_, err := f.Write(p)
			return err
		},
		SyncHook: func(f *os.File) error {
			atomic.AddInt32(&syncs, 1)
			return f.Sync()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	backend := newFakeBackend()
	rec := activeRecord(SessionID("sessnowrite"), "gen1")
	_ = catalog.PutSession(rec)

	// Reset counters after initial catalog PutSession.
	atomic.StoreInt32(&writes, 0)
	atomic.StoreInt32(&syncs, 0)

	backend.setProbe(bindingForRecord(&rec), pty.ProbeEvidence{
		Status: pty.ProbeLive,
		Binding: pty.StableBinding{
			Owner:      string(owner),
			SessionID:  "sessnowrite",
			Generation: "gen1",
		},
		DaemonPID: 999,
		Reason:    "live",
	})

	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	start := time.Now()
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if atomic.LoadInt32(&writes) != 0 || atomic.LoadInt32(&syncs) != 0 {
		t.Fatalf("expected no durable write, got writes=%d syncs=%d", atomic.LoadInt32(&writes), atomic.LoadInt32(&syncs))
	}
	if elapsed > 10*time.Millisecond {
		t.Fatalf("no-change reconciliation too slow: %v", elapsed)
	}
}

// TestReconcilerUnresolvedPendingCreateAvoidsRepublish covers a real defect:
// ReconcileOnce used to force an apply+publish on every single tick as long
// as ANY pending create existed, even when nothing about the classified
// sessions actually changed and the pending create was still genuinely
// unresolved (e.g. a real daemon spawn still in flight under load). Because
// SessionCommandService.executeCreate inserts the new session's layout leaf
// before the session record itself lands in doc.Sessions, that forced
// republish sends a catalog-invariant-invalid snapshot (layout references an
// unknown session) to every connected peer on every tick until the create
// resolves -- each of which pkg/peer/manager.go's validateCatalogInvariants
// correctly rejects, so this was pure repeated no-op churn (and, over a real
// peer link, a stream of "dropping v2 snapshot" warnings) rather than making
// any progress. A tick that changes nothing must not commit or publish
// anything, regardless of pending-create count.
func TestReconcilerUnresolvedPendingCreateAvoidsRepublish(t *testing.T) {
	owner := testOwner()
	dir, err := os.MkdirTemp("", "termyard-state-pending-nowrite")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	var writes int32
	store, err := OpenStore(dir, "test-node", StoreOptions{
		Owner: owner,
		WriteHook: func(f *os.File, p []byte) error {
			atomic.AddInt32(&writes, 1)
			_, werr := f.Write(p)
			return werr
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()

	ref := testRef(SessionID("sesspendingnowrite"))
	pending := PendingCreateRecord{
		IntentID:    NewCommandID(),
		Ref:         ref,
		Inserted:    time.Now(),
		Shell:       "/bin/bash",
		Cwd:         "/home/u",
		DisplayName: "pending-session",
	}
	if err := catalog.PutPendingCreate(pending); err != nil {
		t.Fatal(err)
	}

	// Reset the write counter after the setup PutPendingCreate commit.
	atomic.StoreInt32(&writes, 0)

	var publishes int32
	unsub := catalog.SubscribeCatalog(func(OwnerCatalogSnapshot) {
		atomic.AddInt32(&publishes, 1)
	})
	defer unsub()

	// backend never reports this pending create's daemon as live (default
	// probe is ProbeUnknown, mirroring a real daemon spawn still in flight),
	// so the pending create remains genuinely unresolved across every tick.
	backend := newFakeBackend()
	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{DisablePendingCreates: true})

	for i := 0; i < 5; i++ {
		if err := r.ReconcileOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	if got := atomic.LoadInt32(&writes); got != 0 {
		t.Fatalf("expected no durable write while pending create stays unresolved and nothing else changed, got %d", got)
	}
	if got := atomic.LoadInt32(&publishes); got != 0 {
		t.Fatalf("expected no catalog republish while pending create stays unresolved and nothing else changed, got %d", got)
	}
	if p := catalog.PendingCreates(); len(p) != 1 {
		t.Fatalf("expected the pending create to remain, got %d", len(p))
	}
}

func TestCatalogGettersReturnCopies(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	rec := activeRecord(SessionID("copytest"), "g")
	_ = catalog.PutSession(rec)

	sessions := catalog.Sessions()
	if len(sessions) != 1 {
		t.Fatal("expected one session")
	}
	sessions[0].Phase = SessionPhaseCrashed
	got, _ := catalog.Session(rec.ID)
	if got.Phase != SessionPhaseActive {
		t.Fatal("getter copy allowed mutation of internal record")
	}
}

func TestCatalogProjectionDoesNotMutateStore(t *testing.T) {
	owner := testOwner()
	store, cleanup := newTestStore(t, owner)
	defer cleanup()

	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	rec := activeRecord(SessionID("projtest"), "g")
	_ = catalog.PutSession(rec)

	snap := catalog.LocalCatalogSnapshot()
	if len(snap.Sessions) != 1 {
		t.Fatal("expected one session in snapshot")
	}
	snap.Sessions[0].Phase = SessionPhaseCrashed
	got, _ := catalog.Session(rec.ID)
	if got.Phase != SessionPhaseActive {
		t.Fatal("snapshot projection mutated persisted record")
	}
}

func TestFirstSnapshotCompleteAfterRestart(t *testing.T) {
	owner := testOwner()
	dir, err := os.MkdirTemp("", "termyard-state-restart")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	store, err := OpenStore(dir, "test-node", StoreOptions{Owner: owner})
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalog(owner, store)
	_ = catalog.Load()
	backend := newFakeBackend()

	ids := []SessionID{SessionID("aa"), SessionID("bb"), SessionID("cc")}
	for _, id := range ids {
		rec := activeRecord(id, "gen-"+string(id))
		_ = catalog.PutSession(rec)
		backend.setProbe(bindingForRecord(&rec), pty.ProbeEvidence{
			Status: pty.ProbeLive,
			Binding: pty.StableBinding{
				Owner:      string(owner),
				SessionID:  string(id),
				Generation: "gen-" + string(id),
			},
			DaemonPID: 1,
			Reason:    "live",
		})
	}

	var snapshots []OwnerCatalogSnapshot
	var mu sync.Mutex
	r := NewReconciler(catalog, backend, nil, ReconcilerOptions{})
	r.Subscribe(func(s OwnerCatalogSnapshot) {
		mu.Lock()
		defer mu.Unlock()
		snapshots = append(snapshots, s)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = r.Run(ctx)
	}()

	// Wait for the immediate snapshot produced at startup.
	for start := time.Now(); time.Since(start) < time.Second; {
		mu.Lock()
		if len(snapshots) > 0 {
			mu.Unlock()
			break
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(snapshots) == 0 {
		t.Fatal("expected first snapshot immediately")
	}
	first := snapshots[0]
	if len(first.Sessions) != len(ids) {
		t.Fatalf("expected complete first snapshot with %d sessions, got %d", len(ids), len(first.Sessions))
	}
	for _, rec := range first.Sessions {
		if rec.Phase != SessionPhaseActive {
			t.Fatalf("expected first snapshot classified as active, got %v", rec.Phase)
		}
	}
}
