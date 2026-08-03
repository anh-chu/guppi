package pty

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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

// TestLifecycleStore_DoesNotPolluteDefaultStateDir is a regression guard:
// benchmark and comparison tests must write lifecycle records only into their
// own isolated directories, never into the user's real state directory.
func TestLifecycleStore_DoesNotPolluteDefaultStateDir(t *testing.T) {
	realDefault := DefaultStateDir()

	controlled := t.TempDir()
	t.Setenv("XDG_STATE_HOME", controlled)

	isolated := t.TempDir()
	reg := NewRegistry(t.TempDir())
	store, err := NewLifecycleStore(isolated)
	if err != nil {
		t.Fatal(err)
	}
	reg.SetLifecycleStore(store)

	const id = "lifecycle-isolation-probe"
	if err := store.RecordActive(LifecycleRecord{ID: id, DaemonPID: 999999}); err != nil {
		t.Fatal(err)
	}

	// The record must land only in the explicitly-provided state directory.
	if _, err := os.Stat(filepath.Join(isolated, id+".lifecycle.json")); err != nil {
		t.Fatalf("expected record in isolated dir: %v", err)
	}

	// It must NOT appear in the real default state directory.
	entries, err := os.ReadDir(realDefault)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("failed to read real default dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == id+".lifecycle.json" {
			t.Fatalf("lifecycle probe leaked into real state dir %s", realDefault)
		}
	}
}

func TestGenerationFor(t *testing.T) {
	dir := t.TempDir()
	store, err := NewLifecycleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(t.TempDir())
	reg.SetLifecycleStore(store)

	// No record -> empty.
	if got := reg.GenerationFor("missing"); got != "" {
		t.Fatalf("GenerationFor(missing) = %q, want empty", got)
	}

	// Lifecycle record generation wins.
	if err := store.RecordActive(LifecycleRecord{
		ID:         "s1",
		State:      LifecycleActive,
		DaemonPID:  12345,
		Generation: "gen-live",
	}); err != nil {
		t.Fatal(err)
	}
	if got := reg.GenerationFor("s1"); got != "gen-live" {
		t.Fatalf("GenerationFor(s1) = %q, want gen-live", got)
	}
}
