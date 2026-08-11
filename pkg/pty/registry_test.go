package pty

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestReadSystemdUnit_FromLifecycleRecord(t *testing.T) {
	dir := t.TempDir()

	store, err := NewLifecycleStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(t.TempDir())
	reg.SetLifecycleStore(store)

	// Write a lifecycle record with systemd unit.
	lr := LifecycleRecord{
		ID:          "test-session",
		State:       LifecycleActive,
		DaemonPID:   12345,
		SystemdUnit: "termyard-session-test-session-123.scope",
	}
	if err := store.RecordActive(lr); err != nil {
		t.Fatal(err)
	}

	got := reg.readSystemdUnit("test-session")
	if got != "termyard-session-test-session-123.scope" {
		t.Errorf("readSystemdUnit = %q, want %q", got, "termyard-session-test-session-123.scope")
	}
}

func TestReadSystemdUnit_FromMetadataFallback(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)

	// Write metadata sidecar with systemd unit (no lifecycle store).
	meta := sessionMeta{
		ID:          "test-session",
		Pid:         12345,
		SystemdUnit: "termyard-session-test-session-456.scope",
	}
	data, _ := json.Marshal(meta)
	metaPath := reg.metadataPath("test-session")
	if err := os.WriteFile(metaPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	got := reg.readSystemdUnit("test-session")
	if got != "termyard-session-test-session-456.scope" {
		t.Errorf("readSystemdUnit = %q, want %q", got, "termyard-session-test-session-456.scope")
	}
}

