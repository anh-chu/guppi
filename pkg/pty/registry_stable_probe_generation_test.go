package pty

import (
	"fmt"
	"net"
	"os"
	"testing"
	"time"
)

// waitForSocket polls until path accepts a connection or the timeout expires.
func waitForSocket(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("unix", path, 100*time.Millisecond); err == nil {
			conn.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("socket %s never became connectable within %v", path, timeout)
}

// TestProbe_DiscoversRealGenerationForGenerationLessBinding is a regression
// test for the production defect where Probe(), when queried with a binding
// that has no Generation set (exactly what Reconciler's bindingForRef does
// for a pending create it does not yet know the assigned generation for),
// never reported the daemon's REAL discovered generation back to the
// caller. binding.IsStable() requires a non-empty Generation, so the old
// code gated the entire handshake-verified branch behind it and skipped
// straight to a coarse PID-liveness fallback that never learned (or
// returned) the daemon's actual generation -- callers like
// Reconciler.adoptLivePending that persist ev.Binding.Generation into a
// session's Compat.Generation ended up permanently recording an empty
// generation. An empty Compat.Generation has two lasting consequences: (1)
// bindingForRecord seeds all FUTURE Probe calls for that session with an
// empty Generation too, permanently disabling the stable/verified path for
// it, and (2) reconciler.go's mayRemoveClean refuses to ever reap a
// cleanly-ended/killed session whose recorded generation is empty -- so a
// session adopted this way can never be removed from the catalog again.
func TestProbe_DiscoversRealGenerationForGenerationLessBinding(t *testing.T) {
	socketDir := t.TempDir()
	stateDir := t.TempDir()

	const (
		daemonKey  = "probe-gen-discovery"
		owner      = "owner-under-test"
		sessionID  = "session-under-test"
		generation = "the-real-generation-0123456789"
	)

	cfg := DaemonConfig{
		ID:         daemonKey,
		Shell:      "/bin/sh",
		Cols:       80,
		Rows:       24,
		Cwd:        os.TempDir(),
		SocketDir:  socketDir,
		StateDir:   stateDir,
		Owner:      owner,
		SessionID:  sessionID,
		Generation: generation,
	}

	go func() {
		_ = RunDaemon(cfg)
	}()

	socketPath := fmt.Sprintf("%s/%s.sock", socketDir, daemonKey)
	waitForSocket(t, socketPath, 10*time.Second)

	store, err := NewLifecycleStore(stateDir)
	if err != nil {
		t.Fatalf("NewLifecycleStore: %v", err)
	}
	// The daemon writes its own lifecycle record asynchronously right after
	// binding the socket; poll for it the same way Registry.getLifecycle
	// would observe it once written.
	deadline := time.Now().Add(5 * time.Second)
	var rec *LifecycleRecord
	for time.Now().Before(deadline) {
		if r, err := store.Get(daemonKey); err == nil && r != nil {
			rec = r
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if rec == nil {
		t.Fatal("daemon never wrote its lifecycle record")
	}

	reg := NewRegistry(socketDir)
	reg.SetLifecycleStore(store)

	// This is exactly reconciler.go's bindingForRef: Owner/SessionID known,
	// Generation deliberately empty (the reconciler does not know the
	// generation a pending create's daemon will end up with).
	probeBinding := StableBinding{
		Owner:     owner,
		SessionID: sessionID,
		DaemonKey: daemonKey,
	}

	ev := reg.Probe(probeBinding)

	if ev.Status != ProbeLive {
		t.Fatalf("expected ProbeLive for a real live daemon, got %v (reason: %s)", ev.Status, ev.Reason)
	}
	if ev.Binding.Generation != generation {
		t.Fatalf("expected Probe to discover and report the daemon's real generation %q, got %q; ev.Reason=%q ev.DaemonPID=%d rec.DaemonPID=%d rec.ProcStartTime=%d", generation, ev.Binding.Generation, ev.Reason, ev.DaemonPID, rec.DaemonPID, rec.ProcStartTime)
	}
}
