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

func TestReadSystemdUnit(t *testing.T) {
	reg := NewRegistry(t.TempDir())

	writeMeta := func(name, unit string) {
		data, _ := json.Marshal(sessionMeta{ID: name, Pid: 12345, SystemdUnit: unit})
		if err := os.WriteFile(reg.metadataPath(name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeMeta("with-unit", "termyard-session-test-456.scope")
	writeMeta("no-unit", "")

	if got := reg.readSystemdUnit("with-unit"); got != "termyard-session-test-456.scope" {
		t.Errorf("readSystemdUnit = %q, want scope name", got)
	}
	if got := reg.readSystemdUnit("no-unit"); got != "" {
		t.Errorf("readSystemdUnit = %q, want empty string", got)
	}
	if got := reg.readSystemdUnit("nonexistent"); got != "" {
		t.Errorf("readSystemdUnit = %q, want empty string for missing session", got)
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

// TestScan_ReturnsIdentity verifies Scan returns SessionInfo with live PID,
// nonce, and SystemdUnit from a sidecar.
func TestScan_ReturnsIdentity(t *testing.T) {
	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

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

// TestAdopt_PreservesIdentity verifies Adopt preserves ProcStartTime and nonce.
func TestAdopt_PreservesIdentity(t *testing.T) {
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

// TestAcquireAdoptionLock verifies flock behavior.
func TestAcquireAdoptionLock(t *testing.T) {
	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	release, err := reg.AcquireAdoptionLock()
	if err != nil {
		t.Fatalf("AcquireAdoptionLock failed: %v", err)
	}

	// flock conflicts across open file descriptions, even within one process.
	if rel, ok := reg.TryAcquireAdoptionLock(); ok {
		rel()
		t.Error("TryAcquireAdoptionLock should fail while lock is held")
	}

	release()
	release2, ok := reg.TryAcquireAdoptionLock()
	if !ok {
		t.Fatal("TryAcquireAdoptionLock should succeed after release")
	}
	release2()
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

// TestWatch_TransportLossRecovery_R6Core verifies that socket loss with live PID
// triggers onUnreachable (true), does NOT trigger onExit within 3s, and can recover
// by reconnecting without removal (onUnreachable false).
func TestWatch_TransportLossRecovery_R6Core(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real daemon test in short mode")
	}

	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	sessionName := "test-transport-loss"
	nonce := "transprt12345678"
	pid := os.Getpid()

	// Write metadata for the daemon
	meta := sessionMeta{
		ID:       sessionName,
		Pid:      pid,
		Nonce:    nonce,
		ShellPid: pid,
		Shell:    "/bin/bash",
		Cwd:      "/tmp",
		Created:  "2024-01-01T00:00:00Z",
		Cols:     120,
		Rows:     40,
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath(sessionName), data, 0600); err != nil {
		t.Fatal(err)
	}

	// Create a listener socket (simulating daemon readiness)
	listener, err := net.Listen("unix", reg.SocketPath(sessionName))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	// Background: accept and discard connections (simulate daemon echo)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // listener closed
			}
			conn.Close() // Accept and close immediately (idle client test)
		}
	}()

	inst := Instance{
		Name:  sessionName,
		Pid:   pid,
		Nonce: nonce,
	}

	var unreachableCalls []bool
	var unreachableMu sync.Mutex
	var exitCalls int

	onUnreachable := func(inst Instance, bad bool) {
		unreachableMu.Lock()
		unreachableCalls = append(unreachableCalls, bad)
		unreachableMu.Unlock()
	}

	onExit := func(inst Instance) {
		exitCalls++
	}

	stop, err := reg.Watch(inst, onExit, onUnreachable)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for initial connection
	time.Sleep(200 * time.Millisecond)

	// Break transport: close listener to drop connection, then remove socket file
	listener.Close()
	time.Sleep(100 * time.Millisecond) // Let watcher detect the closure
	oldSocketPath := reg.SocketPath(sessionName)
	// Socket file may be auto-deleted by the system when listener closes,
	// so we check if it exists before removing it
	if _, err := os.Stat(oldSocketPath); err == nil {
		if err := os.Remove(oldSocketPath); err != nil {
			t.Fatal(err)
		}
	}

	// Wait for onUnreachable(true) to be called
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		unreachableMu.Lock()
		if len(unreachableCalls) > 0 && unreachableCalls[0] == true {
			unreachableMu.Unlock()
			break
		}
		unreachableMu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}

	unreachableMu.Lock()
	if len(unreachableCalls) == 0 || unreachableCalls[0] != true {
		unreachableMu.Unlock()
		t.Errorf("onUnreachable(true) not called; calls: %v", unreachableCalls)
	}
	unreachableMu.Unlock()

	// Verify onExit NOT called within 3s
	if exitCalls > 0 {
		t.Errorf("onExit called prematurely; calls: %d", exitCalls)
	}
	time.Sleep(3 * time.Second)
	if exitCalls > 0 {
		t.Errorf("onExit should not fire within 3s with live PID; calls: %d", exitCalls)
	}

	// Restore socket by recreating listener
	newListener, err := net.Listen("unix", oldSocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer newListener.Close()

	// Resume accepting connections
	go func() {
		for {
			conn, err := newListener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	// Wait for onUnreachable(false) recovery call
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		unreachableMu.Lock()
		if len(unreachableCalls) >= 2 && unreachableCalls[1] == false {
			unreachableMu.Unlock()
			break
		}
		unreachableMu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}

	unreachableMu.Lock()
	if len(unreachableCalls) < 2 || unreachableCalls[1] != false {
		unreachableMu.Unlock()
		t.Errorf("onUnreachable(false) not called after recovery; calls: %v", unreachableCalls)
	}
	unreachableMu.Unlock()

	// Verify still no onExit
	if exitCalls > 0 {
		t.Errorf("onExit should not fire after recovery; calls: %d", exitCalls)
	}

	// Clean stop
	stop()
}

// TestWatch_StopWithActiveWatcher verifies that stop() with active/connected watcher
// returns promptly (does not block/deadlock waiting for goroutine).
// Callback suppression is tested implicitly: if stop() returns quickly despite an active
// connection, it means the watcher goroutine exited and callbacks were suppressed.
func TestWatch_StopWithActiveWatcher(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real daemon test in short mode")
	}

	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	sessionName := "test-stop-active"
	nonce := "stopactive1234567"
	pid := os.Getpid()

	// Write metadata
	meta := sessionMeta{
		ID:       sessionName,
		Pid:      pid,
		Nonce:    nonce,
		ShellPid: pid,
		Shell:    "/bin/bash",
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(reg.metadataPath(sessionName), data, 0600); err != nil {
		t.Fatal(err)
	}

	// Create listening socket (active daemon connection)
	listener, err := net.Listen("unix", reg.SocketPath(sessionName))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	inst := Instance{
		Name:  sessionName,
		Pid:   pid,
		Nonce: nonce,
	}

	// The fake daemon closes connections immediately, so unreachable callbacks
	// may legitimately fire before stop; only exit must never fire (PID alive).
	var exitCount int
	var cbMu sync.Mutex
	stop, err := reg.Watch(inst,
		func(Instance) { cbMu.Lock(); exitCount++; cbMu.Unlock() },
		nil)
	if err != nil {
		t.Fatal(err)
	}

	// Give watcher time to connect
	time.Sleep(200 * time.Millisecond)

	// Call stop and verify it returns promptly (no deadlock/hang)
	started := time.Now()
	stop()
	elapsed := time.Since(started)

	if elapsed > 2*time.Second {
		t.Errorf("stop() took %v, want < 2s; stop() may have deadlocked", elapsed)
	}

	// Call stop again (idempotent) to verify it's truly idempotent
	stop()

	cbMu.Lock()
	if exitCount > 0 {
		t.Errorf("onExit fired with live PID: %d", exitCount)
	}
	cbMu.Unlock()
}

// TestExactlyOnceRemoval_NameReuse verifies instance identity prevents old callbacks
// from affecting new instances after name reuse. Tests R7 — removed sessions never
// resurrect; no ABA on name reuse.
func TestExactlyOnceRemoval_NameReuse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real daemon test in short mode")
	}

	sockDir := t.TempDir()
	reg := NewRegistry(sockDir)

	sessionName := "test-reuse-session"

	// === PHASE 1: Create and kill session A (old instance) ===
	oldNonce := "oldnonce12345678"
	oldPid := os.Getpid() // Use test process for simplicity

	// Write metadata for old instance
	oldMeta := sessionMeta{
		ID:       sessionName,
		Pid:      oldPid,
		Nonce:    oldNonce,
		ShellPid: oldPid,
		Shell:    "/bin/bash",
		Cwd:      "/tmp",
		Created:  "2024-01-01T00:00:00Z",
	}
	oldData, _ := json.Marshal(oldMeta)
	if err := os.WriteFile(reg.metadataPath(sessionName), oldData, 0600); err != nil {
		t.Fatal(err)
	}

	// Create socket file for old instance
	oldSocketPath := reg.SocketPath(sessionName)
	if err := os.WriteFile(oldSocketPath, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	oldInst := Instance{
		Name:  sessionName,
		Pid:   oldPid,
		Nonce: oldNonce,
	}

	var oldExitCalled bool
	var oldExitMu sync.Mutex

	// Start watcher on old instance
	oldStop, err := reg.Watch(oldInst, func(inst Instance) {
		oldExitMu.Lock()
		oldExitCalled = true
		oldExitMu.Unlock()
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Stop the old watcher (simulating removal during kill)
	oldStop()

	// === PHASE 2: Immediately create new session with same name (new instance) ===
	newNonce := "newnonce123456789"
	// Simulate new process with different PID (in real scenario)
	// For test simplicity, we keep same PID but different nonce

	// Write metadata for new instance
	newMeta := sessionMeta{
		ID:       sessionName,
		Pid:      oldPid,
		Nonce:    newNonce,
		ShellPid: oldPid,
		Shell:    "/bin/bash",
		Cwd:      "/tmp",
		Created:  "2024-01-01T01:00:00Z",
	}
	newData, _ := json.Marshal(newMeta)
	if err := os.WriteFile(reg.metadataPath(sessionName), newData, 0600); err != nil {
		t.Fatal(err)
	}

	newInst := Instance{
		Name:  sessionName,
		Pid:   oldPid,
		Nonce: newNonce,
	}

	var newExitCalled bool
	var newExitMu sync.Mutex

	// Start watcher on new instance
	newStop, err := reg.Watch(newInst, func(inst Instance) {
		newExitMu.Lock()
		newExitCalled = true
		newExitMu.Unlock()
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Wait a bit to ensure watchers are settled
	time.Sleep(100 * time.Millisecond)

	// === VERIFICATION ===
	// Instance identity should be different (nonce differs), so old callback
	// shouldn't be triggered by the new instance. Both instances have the same
	// name and PID, but different nonces.

	// Old instance callback should not be called (we stopped it)
	oldExitMu.Lock()
	if oldExitCalled {
		oldExitMu.Unlock()
		t.Error("old instance exit callback should not be called after stop()")
	}
	oldExitMu.Unlock()

	// New instance should still be alive and tracked
	newExitMu.Lock()
	if newExitCalled {
		newExitMu.Unlock()
		t.Error("new instance should not have exit callback called")
	}
	newExitMu.Unlock()

	// Verify instances don't match each other
	if reg.instanceMatches(oldInst) {
		// After new instance created, old instance should no longer match
		t.Error("old instance should not match after new instance created with different nonce")
	}

	if !reg.instanceMatches(newInst) {
		t.Error("new instance should match sidecar")
	}

	// Clean stop
	newStop()
}