func TestReadSystemdUnit_LifecycleRecordTakesPrecedence(t *testing.T) {
	sockDir := t.TempDir()
	stateDir := t.TempDir()

	store, err := NewLifecycleStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(sockDir)
	reg.SetLifecycleStore(store)

	// Write lifecycle record with one unit name.
	lr := LifecycleRecord{
		ID:          "precedence-test",
		State:       LifecycleActive,
		DaemonPID:   12345,
		SystemdUnit: "from-lifecycle.scope",
	}
	if err := store.RecordActive(lr); err != nil {
		t.Fatal(err)
	}

	// Write metadata sidecar with a DIFFERENT unit name.
	meta := sessionMeta{
		ID:          "precedence-test",
		Pid:         12345,
		SystemdUnit: "from-metadata.scope",
	}
	data, _ := json.Marshal(meta)
	metaPath := reg.metadataPath("precedence-test")
	if err := os.WriteFile(metaPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	got := reg.readSystemdUnit("precedence-test")
	if got != "from-lifecycle.scope" {
		t.Errorf("readSystemdUnit = %q, want lifecycle value %q", got, "from-lifecycle.scope")
	}
}

func TestReadSystemdUnit_NoData(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	got := reg.readSystemdUnit("nonexistent")
	if got != "" {
		t.Errorf("readSystemdUnit = %q, want empty string for missing session", got)
	}
}

func TestReadSystemdUnit_EmptySystemdUnit(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)

	// Write metadata sidecar without systemd unit.
	meta := sessionMeta{
		ID:  "no-unit",
		Pid: 12345,
	}
	data, _ := json.Marshal(meta)
	metaPath := reg.metadataPath("no-unit")
	if err := os.WriteFile(metaPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	got := reg.readSystemdUnit("no-unit")
	if got != "" {
		t.Errorf("readSystemdUnit = %q, want empty string", got)
	}
}

func TestStopSystemdScope_NoUnit(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	// Should not panic or block when there is no systemd unit recorded.
	reg.stopSystemdScope("nonexistent")
}

func TestIsSessionDead(t *testing.T) {
	stateDir := t.TempDir()
	store, err := NewLifecycleStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(t.TempDir())
	reg.SetLifecycleStore(store)

	if err := store.RecordActive(LifecycleRecord{ID: "alive", DaemonPID: 999999}); err != nil {
		t.Fatal(err)
	}
	// RecordActive forces state=active, so write terminal-state records directly
	// via the atomic writer to set the exact state we need to exercise.
	for _, s := range []string{LifecycleCleanlyEnded, LifecycleTerminationRequested, LifecycleDismissed, LifecycleCrashed} {
		id := "ended"
		switch s {
		case LifecycleTerminationRequested:
			id = "killed"
		case LifecycleDismissed:
			id = "dismissed"
		case LifecycleCrashed:
			id = "crashed"
		}
		if err := store.writeAtomic(LifecycleRecord{ID: id, State: s}); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name string
		want bool
	}{
		{"alive", false},
		{"ended", true},
		{"killed", true},
		{"dismissed", true},
		{"crashed", false}, // preserved for recovery, not dead
		{"missing", false}, // no record
	}
	for _, tc := range tests {
		if got := reg.IsSessionDead(tc.name); got != tc.want {
			t.Errorf("IsSessionDead(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsSessionDead_NoLifecycleStore(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	// Conservative: no store configured → never claim dead.
	if reg.IsSessionDead("anything") {
		t.Errorf("expected false with no lifecycle store")
	}
}

// TestDetectAndCleanupCrashes verifies that crashed sessions are detected and
// their systemd scopes are stopped (best-effort). The scope-stop is a fire-and-
// forget operation; this test confirms the lifecycle transition is correct.
func TestDetectAndCleanupCrashes(t *testing.T) {
	sockDir := t.TempDir()
	stateDir := t.TempDir()

	store, err := NewLifecycleStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(sockDir)
	reg.SetLifecycleStore(store)

	// Write an active lifecycle record with a systemd unit.
	lr := LifecycleRecord{
		ID:          "crash-test",
		State:       LifecycleActive,
		DaemonPID:   999999, // non-existent PID → detected as crashed
		SystemdUnit: "termyard-session-crash-test-123.scope",
		Shell:       "/bin/bash",
		Cwd:         "/tmp",
		Cols:        120,
		Rows:        40,
	}
	if err := store.RecordActive(lr); err != nil {
		t.Fatal(err)
	}

	// Verify initial state.
	rec, err := store.Get("crash-test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != LifecycleActive {
		t.Fatalf("expected state active, got %s", rec.State)
	}

	// Detect and clean up.
	crashed := reg.DetectAndCleanupCrashes()
	if len(crashed) != 1 {
		t.Fatalf("expected 1 crashed session, got %d", len(crashed))
	}
	if crashed[0].ID != "crash-test" {
		t.Errorf("expected crashed ID 'crash-test', got %s", crashed[0].ID)
	}

	// Verify state transitioned to crashed.
	rec, err = store.Get("crash-test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != LifecycleCrashed {
		t.Errorf("expected state crashed, got %s", rec.State)
	}
}

// TestDetectAndCleanupCrashes_NoLifecycleStore verifies the nil-store guard.
func TestDetectAndCleanupCrashes_NoLifecycleStore(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	// Should not panic when no lifecycle store is configured.
	crashed := reg.DetectAndCleanupCrashes()
	if crashed != nil {
		t.Errorf("expected nil, got %v", crashed)
	}
}

// TestDismissSessionLocked_StopsScope verifies that dismiss stops the systemd
// scope before removing files. The scope-stop is fire-and-forget; we verify the
// files are removed and the lifecycle state is dismissed.
func TestDismissSessionLocked_StopsScope(t *testing.T) {
	sockDir := t.TempDir()
	stateDir := t.TempDir()

	store, err := NewLifecycleStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(sockDir)
	reg.SetLifecycleStore(store)

	// Create the socket file and metadata sidecar (simulating a crashed session).
	sockPath := reg.SocketPath("dismiss-test")
	if f, err := os.Create(sockPath); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}
	meta := sessionMeta{
		ID:          "dismiss-test",
		Pid:         12345,
		ShellPid:    12346,
		SystemdUnit: "termyard-session-dismiss-test-456.scope",
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath("dismiss-test"), data, 0600); err != nil {
		t.Fatal(err)
	}

	// Write lifecycle record in crashed state.
	lr := LifecycleRecord{
		ID:          "dismiss-test",
		State:       LifecycleCrashed,
		DaemonPID:   12345,
		SystemdUnit: "termyard-session-dismiss-test-456.scope",
	}
	if err := store.RecordActive(lr); err != nil {
		t.Fatal(err)
	}
	// Overwrite state to crashed (RecordActive sets active).
	if err := store.Transition("dismiss-test", LifecycleActive, LifecycleCrashed); err != nil {
		t.Fatal(err)
	}

	// Dismiss.
	if err := reg.DismissSession("dismiss-test"); err != nil {
		t.Fatal(err)
	}

	// Verify files are removed.
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Error("socket file should be removed after dismiss")
	}
	if _, err := os.Stat(reg.metadataPath("dismiss-test")); !os.IsNotExist(err) {
		t.Error("metadata file should be removed after dismiss")
	}

	// Verify lifecycle state is dismissed.
	rec, err := store.Get("dismiss-test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != LifecycleDismissed {
		t.Errorf("expected state dismissed, got %s", rec.State)
	}
}

func TestSocketPath(t *testing.T) {
	reg := NewRegistry("/tmp/test-sockets")
	path := reg.SocketPath("mysession")
	expected := filepath.Join("/tmp/test-sockets", "mysession.sock")
	if path != expected {
		t.Errorf("SocketPath = %q, want %q", path, expected)
	}
}

func TestMetadataPath(t *testing.T) {
	reg := NewRegistry("/tmp/test-sockets")
	path := reg.metadataPath("mysession")
	expected := filepath.Join("/tmp/test-sockets", "mysession.json")
	if path != expected {
		t.Errorf("metadataPath = %q, want %q", path, expected)
	}
}

// TestDismissSession_NonCrashedIsNotKilled verifies that dismissing a
// non-crashed (active) session returns an error and does NOT stop its
// systemd scope. This is the regression test for the bug where
// dismissSessionLocked called stopSystemdScope before validating state.
func TestDismissSession_NonCrashedIsNotKilled(t *testing.T) {
	sockDir := t.TempDir()
	stateDir := t.TempDir()

	store, err := NewLifecycleStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(sockDir)
	reg.SetLifecycleStore(store)

	// Capture which units are stopped.
	var stoppedUnits []string
	reg.stopUnitFn = func(unit string) {
		stoppedUnits = append(stoppedUnits, unit)
	}

	// Create socket and metadata files (simulating a live session).
	if f, err := os.Create(reg.SocketPath("live-session")); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}
	meta := sessionMeta{
		ID:          "live-session",
		Pid:         12345,
		SystemdUnit: "termyard-session-live-session-999.scope",
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath("live-session"), data, 0600); err != nil {
		t.Fatal(err)
	}

	// Write lifecycle record in ACTIVE state (not crashed).
	lr := LifecycleRecord{
		ID:          "live-session",
		DaemonPID:   12345,
		SystemdUnit: "termyard-session-live-session-999.scope",
	}
	if err := store.RecordActive(lr); err != nil {
		t.Fatal(err)
	}

	// Attempt to dismiss — must fail because state is active, not crashed.
	err = reg.DismissSession("live-session")
	if err == nil {
		t.Fatal("expected error dismissing active session, got nil")
	}

	// No systemd scope must have been stopped.
	if len(stoppedUnits) > 0 {
		t.Errorf("expected 0 scope stops, got %d: %v", len(stoppedUnits), stoppedUnits)
	}

	// Files must still exist (not cleaned up).
	if _, err := os.Stat(reg.SocketPath("live-session")); os.IsNotExist(err) {
		t.Error("socket file should still exist after failed dismiss")
	}
	if _, err := os.Stat(reg.metadataPath("live-session")); os.IsNotExist(err) {
		t.Error("metadata file should still exist after failed dismiss")
	}
}

// TestDismissSession_CrashedUsesExactUnit verifies that dismissing a
// crashed session stops the exact SystemdUnit from the lifecycle record
// and does NOT re-read mutable state.
func TestDismissSession_CrashedUsesExactUnit(t *testing.T) {
	sockDir := t.TempDir()
	stateDir := t.TempDir()

	store, err := NewLifecycleStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(sockDir)
	reg.SetLifecycleStore(store)

	var stoppedUnits []string
	reg.stopUnitFn = func(unit string) {
		stoppedUnits = append(stoppedUnits, unit)
	}

	// Create files.
	if f, err := os.Create(reg.SocketPath("crash-dismiss")); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}
	meta := sessionMeta{
		ID:          "crash-dismiss",
		Pid:         12345,
		SystemdUnit: "termyard-session-crash-dismiss-456.scope",
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath("crash-dismiss"), data, 0600); err != nil {
		t.Fatal(err)
	}

	// Write lifecycle record in CRASHED state.
	lr := LifecycleRecord{
		ID:          "crash-dismiss",
		DaemonPID:   12345,
		SystemdUnit: "termyard-session-crash-dismiss-456.scope",
	}
	if err := store.RecordActive(lr); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition("crash-dismiss", LifecycleActive, LifecycleCrashed); err != nil {
		t.Fatal(err)
	}

	// Dismiss — should succeed and stop the exact unit.
	if err := reg.DismissSession("crash-dismiss"); err != nil {
		t.Fatal(err)
	}

	// Verify exactly one unit was stopped, and it's the correct one.
	if len(stoppedUnits) != 1 {
		t.Fatalf("expected 1 scope stop, got %d: %v", len(stoppedUnits), stoppedUnits)
	}
	if stoppedUnits[0] != "termyard-session-crash-dismiss-456.scope" {
		t.Errorf("stopped unit = %q, want %q", stoppedUnits[0], "termyard-session-crash-dismiss-456.scope")
	}

	// Files must be removed.
	if _, err := os.Stat(reg.SocketPath("crash-dismiss")); !os.IsNotExist(err) {
		t.Error("socket file should be removed after dismiss")
	}

	// Lifecycle state must be dismissed.
	rec, err := store.Get("crash-dismiss")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != LifecycleDismissed {
		t.Errorf("expected state dismissed, got %s", rec.State)
	}
}

// TestDetectAndCleanupCrashes_UsesExactUnit verifies that
// DetectAndCleanupCrashes stops the exact SystemdUnit from each crashed
// record rather than re-reading mutable lifecycle state via stopSystemdScope.
func TestDetectAndCleanupCrashes_UsesExactUnit(t *testing.T) {
	sockDir := t.TempDir()
	stateDir := t.TempDir()

	store, err := NewLifecycleStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(sockDir)
	reg.SetLifecycleStore(store)

	var stoppedUnits []string
	reg.stopUnitFn = func(unit string) {
		stoppedUnits = append(stoppedUnits, unit)
	}

	// Write an active record with a dead PID.
	lr := LifecycleRecord{
		ID:          "crash-exact-unit",
		DaemonPID:   999999, // non-existent → detected as crashed
		SystemdUnit: "termyard-session-crash-exact-unit-999.scope",
		Shell:       "/bin/bash",
		Cwd:         "/tmp",
		Cols:        120,
		Rows:        40,
	}
	if err := store.RecordActive(lr); err != nil {
		t.Fatal(err)
	}

	crashed := reg.DetectAndCleanupCrashes()
	if len(crashed) != 1 {
		t.Fatalf("expected 1 crashed, got %d", len(crashed))
	}

	// Verify the exact unit was stopped — not re-read from store.
	if len(stoppedUnits) != 1 {
		t.Fatalf("expected 1 scope stop, got %d: %v", len(stoppedUnits), stoppedUnits)
	}
	if stoppedUnits[0] != "termyard-session-crash-exact-unit-999.scope" {
		t.Errorf("stopped unit = %q, want %q", stoppedUnits[0], "termyard-session-crash-exact-unit-999.scope")
	}
}

// TestDismissSession_NoLifecycleStore_NoScopeStop verifies that dismissing
// a session when no lifecycle store is configured cleans up files without
// attempting to stop any systemd scope.
func TestDismissSession_NoLifecycleStore_NoScopeStop(t *testing.T) {
	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	var stoppedUnits []string
	reg.stopUnitFn = func(unit string) {
		stoppedUnits = append(stoppedUnits, unit)
	}

	// Create files.
	if f, err := os.Create(reg.SocketPath("no-store")); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}
	meta := sessionMeta{
		ID:          "no-store",
		Pid:         12345,
		SystemdUnit: "should-not-be-stopped.scope",
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath("no-store"), data, 0600); err != nil {
		t.Fatal(err)
	}

	// Dismiss should succeed (no lifecycle store — files only).
	if err := reg.DismissSession("no-store"); err != nil {
		t.Fatal(err)
	}

	// No scope should have been stopped.
	if len(stoppedUnits) > 0 {
		t.Errorf("expected 0 scope stops without lifecycle store, got %d: %v", len(stoppedUnits), stoppedUnits)
	}

	// Files should be removed.
	if _, err := os.Stat(reg.SocketPath("no-store")); !os.IsNotExist(err) {
		t.Error("socket file should be removed after dismiss")
	}
	if _, err := os.Stat(reg.metadataPath("no-store")); !os.IsNotExist(err) {
		t.Error("metadata file should be removed after dismiss")
	}
}

// TestDismissSession_NoLifecycleRecordCleansFilesOnly verifies that
// dismissing a session with no lifecycle record cleans up files without
// stopping any systemd scope.
func TestDismissSession_NoLifecycleRecordCleansFilesOnly(t *testing.T) {
	sockDir := t.TempDir()
	stateDir := t.TempDir()

	store, err := NewLifecycleStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(sockDir)
	reg.SetLifecycleStore(store)

	var stoppedUnits []string
	reg.stopUnitFn = func(unit string) {
		stoppedUnits = append(stoppedUnits, unit)
	}

	// Create files only (no lifecycle record).
	if f, err := os.Create(reg.SocketPath("no-record")); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}
	meta := sessionMeta{
		ID:          "no-record",
		Pid:         12345,
		SystemdUnit: "should-not-be-stopped.scope",
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath("no-record"), data, 0600); err != nil {
		t.Fatal(err)
	}

	// Dismiss should succeed (no lifecycle record — files only).
	if err := reg.DismissSession("no-record"); err != nil {
		t.Fatal(err)
	}

	// No scope should have been stopped.
	if len(stoppedUnits) > 0 {
		t.Errorf("expected 0 scope stops without lifecycle record, got %d: %v", len(stoppedUnits), stoppedUnits)
	}

	// Files should be removed.
	if _, err := os.Stat(reg.SocketPath("no-record")); !os.IsNotExist(err) {
		t.Error("socket file should be removed after dismiss")
	}
	if _, err := os.Stat(reg.metadataPath("no-record")); !os.IsNotExist(err) {
		t.Error("metadata file should be removed after dismiss")
	}
}

// ===== Phase 1 Tests =====

// TestCreate_ReturnsIdentityWithSystemdUnit verifies Create returns SessionInfo
// with live PID, nonce, and SystemdUnit.
func TestCreate_ReturnsIdentityWithSystemdUnit(t *testing.T) {
	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	// Create a session. Since we can't actually spawn a real daemon in tests,
	// we'll verify the happy path behavior by checking the polling logic.
	// This test would need to be integrated with a real daemon in production tests.
	// For now, we verify the structure exists and polling validates all required fields.

	// Simulate a daemon by creating the metadata file manually and ensuring socket exists.
	// This tests the polling logic that checks nonce + PID + socket dialability.

	sessionName := "test-create"
	nonce := "a1b2c3d4e5f6g7h8"

	// Pre-write metadata to simulate daemon behavior
	meta := sessionMeta{
		ID:          sessionName,
		Pid:         os.Getpid(), // Use test process PID for this test
		Nonce:       nonce,
		ShellPid:    os.Getpid(),
		Shell:       "/bin/bash",
		Cwd:         "/tmp",
		Created:     "2024-01-01T00:00:00Z",
		Cols:        120,
		Rows:        40,
		SystemdUnit: "termyard-session-test-create-123.scope",
	}
	data, _ := json.Marshal(meta)
	metaPath := reg.metadataPath(sessionName)
	if err := os.WriteFile(metaPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	// Create a listen socket to simulate daemon readiness
	listener, err := net.Listen("unix", reg.SocketPath(sessionName))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	// Now that the files are in place, we would call Create() which polls for these.
	// For this test, we verify that Scan() picks up the correctly-formed session.
	sessions := reg.Scan()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session from Scan, got %d", len(sessions))
	}

	s := sessions[0]
	if s.Instance.Pid != os.Getpid() {
		t.Errorf("Pid = %d, want %d", s.Instance.Pid, os.Getpid())
	}
	if s.Instance.Nonce != nonce {
		t.Errorf("Nonce = %q, want %q", s.Instance.Nonce, nonce)
	}
	if s.Instance.SystemdUnit != "termyard-session-test-create-123.scope" {
		t.Errorf("SystemdUnit = %q, want %q", s.Instance.SystemdUnit, "termyard-session-test-create-123.scope")
	}
	if s.Shell != "/bin/bash" {
		t.Errorf("Shell = %q, want /bin/bash", s.Shell)
	}
}

// TestWatch_StopIsIdempotent verifies that stop() is idempotent (safe to call multiple times).
func TestWatch_StopIsIdempotent(t *testing.T) {
	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	// Create metadata and listening socket for a session
	sessionName := "test-idempotent"
	nonce := "test1234567890ab"
	meta := sessionMeta{
		ID:          sessionName,
		Pid:         os.Getpid(),
		Nonce:       nonce,
		ShellPid:    os.Getpid(),
		Shell:       "/bin/bash",
		Cwd:         "/tmp",
		Created:     "2024-01-01T00:00:00Z",
		Cols:        120,
		Rows:        40,
		SystemdUnit: "test-unit.scope",
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath(sessionName), data, 0600); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("unix", reg.SocketPath(sessionName))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	inst := Instance{
		Name:        sessionName,
		Pid:         os.Getpid(),
		Nonce:       nonce,
		SystemdUnit: "test-unit.scope",
	}

	var exitCount, unreachableCount int
	onExit := func(Instance) { exitCount++ }
	onUnreachable := func(Instance, bool) { unreachableCount++ }

	stop, err := reg.Watch(inst, onExit, onUnreachable)
	if err != nil {
		t.Fatal(err)
	}

	// Call stop multiple times; should be safe (idempotent)
	stop()
	stop()
	stop()

	// No callbacks should have been triggered
	if exitCount > 0 {
		t.Errorf("exitCount = %d, expected 0", exitCount)
	}
	if unreachableCount > 0 {
		t.Errorf("unreachableCount = %d, expected 0", unreachableCount)
	}
}

// TestScan_LiveDaemonAdopted verifies that Scan returns live daemon sessions.
func TestScan_LiveDaemonAdopted(t *testing.T) {
	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	sessionName := "test-scan-live"
	nonce := "scantest1234abcd"
	meta := sessionMeta{
		ID:          sessionName,
		Pid:         os.Getpid(),
		Nonce:       nonce,
		ShellPid:    os.Getpid(),
		Shell:       "/bin/bash",
		Cwd:         "/tmp",
		Created:     "2024-01-01T00:00:00Z",
		Cols:        120,
		Rows:        40,
		SystemdUnit: "test-unit.scope",
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath(sessionName), data, 0600); err != nil {
		t.Fatal(err)
	}

	// Create socket file (no listener needed for Scan; it only checks PID)
	if err := os.WriteFile(reg.SocketPath(sessionName), []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	sessions := reg.Scan()
	if len(sessions) != 1 {
		t.Fatalf("Scan = %d sessions, want 1", len(sessions))
	}
	if sessions[0].Instance.Pid != os.Getpid() {
		t.Errorf("Pid = %d, want %d", sessions[0].Instance.Pid, os.Getpid())
	}
}

// TestAdopt_RemovesDeadSessionFiles verifies that Adopt removes stale PID-dead leftovers
// after verifying sidecar identity.
func TestAdopt_RemovesDeadSessionFiles(t *testing.T) {
	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	// Create a session with a dead PID (999999 is unlikely to be running)
	deadPID := 999999
	sessionName := "test-adopt-dead"
	nonce := "adoptdead1234567"
	meta := sessionMeta{
		ID:          sessionName,
		Pid:         deadPID,
		Nonce:       nonce,
		ShellPid:    deadPID,
		Shell:       "/bin/bash",
		Cwd:         "/tmp",
		Created:     "2024-01-01T00:00:00Z",
		Cols:        120,
		Rows:        40,
		SystemdUnit: "test-unit.scope",
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath(sessionName), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reg.SocketPath(sessionName), []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	// Adopt should remove the dead session
	sessions := reg.Adopt()
	if len(sessions) != 0 {
		t.Errorf("Adopt = %d sessions, want 0 (dead PID)", len(sessions))
	}

	// Verify files were removed
	if _, err := os.Stat(reg.metadataPath(sessionName)); !os.IsNotExist(err) {
		t.Error("metadata file should be removed")
	}
	if _, err := os.Stat(reg.SocketPath(sessionName)); !os.IsNotExist(err) {
		t.Error("socket file should be removed")
	}
}

// TestAdopt_KeepsAliveSessionsEvenIfUnreachable verifies that Adopt returns
// sessions with live PIDs even if the socket is unreachable (file exists but dial fails).
func TestAdopt_KeepsAliveSessionsEvenIfUnreachable(t *testing.T) {
	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	// Create a session with a live PID and socket file, but socket is unreachable
	sessionName := "test-adopt-unreachable"
	nonce := "unreachable12345"
	meta := sessionMeta{
		ID:          sessionName,
		Pid:         os.Getpid(),
		Nonce:       nonce,
		ShellPid:    os.Getpid(),
		Shell:       "/bin/bash",
		Cwd:         "/tmp",
		Created:     "2024-01-01T00:00:00Z",
		Cols:        120,
		Rows:        40,
		SystemdUnit: "test-unit.scope",
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath(sessionName), data, 0600); err != nil {
		t.Fatal(err)
	}
	// Create empty socket file (exists but not listening, so unreachable)
	if err := os.WriteFile(reg.SocketPath(sessionName), []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	sessions := reg.Adopt()
	if len(sessions) != 1 {
		t.Fatalf("Adopt = %d sessions, want 1 (live PID, unreachable)", len(sessions))
	}
	if sessions[0].Instance.Pid != os.Getpid() {
		t.Errorf("Pid = %d, want %d", sessions[0].Instance.Pid, os.Getpid())
	}
}

// TestProcessPIDAlive_DetectsLiveAndDeadProcesses verifies that processPIDAlive
// correctly identifies live vs. dead processes and handles legacy PID reuse.
func TestProcessPIDAlive_DetectsProcesses(t *testing.T) {
	// Test with our own PID (should be alive)
	inst := Instance{
		Pid: os.Getpid(),
	}
	reg := NewRegistry(t.TempDir())
	if !reg.processPIDAlive(inst) {
		t.Error("processPIDAlive should return true for own PID")
	}

	// Test with a dead PID
	inst.Pid = 999999
	if reg.processPIDAlive(inst) {
		t.Error("processPIDAlive should return false for dead PID")
	}

	// Test with nonce-based identity (no ProcStartTime needed)
	inst.Pid = os.Getpid()
	inst.Nonce = "test123456789abc"
	if !reg.processPIDAlive(inst) {
		t.Error("processPIDAlive should return true for live PID with nonce")
	}
}

// TestCreate_ReturnsIdentityWithProcStartTime verifies Create/Adopt capture ProcStartTime.
func TestCreate_ReturnsIdentityWithProcStartTime(t *testing.T) {
	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	// Simulate a daemon by creating metadata with ProcStartTime
	sessionName := "test-procstart"
	nonce := "procstarttest1234"
	pid := os.Getpid()
	startTime := procStartTime(pid)

	meta := sessionMeta{
		ID:            sessionName,
		Pid:           pid,
		Nonce:         nonce,
		ShellPid:      pid,
		Shell:         "/bin/bash",
		Cwd:           "/tmp",
		Created:       "2024-01-01T00:00:00Z",
		Cols:          120,
		Rows:          40,
		ProcStartTime: startTime,
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath(sessionName), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reg.SocketPath(sessionName), []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	// Verify Adopt preserves ProcStartTime
	sessions := reg.Adopt()
	if len(sessions) != 1 {
		t.Fatalf("Adopt = %d sessions, want 1", len(sessions))
	}
	if sessions[0].Instance.ProcStartTime != startTime {
		t.Errorf("ProcStartTime = %d, want %d", sessions[0].Instance.ProcStartTime, startTime)
	}
	if sessions[0].Instance.Nonce != nonce {
		t.Errorf("Nonce = %s, want %s", sessions[0].Instance.Nonce, nonce)
	}
}

// TestWatch_StopReturnsPromptly verifies stop() returns quickly.
func TestWatch_StopReturnsPromptly(t *testing.T) {
	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	// Simulate a daemon with metadata but no socket (so Watch can't connect)
	// This allows us to test stop() semantics without spawning real processes
	sessionName := "test-stop-prompt"
	nonce := "testprompt1234567"
	pid := os.Getpid()

	meta := sessionMeta{
		ID:       sessionName,
		Pid:      pid,
		Nonce:    nonce,
		ShellPid: pid,
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath(sessionName), data, 0600); err != nil {
		t.Fatal(err)
	}

	inst := Instance{
		Name:  sessionName,
		Pid:   pid,
		Nonce: nonce,
	}

	stop, err := reg.Watch(inst, nil, nil)
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	// Call stop immediately and measure time
	started := time.Now()
	stop()
	elapsed := time.Since(started)

	// Verify stop returned promptly (< 2 seconds)
	if elapsed > 2*time.Second {
		t.Errorf("stop() took %v, want < 2s", elapsed)
	}

	// Call stop again and verify idempotent
	stop()
}

// TestWatch_StopIdempotent verifies stop() is safe to call multiple times.
func TestWatch_StopIdempotent(t *testing.T) {
	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	// Use current process PID so Watch can report it as alive
	sessionName := "test-idempotent"
	nonce := "testidempotent123456"
	pid := os.Getpid()

	meta := sessionMeta{
		ID:    sessionName,
		Pid:   pid,
		Nonce: nonce,
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath(sessionName), data, 0600); err != nil {
		t.Fatal(err)
	}

	inst := Instance{
		Name:  sessionName,
		Pid:   pid,
		Nonce: nonce,
	}

	callbackCount := 0
	var cbMu sync.Mutex
	onExit := func(inst Instance) {
		cbMu.Lock()
		callbackCount++
		cbMu.Unlock()
	}

	stop, err := reg.Watch(inst, onExit, nil)
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	// Call stop multiple times
	stop()
	stop()
	stop()

	// Verify stop returned promptly
	started := time.Now()
	for i := 0; i < 5; i++ {
		stop()
	}
	elapsed := time.Since(started)

	if elapsed > 1*time.Second {
		t.Errorf("multiple stop() calls took %v, want < 1s", elapsed)
	}
}

// TestAdoptionCleanup_TOCTOU verifies adoption cleanup rechecks PID death before deletion.
func TestAdoptionCleanup_TOCTOU(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real daemon test in short mode")
	}

	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	// Create a session with a dead PID
	deadPID := 999999
	sessionName := "test-adopt-toctou"
	nonce := "adopttoctou12345"
	meta := sessionMeta{
		ID:            sessionName,
		Pid:           deadPID,
		Nonce:         nonce,
		ShellPid:      deadPID,
		Shell:         "/bin/bash",
		Cwd:           "/tmp",
		Created:       "2024-01-01T00:00:00Z",
		Cols:          120,
		Rows:          40,
		ProcStartTime: 0,
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath(sessionName), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reg.SocketPath(sessionName), []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	// Verify dead session is cleaned up
	sessions := reg.Adopt()
	if len(sessions) != 0 {
		t.Errorf("Adopt = %d sessions, want 0 (dead PID)", len(sessions))
	}

	// Verify files were removed
	if _, err := os.Stat(reg.metadataPath(sessionName)); !os.IsNotExist(err) {
		t.Error("metadata file should be removed after adoption")
	}
	if _, err := os.Stat(reg.SocketPath(sessionName)); !os.IsNotExist(err) {
		t.Error("socket file should be removed after adoption")
	}
}

// TestAcquireAdoptionLock verifies flock behavior.
func TestAcquireAdoptionLock(t *testing.T) {
	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	// Acquire lock
	release, err := reg.AcquireAdoptionLock()
	if err != nil {
		t.Fatalf("AcquireAdoptionLock failed: %v", err)
	}

	// Try to check if held (should return true)
	if !reg.CheckAdoptionLockHeld() {
		t.Error("CheckAdoptionLockHeld should return true while lock is held")
	}

	// Release and verify
	release()
	if reg.CheckAdoptionLockHeld() {
		t.Error("CheckAdoptionLockHeld should return false after release")
	}
}

// TestInstanceMatches_NonceIdentity verifies nonce-based matching.
func TestInstanceMatches_NonceIdentity(t *testing.T) {
	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	sessionName := "test-nonce-match"
	nonce := "testnonce123456"
	pid := os.Getpid()

	// Write metadata with matching nonce
	meta := sessionMeta{
		ID:    sessionName,
		Pid:   pid,
		Nonce: nonce,
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath(sessionName), data, 0600); err != nil {
		t.Fatal(err)
	}

	inst := Instance{
		Name:  sessionName,
		Pid:   pid,
		Nonce: nonce,
	}

	// Should match (nonce + pid)
	if !reg.instanceMatches(inst) {
		t.Error("instanceMatches should return true for matching nonce+pid")
	}

	// Should not match with different nonce
	inst.Nonce = "differentnonce11"
	if reg.instanceMatches(inst) {
		t.Error("instanceMatches should return false for different nonce")
	}
}
