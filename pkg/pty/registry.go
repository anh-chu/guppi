package pty

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
)

// ansiRe matches ANSI escape sequences (CSI, OSC, and simple escapes).
var ansiRe = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[a-zA-Z]|\][^\x1b\x07]*(?:\x07|\x1b\\)|[()][AB012]|\[\?[0-9;]*[hl]|=|>|\x1b)`)

// ctrlRe matches carriage returns and other non-newline control chars.
var ctrlRe = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f]`)

// Instance uniquely identifies a session daemon by process and nonce.
// Identity match uses: nonce != "" ? (Pid+Nonce) : (Pid+ProcStartTime).
type Instance struct {
	Name          string // session name
	Pid           int    // daemon PID from sidecar JSON / Create
	Nonce         string // 8-byte hex random written by daemon at spawn; "" for legacy
	ProcStartTime int64  // fallback for legacy daemons: /proc/<pid>/stat field 22
	SystemdUnit   string // systemd scope name, immutable from spawn/adoption
}

// SessionInfo holds metadata about a running session daemon.
type SessionInfo struct {
	Instance // embedded for easy identity access
	ID       string
	Pid      int // for backward compat with existing SessionInfo.Pid
	ShellPid int
	Shell    string
	Cwd      string
	Created  string // RFC3339
	Cols     uint16
	Rows     uint16
	Socket   string // full path to .sock file
}

// generateNonce generates a random 8-byte hex nonce for instance identity.
func generateNonce() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// validSessionID returns true if id is safe for use in file paths.
// Rejects empty, contains path separators, dots-only, or control chars.
func validSessionID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, c := range id {
		if c == '/' || c == '\\' || c == '\x00' {
			return false
		}
	}
	return true
}

// Registry manages session daemon lifecycle: create, list, kill, capture.
type Registry struct {
	dir       string // socket directory
	failMu    sync.Mutex
	failCount map[string]int // consecutive liveness failures per session

	recoveryMu     sync.Mutex      // serializes recover/dismiss operations
	lifecycleStore *LifecycleStore // durable lifecycle state (may be nil)

	// stopUnitFn is a test hook that, when non-nil, is called instead of
	// spawning a real systemctl process. The unit argument is the systemd
	// scope unit name to stop.
	stopUnitFn func(unit string)
}

// NewRegistry creates a session registry using the given socket directory.
// The directory is created with 0700 if it does not exist.
func NewRegistry(dir string) *Registry {
	os.MkdirAll(dir, 0700)
	return &Registry{dir: dir, failCount: make(map[string]int)}
}

// Dir returns the registry's socket directory.
func (r *Registry) Dir() string {
	return r.dir
}

// SetLifecycleStore wires the durable lifecycle store into the registry.
// When set, the registry will differentiate crashes from clean shutdowns
// and persist session metadata for crash recovery.
func (r *Registry) SetLifecycleStore(store *LifecycleStore) {
	r.lifecycleStore = store
}

// LifecycleStore returns the durable lifecycle store, or nil if not set.
func (r *Registry) LifecycleStore() *LifecycleStore {
	return r.lifecycleStore
}

// SocketPath returns the full path to a session's Unix socket.
func (r *Registry) SocketPath(name string) string {
	return filepath.Join(r.dir, name+".sock")
}

// metadataPath returns the full path to a session's metadata JSON file.
func (r *Registry) metadataPath(name string) string {
	return filepath.Join(r.dir, name+".json")
}

// Create spawns a session daemon as a fully detached subprocess.
// It waits up to 2s for the sidecar JSON to appear with the nonce (daemon-written),
// then returns populated SessionInfo including nonce and SystemdUnit.
func (r *Registry) Create(name, shell, cwd string, cols, rows uint16) (SessionInfo, error) {
	log := logrus.WithFields(logrus.Fields{
		"component": "registry",
		"name":      name,
		"shell":     shell,
		"cwd":       cwd,
	})

	// Guard against duplicate names — the socket file would be overwritten,
	// causing two terminals to share a single daemon.
	if _, err := os.Stat(r.SocketPath(name)); err == nil {
		return SessionInfo{}, fmt.Errorf("session %q already exists", name)
	}

	// Atomically claim the name via O_CREATE|O_EXCL on a .claim file.
	// This prevents two concurrent CLI spawns from selecting the same name.
	// The claim is released once the daemon writes metadata and is ready.
	claimPath := filepath.Join(r.dir, name+".claim")
	claimFile, err := os.OpenFile(claimPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return SessionInfo{}, fmt.Errorf("session %q creation in progress", name)
		}
		return SessionInfo{}, fmt.Errorf("claim name: %w", err)
	}
	claimFile.Close() // Just holding the file was the point; close immediately.
	// Always remove the claim on exit: on success the sidecar/socket now guard
	// the name; on failure the name must become reusable. A conditional check
	// of the (shadowed) err variable previously leaked claims permanently.
	defer func() { _ = os.Remove(claimPath) }()

	exe, err := os.Executable()
	if err != nil {
		return SessionInfo{}, fmt.Errorf("get executable: %w", err)
	}

	// Derive defaults in-process so the daemon gets explicit values.
	if shell == "" {
		shell = os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/bash"
		}
	}
	if cols == 0 {
		cols = 120
	}
	if rows == 0 {
		rows = 40
	}

	// Generate a unique nonce for instance identity.
	nonce, err := generateNonce()
	if err != nil {
		return SessionInfo{}, fmt.Errorf("generate nonce: %w", err)
	}

	args := []string{
		"session-daemon",
		"--id", name,
		"--shell", shell,
		"--cols", fmt.Sprintf("%d", cols),
		"--rows", fmt.Sprintf("%d", rows),
		"--cwd", cwd,
		"--socket-dir", r.dir,
		"--nonce", nonce,
	}

	// Pass state dir for lifecycle persistence.
	stateDir := DefaultStateDir()
	args = append(args, "--state-dir", stateDir)

	// Try to wrap in a systemd user scope for cgroup isolation.
	// Falls back to direct spawn if systemd-run is unavailable or
	// the user session bus is not reachable.
	var cmd *exec.Cmd
	useSystemd := false
	var systemdUnit string
	if systemdRun, err := exec.LookPath("systemd-run"); err == nil {
		// Verify user session bus is available (systemd-run --user needs it).
		if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
			systemdUnit = fmt.Sprintf("termyard-session-%s-%d.scope", name, time.Now().UnixMilli())
			scopeArgs := []string{
				"--user", "--scope",
				"--unit", systemdUnit,
				"--",
			}
			fullArgs := append(scopeArgs, exe)
			fullArgs = append(fullArgs, args...)
			cmd = exec.Command(systemdRun, fullArgs...)
			useSystemd = true
		}
	}
	if cmd == nil {
		cmd = exec.Command(exe, args...)
	}

	// Append --systemd-unit so the daemon stores it in metadata/lifecycle
	// for later cleanup.
	if systemdUnit != "" {
		cmd.Args = append(cmd.Args, "--systemd-unit", systemdUnit)
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Open /dev/null explicitly so the daemon doesn't inherit parent's
	// fds (which may be pipes that close when the server restarts).
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return SessionInfo{}, fmt.Errorf("open /dev/null: %w", err)
	}
	defer devNull.Close()
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull

	if err := cmd.Start(); err != nil {
		if useSystemd {
			// systemd-run failed; retry with direct spawn.
			log.WithError(err).Warn("systemd-run failed, falling back to direct spawn")
			cmd = exec.Command(exe, args...)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			cmd.Stdin = devNull
			cmd.Stdout = devNull
			cmd.Stderr = devNull
			if err := cmd.Start(); err != nil {
				return SessionInfo{}, fmt.Errorf("start daemon process: %w", err)
			}
			useSystemd = false // Now using direct spawn, not systemd
		} else {
			return SessionInfo{}, fmt.Errorf("start daemon process: %w", err)
		}
	}

	// Release the process handle so the daemon is fully independent.
	// Keep track of the PID to check for exit on readiness timeout.
	originalPid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		log.WithError(err).Warn("failed to release daemon process handle")
	}

	// Poll for the sidecar JSON to be written with the correct nonce, PID alive,
	// and socket dialable (up to 2s). This ensures the daemon has started, written
	// its metadata, and is ready to accept connections.
	var info SessionInfo
	metaPath := r.metadataPath(name)
	socketPath := r.SocketPath(name)
	deadline := time.Now().Add(2 * time.Second)
	var retried bool

readiness_poll:
	for {
		if time.Now().After(deadline) {
			// Readiness timeout. If we used systemd and the process died, retry with direct spawn.
			if useSystemd && !retried {
				// The systemd-run wrapper may be alive but stuck (e.g. bogus
				// DBUS_SESSION_BUS_ADDRESS). Terminate it best-effort and retry
				// with direct spawn regardless of wrapper liveness.
				if proc, _ := os.FindProcess(originalPid); proc != nil {
					_ = proc.Kill()
				}
				log.Warn("systemd-run daemon not ready, retrying with direct spawn")
				devNull2, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
				if err != nil {
					return SessionInfo{}, fmt.Errorf("daemon did not become ready within 2s, and retry failed: open /dev/null: %w", err)
				}
				cmd = exec.Command(exe, args...)
				cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				cmd.Stdin = devNull2
				cmd.Stdout = devNull2
				cmd.Stderr = devNull2
				if err := cmd.Start(); err != nil {
					devNull2.Close()
					return SessionInfo{}, fmt.Errorf("daemon did not become ready within 2s, and direct spawn retry failed: %w", err)
				}
				originalPid = cmd.Process.Pid
				_ = cmd.Process.Release()
				devNull2.Close()
				useSystemd = false
				retried = true
				// Reset deadline for retry.
				deadline = time.Now().Add(2 * time.Second)
				goto readiness_poll
			}
			return SessionInfo{}, fmt.Errorf("daemon did not become ready within 2s")
		}

		data, err := os.ReadFile(metaPath)
		if err != nil {
			// File not yet written; sleep briefly and retry.
			time.Sleep(10 * time.Millisecond)
			continue
		}

		var meta sessionMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Verify nonce matches what we generated (identity stale detection)
		if meta.Nonce != nonce {
			log.WithFields(logrus.Fields{
				"expected_nonce": nonce[:8],
				"got_nonce":      meta.Nonce[:min(8, len(meta.Nonce))],
			}).Debug("waiting for daemon nonce to match")
			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Verify PID is alive
		if meta.Pid <= 0 || !processAlive(meta.Pid) {
			log.Debug("waiting for daemon process to become alive")
			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Verify socket is dialable (daemon bound and listening)
		conn, err := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
		if err != nil {
			log.WithError(err).Debug("waiting for daemon socket to be ready")
			time.Sleep(10 * time.Millisecond)
			continue
		}
		conn.Close() // Immediately close; this was just a probe

		// Daemon is ready. Release the claim (name is now taken by actual daemon).
		_ = os.Remove(claimPath)
		err = nil // Clear error so deferred cleanup won't remove files on success.

		// Build SessionInfo.
		startTime := meta.ProcStartTime
		if startTime <= 0 {
			// Sidecar lacks a start time; capture from /proc so identity is
			// always verifiable.
			startTime = procStartTime(meta.Pid)
		}
		info = SessionInfo{
			Instance: Instance{
				Name:          name,
				Pid:           meta.Pid,
				Nonce:         meta.Nonce,
				ProcStartTime: startTime,
				SystemdUnit:   meta.SystemdUnit,
			},
			ID:       name,
			Pid:      meta.Pid,
			ShellPid: meta.ShellPid,
			Shell:    meta.Shell,
			Cwd:      meta.Cwd,
			Created:  meta.Created,
			Cols:     meta.Cols,
			Rows:     meta.Rows,
			Socket:   socketPath,
		}
		break
	}

	log.WithField("socket", socketPath).Info("session daemon created")
	return info, nil
}

// Watch monitors a daemon instance for liveness and calls callbacks on exit or connectivity issues.
// It dials the socket and holds an idle connection, discarding frames. On read error/EOF:
//   - If PID is dead (matched via inst.Pid and, for legacy, inst.ProcStartTime from /proc/<pid>/stat field 22):
//     call onExit(inst) once.
//   - If PID is alive: call onUnreachable(inst, true), then reconnect with backoff
//     (250ms, 500ms, 1s, 2s, 5s, 5s...). On success, verify sidecar still matches inst.
//     If sidecar mismatch with live PID, keep retrying indefinitely (session unreachable).
//     Only call onExit when PID confirmed dead.
//
// Initial dial retries for up to 2s (daemon startup tolerance).
// stop() suppresses all callbacks and returns after the goroutine has exited (synchronous stop-and-wait).
func (r *Registry) Watch(inst Instance, onExit func(Instance), onUnreachable func(Instance, bool)) (func(), error) {
	log := logrus.WithFields(logrus.Fields{
		"component": "registry",
		"name":      inst.Name,
		"pid":       inst.Pid,
		"nonce":     inst.Nonce[:min(8, len(inst.Nonce))],
	})

	// Channel to signal stop
	stopCh := make(chan struct{})
	// WaitGroup to track goroutine completion
	var wg sync.WaitGroup
	// WaitGroup to track in-flight callbacks
	var cbWg sync.WaitGroup
	// Flag to suppress callbacks after stop is called
	var suppressMu sync.Mutex
	suppress := false
	// Ensure onExit is called at most once
	var exitOnce sync.Once
	// Track active connection for stop to close
	var connMu sync.Mutex
	var activeConn net.Conn
	var stopping bool // guarded by connMu; set by stop() before closing activeConn
	// Make stop idempotent
	var stopOnce sync.Once

	// Helper to call callbacks only if not suppressed.
	// Increments callback WaitGroup before invoking, decrements after.
	// Checks suppression under mutex, releases, THEN invokes callback.
	// This prevents callbacks from being blocked by locks that stop() holds.
	callOnExit := func() {
		suppressMu.Lock()
		if suppress {
			suppressMu.Unlock()
			return
		}
		// Increment callback counter before releasing lock
		cbWg.Add(1)
		suppressMu.Unlock()

		defer cbWg.Done()
		exitOnce.Do(func() {
			if onExit != nil {
				onExit(inst)
			}
		})
	}

	callOnUnreachable := func(bad bool) {
		suppressMu.Lock()
		if suppress {
			suppressMu.Unlock()
			return
		}
		// Increment callback counter before releasing lock
		cbWg.Add(1)
		cb := onUnreachable
		suppressMu.Unlock()

		defer cbWg.Done()
		if cb != nil {
			cb(inst, bad)
		}
	}

	// Watch loop runs in a goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()

		backoffDelays := []time.Duration{
			250 * time.Millisecond,
			500 * time.Millisecond,
			1 * time.Second,
			2 * time.Second,
			5 * time.Second,
			// 5s repeats after the 5th attempt
		}
		backoffIndex := 0

		// Initial dial with retries (up to 2s)
		deadline := time.Now().Add(2 * time.Second)
		var conn net.Conn
		var err error
	TRY_INITIAL_DIAL:
		for {
			select {
			case <-stopCh:
				return
			default:
			}

			if time.Now().After(deadline) {
				// Initial dial failed within 2s; check if PID is alive
				if !r.processPIDAlive(inst) {
					log.Info("initial dial failed and PID dead")
					callOnExit()
					return
				}
				// PID alive but dial failed; session is unreachable
				log.Info("initial dial timeout, entering unreachable loop")
				callOnUnreachable(true)
				break TRY_INITIAL_DIAL
			}

			conn, err = net.DialTimeout("unix", r.SocketPath(inst.Name), 500*time.Millisecond)
			if err == nil {
				log.Debug("initial dial succeeded")
				// Publish under connMu with stop check in the same critical
				// section to close the race with stop().
				connMu.Lock()
				if stopping {
					connMu.Unlock()
					conn.Close()
					return
				}
				activeConn = conn
				connMu.Unlock()
				break TRY_INITIAL_DIAL
			}

			time.Sleep(100 * time.Millisecond)
		}

		// Hold the connection and discard frames until EOF or stop signal
	WATCH_LOOP:
		for {
			if conn != nil {
				// Read frames and discard them
				header := make([]byte, 5)
				if n, err := io.ReadFull(conn, header); err != nil || n < 5 {
					conn.Close()
					connMu.Lock()
					if activeConn == conn {
						activeConn = nil
					}
					connMu.Unlock()
					conn = nil
					// Got EOF or error; check if it's exit or transient
					if !r.processPIDAlive(inst) {
						log.Info("connection closed and PID dead")
						callOnExit()
						return
					}
					// PID alive; session is unreachable
					log.WithError(err).Debug("connection error, PID alive, entering unreachable loop")
					callOnUnreachable(true)
					continue WATCH_LOOP // conn is nil now; next iteration reconnects with backoff
				}

				// Discard the frame payload
				plen := binary.BigEndian.Uint32(header[1:5])
				if plen > 0 && plen <= captureMaxPayload {
					_, _ = io.CopyN(io.Discard, conn, int64(plen))
				}

				select {
				case <-stopCh:
					conn.Close()
					connMu.Lock()
					if activeConn == conn {
						activeConn = nil
					}
					connMu.Unlock()
					return
				default:
				}
			} else {
				// Reconnect with backoff
				delay := backoffDelays[backoffIndex]
				if backoffIndex < len(backoffDelays)-1 {
					backoffIndex++
				}

				select {
				case <-time.After(delay):
				case <-stopCh:
					return
				}

				conn, err = net.DialTimeout("unix", r.SocketPath(inst.Name), 1*time.Second)
				if err != nil {
					log.WithError(err).Debug("reconnect attempt failed")
					conn = nil
					// Check if PID is dead
					if !r.processPIDAlive(inst) {
						log.Info("reconnect failed and PID dead")
						callOnExit()
						return
					}
					// PID alive; continue retrying
					continue WATCH_LOOP
				}

				// Publish under connMu with stop check in the same critical
				// section to close the race with stop().
				connMu.Lock()
				if stopping {
					connMu.Unlock()
					conn.Close()
					return
				}
				activeConn = conn
				connMu.Unlock()

				// Successfully reconnected; verify sidecar still matches
				if !r.instanceMatches(inst) {
					log.Warn("sidecar mismatch after reconnect; session unreachable")
					conn.Close()
					connMu.Lock()
					if activeConn == conn {
						activeConn = nil
					}
					connMu.Unlock()
					conn = nil
					// Don't call onUnreachable again; already in unreachable state
					// Keep retrying forever
					continue WATCH_LOOP
				}

				log.Debug("reconnected and sidecar matches")
				callOnUnreachable(false)
				backoffIndex = 0 // Reset backoff on successful reconnect
			}
		}
	}()

	// Return stop function (idempotent)
	stopFn := func() {
		stopOnce.Do(func() {
			// Set suppression FIRST (covers in-flight callbacks)
			suppressMu.Lock()
			suppress = true
			suppressMu.Unlock()

			// Mark stopping and close active connection to break the read.
			// The stopping flag is checked under connMu at every publication
			// site, so no connection can be published after this point.
			connMu.Lock()
			stopping = true
			if activeConn != nil {
				activeConn.Close()
				activeConn = nil
			}
			connMu.Unlock()

			// Signal stop
			close(stopCh)

			// Wait for goroutine to exit
			wg.Wait()

			// Wait for any in-flight callbacks to complete
			cbWg.Wait()
		})
	}

	return stopFn, nil
}

// min returns the minimum of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// processPIDAlive checks if the daemon process instance is alive by verifying:
// - PID responds to signal 0
// - For legacy daemons (no nonce): /proc start time matches the recorded start time
// Sidecar matching is separate and handled by instanceMatches().
func (r *Registry) processPIDAlive(inst Instance) bool {
	if inst.Pid <= 0 {
		return false
	}
	if !processAlive(inst.Pid) {
		return false
	}
	// Always verify /proc start time when we have a captured value, for nonce
	// instances too, to detect PID reuse.
	if inst.ProcStartTime > 0 {
		currentStart := procStartTime(inst.Pid)
		if currentStart > 0 {
			return currentStart == inst.ProcStartTime
		}
		// /proc unreadable: identity unverifiable. Err on the side of
		// "alive" — returning false would trigger destructive removal (onExit)
		// of a possibly live session, violating R6.
		return true
	}
	// No captured start time (should not happen: Create/Scan/Adopt capture it).
	// Identity unverifiable; trust the PID check — safe direction, since a
	// wrong-instance watch degrades to unreachable, never removal.
	return true
}

// TryAcquireAdoptionLock attempts to acquire the adoption lock without blocking.
// Returns the release func and true if acquired, or nil func and false if lock is held.
func (r *Registry) TryAcquireAdoptionLock() (func(), bool) {
	lockPath := r.SocketDirLockPath()
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, false
	}

	// Try non-blocking lock
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lockFile.Close()
		return nil, false // Lock held by another process
	}

	return func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}, true
}

// instanceAlive checks if the daemon PID is alive, using instance identity matching.
// Deprecated: use processPIDAlive() instead. This method is kept for compatibility.
func (r *Registry) instanceAlive(inst Instance) bool {
	return r.processPIDAlive(inst)
}

// instanceMatches checks if the current sidecar matches the instance identity.
func (r *Registry) instanceMatches(inst Instance) bool {
	data, err := os.ReadFile(r.metadataPath(inst.Name))
	if err != nil {
		return false
	}
	var meta sessionMeta
	if json.Unmarshal(data, &meta) != nil {
		return false
	}
	if meta.Pid != inst.Pid {
		return false
	}
	// Guard against PID reuse: verify current /proc start time against the
	// captured value when available.
	if inst.ProcStartTime > 0 {
		if cur := procStartTime(meta.Pid); cur > 0 && cur != inst.ProcStartTime {
			return false
		}
	}
	if inst.Nonce != "" {
		return meta.Nonce == inst.Nonce
	}
	return meta.ProcStartTime == inst.ProcStartTime || meta.ProcStartTime == 0
}

// Scan performs a read-only pass over the socket directory, returning SessionInfo
// for every sidecar/socket pair whose daemon PID is alive. It does not delete files.
func (r *Registry) Scan() []SessionInfo {
	entries, err := filepath.Glob(filepath.Join(r.dir, "*.sock"))
	if err != nil {
		return nil
	}

	var out []SessionInfo
	for _, sockPath := range entries {
		name := filepath.Base(sockPath[:len(sockPath)-len(".sock")])

		// Read metadata sidecar
		metaPath := r.metadataPath(name)
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}

		var meta sessionMeta
		if json.Unmarshal(data, &meta) != nil {
			continue
		}

		// Check if PID is alive
		if meta.Pid <= 0 || !processAlive(meta.Pid) {
			continue
		}

		// Capture ProcStartTime from /proc if not set (legacy daemon support)
		startTime := meta.ProcStartTime
		if startTime <= 0 {
			// No recorded start time; capture from /proc so identity is always
			// verifiable (applies to legacy and nonce daemons alike).
			startTime = procStartTime(meta.Pid)
		}

		info := SessionInfo{
			Instance: Instance{
				Name:          name,
				Pid:           meta.Pid,
				Nonce:         meta.Nonce,
				ProcStartTime: startTime,
				SystemdUnit:   meta.SystemdUnit,
			},
			ID:       name,
			Pid:      meta.Pid,
			ShellPid: meta.ShellPid,
			Shell:    meta.Shell,
			Cwd:      meta.Cwd,
			Created:  meta.Created,
			Cols:     meta.Cols,
			Rows:     meta.Rows,
			Socket:   sockPath,
		}
		out = append(out, info)
	}

	return out
}

// Adopt scans for live daemon sidecars and adopts them; cleans up stale PID-dead leftovers
// after re-verifying sidecar identity (nonce or legacy start-time) and process-instance death.
// Returns adopted sessions.
func (r *Registry) Adopt() []SessionInfo {
	entries, err := filepath.Glob(filepath.Join(r.dir, "*.sock"))
	if err != nil {
		return nil
	}

	var out []SessionInfo
	var stale []struct {
		name string
		meta sessionMeta
	}

	for _, sockPath := range entries {
		name := filepath.Base(sockPath[:len(sockPath)-len(".sock")])

		// Read metadata sidecar
		metaPath := r.metadataPath(name)
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}

		var meta sessionMeta
		if json.Unmarshal(data, &meta) != nil {
			continue
		}

		// Check if PID is alive
		if meta.Pid > 0 && processAlive(meta.Pid) {
			// Alive; adopt even if probe dial fails
			// Capture ProcStartTime from /proc if not set (legacy daemon support)
			startTime := meta.ProcStartTime
			if startTime <= 0 {
				// No recorded start time; capture from /proc so identity is always
				// verifiable (applies to legacy and nonce daemons alike).
				startTime = procStartTime(meta.Pid)
			}

			info := SessionInfo{
				Instance: Instance{
					Name:          name,
					Pid:           meta.Pid,
					Nonce:         meta.Nonce,
					ProcStartTime: startTime,
					SystemdUnit:   meta.SystemdUnit,
				},
				ID:       name,
				Pid:      meta.Pid,
				ShellPid: meta.ShellPid,
				Shell:    meta.Shell,
				Cwd:      meta.Cwd,
				Created:  meta.Created,
				Cols:     meta.Cols,
				Rows:     meta.Rows,
				Socket:   sockPath,
			}
			out = append(out, info)
		} else {
			// PID dead; mark for stale-file cleanup
			stale = append(stale, struct {
				name string
				meta sessionMeta
			}{name: name, meta: meta})
		}
	}

	// Clean up stale PID-dead leftovers after TOCTOU re-verification:
	// - Re-read sidecar identity
	// - Verify identity still matches
	// - Re-check process-instance death (PID + /proc start time)
	// Only delete if everything confirms the daemon is dead and hasn't been replaced.
	for _, item := range stale {
		// Re-read sidecar to verify identity hasn't changed
		metaPath := r.metadataPath(item.name)
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue // File already gone; skip
		}
		var currentMeta sessionMeta
		if json.Unmarshal(data, &currentMeta) != nil {
			continue // Can't parse; skip
		}

		// Verify identity still matches
		if item.meta.Nonce != "" {
			if currentMeta.Nonce != item.meta.Nonce || currentMeta.Pid != item.meta.Pid {
				continue // Identity changed; skip cleanup (might be new daemon)
			}
			// Nonce identity matches; verify PID is actually dead (not just nonce unchanged)
			if currentMeta.Pid > 0 && processAlive(currentMeta.Pid) {
				continue // PID still alive; don't delete
			}
		} else {
			// Legacy: verify PID + ProcStartTime
			// Use /proc-captured start time, not sidecar (which may be missing/wrong)
			expectedStart := item.meta.ProcStartTime
			if expectedStart <= 0 {
				// If no start time was recorded, capture it now for comparison
				if item.meta.Pid > 0 && processAlive(item.meta.Pid) {
					// PID came back to life; don't delete
					continue
				}
				// PID is dead, proceed with deletion regardless of sidecar mismatch
			} else {
				// Verify PID + /proc start time match still
				if currentMeta.Pid != item.meta.Pid {
					continue // PID changed; skip cleanup (new daemon)
				}
				// Re-check PID is still dead (not reused)
				if currentMeta.Pid > 0 && processAlive(currentMeta.Pid) {
					currentStart := procStartTime(currentMeta.Pid)
					if currentStart > 0 && currentStart != expectedStart {
						// PID was reused by a different process; proceed with deletion
					} else {
						// Same process still alive, or /proc unreadable (identity
						// unverifiable): never delete while the PID is live.
						continue
					}
				}
			}
		}

		// Identity matches and PID is confirmed dead (or reused); safe to delete
		os.Remove(r.SocketPath(item.name))
		os.Remove(metaPath)
		logrus.WithFields(logrus.Fields{
			"component": "registry",
			"name":      item.name,
			"reason":    "daemon process dead",
		}).Info("removed stale session files during adoption")
	}

	return out
}

// List scans the socket directory for *.sock files, reads their sidecar JSON,
// and checks liveness by attempting a connection.
// Stale socket+json files (where the daemon process is confirmed dead) are removed.
func (r *Registry) List() []SessionInfo {
	entries, err := filepath.Glob(filepath.Join(r.dir, "*.sock"))
	if err != nil {
		return nil
	}

	type removal struct {
		name   string
		reason string
	}

	var (
		out   []SessionInfo
		stale []removal
		total = len(entries)
	)
	// Track which entries we saw for later failure-count cleanup.
	seen := make(map[string]bool, total)

	for _, sockPath := range entries {
		name := filepath.Base(sockPath[:len(sockPath)-len(".sock")])
		seen[name] = true

		conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
		if err != nil {
			// If the daemon process is provably gone, remove now instead of
			// waiting out the 5-tick (~10s) grace window. This matters when a
			// crash/SIGKILL leaves the .sock file behind: without this, the
			// session lingers unreachable and the frontend shows its
			// "Disconnected — Reconnecting" overlay for ~10s. A live-but-
			// unreachable daemon still falls through to the grace below.
			if pid := r.readDaemonPID(name); pid > 0 && !processAlive(pid) {
				stale = append(stale, removal{name: name, reason: "daemon process dead"})
				continue
			}

			r.failMu.Lock()
			r.failCount[name]++
			fails := r.failCount[name]
			r.failMu.Unlock()

			if fails < 5 {
				logrus.WithFields(logrus.Fields{
					"component": "registry",
					"name":      name,
					"fails":     fails,
				}).Debug("daemon liveness check failed, will retry")
				// Still include the session so it is not removed from
				// state during the grace window. Metadata sidecar is
				// readable regardless of socket health.
			} else {
				// Threshold reached. Before removing, verify the daemon
				// process is actually dead (not just slow/overloaded).
				pid := r.readDaemonPID(name)
				if pid > 0 && processAlive(pid) {
					logrus.WithFields(logrus.Fields{
						"component": "registry",
						"name":      name,
						"pid":       pid,
						"fails":     fails,
					}).Warn("daemon process is alive but socket unreachable — keeping session")
					r.failMu.Lock()
					delete(r.failCount, name)
					r.failMu.Unlock()
				} else {
					// Process is dead — safe to clean up.
					stale = append(stale, removal{name: name, reason: "daemon process dead"})
					continue
				}
			}
		} else {
			conn.Close()

			// Reset failure counter on successful connect.
			r.failMu.Lock()
			delete(r.failCount, name)
			r.failMu.Unlock()
		}

		info := SessionInfo{
			ID:     name,
			Socket: sockPath,
		}

		// Read metadata sidecar.
		metaPath := r.metadataPath(name)
		if data, err := os.ReadFile(metaPath); err == nil {
			var meta sessionMeta
			if json.Unmarshal(data, &meta) == nil {
				info.Pid = meta.Pid
				info.ShellPid = meta.ShellPid
				info.Shell = meta.Shell
				info.Cwd = meta.Cwd
				info.Created = meta.Created
				info.Cols = meta.Cols
				info.Rows = meta.Rows
			}
		}

		out = append(out, info)
	}

	// Mass-removal protection: if ALL entries would be removed (stale > 0
	// and live == 0), skip staleness cleanup — this is almost certainly a
	// transient system event (load spike, tmpfs issue, etc.) and we should
	// not nuke every session in one go.
	if len(out) == 0 && len(stale) > 0 {
		logrus.WithFields(logrus.Fields{
			"component": "registry",
			"stale":     len(stale),
			"total":     total,
		}).Warn("all sessions appear stale — skipping removal (probable transient event)")
		// Keep all sessions — do not clean up.
		for _, s := range stale {
			r.failMu.Lock()
			delete(r.failCount, s.name)
			r.failMu.Unlock()
		}
	} else {
		for _, s := range stale {
			r.removeStale(s.name, s.reason)
			r.failMu.Lock()
			delete(r.failCount, s.name)
			r.failMu.Unlock()
		}
	}

	// Clean up failure counters for sessions whose sockets disappeared
	// (e.g. killed externally).
	r.failMu.Lock()
	for name := range r.failCount {
		if !seen[name] {
			delete(r.failCount, name)
		}
	}
	r.failMu.Unlock()

	return out
}

// IsSessionDead reports whether a session is confirmed to have terminated
// (cleanly exited, intentionally killed, or dismissed) according to the
// durable lifecycle store. The state manager uses this to distinguish a
// genuinely empty discovery (all sessions dead) from a transient discovery
// failure, so the last session can be removed instead of lingering in the
// sidebar as "disconnected — reconnecting" forever.
//
// Returns false when no lifecycle store is configured or no record exists —
// conservative, so the mass-removal guards keep protecting against transients
// in the pre-lifecycle / no-store cases.
func (r *Registry) IsSessionDead(name string) bool {
	if r.lifecycleStore == nil {
		return false
	}
	rec, err := r.lifecycleStore.Get(name)
	if err != nil {
		return false
	}
	switch rec.State {
	case LifecycleCleanlyEnded, LifecycleTerminationRequested, LifecycleDismissed:
		return true
	}
	return false
}

// readDaemonPID reads the metadata JSON sidecar and returns the daemon PID,
// or 0 if the file cannot be read or parsed.
func (r *Registry) readDaemonPID(name string) int {
	metaPath := r.metadataPath(name)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return 0
	}
	var meta sessionMeta
	if json.Unmarshal(data, &meta) != nil {
		return 0
	}
	return meta.Pid
}

// processAlive returns true if the process with the given PID exists and is not a zombie.
// Zombies are detected by reading /proc/<pid>/stat and checking the state field (3rd field after last ')'),
// treating 'Z' as dead. Falls back to signal 0 check if /proc is unreadable.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err != nil {
		return false
	}
	// Signal 0 succeeded; process exists. Check if it's a zombie.
	// Read /proc/<pid>/stat to detect zombie state.
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	data, err := os.ReadFile(statPath)
	if err != nil {
		// /proc unreadable; can't verify zombie state. Trust signal 0.
		// Conservative: assume alive (removing a live PID is worse than keeping a zombie).
		return true
	}
	// Parse state field: (pid) ... state ....
	// Field 3 (after the last ')') is the state.
	statStr := string(data)
	closeParen := strings.LastIndexByte(statStr, ')')
	if closeParen < 0 || closeParen+2 >= len(statStr) {
		// Can't parse; trust signal 0.
		return true
	}
	state := statStr[closeParen+2]
	// 'Z' = zombie
	return state != 'Z'
}

// removeStale removes a socket file and its metadata sidecar.
// If the lifecycle store is configured, this function checks whether the
// daemon exited intentionally (cleanly_ended, termination_requested, dismissed)
// or crashed (active state with dead process).  Crashed sessions are NOT
// deleted — they are left on disk for recovery.
// In all cleanup paths (including crash preservation), the associated
// systemd scope is stopped as a best-effort cleanup to prevent orphaned
// cgroups.
func (r *Registry) removeStale(name, reason string) {
	sockPath := r.SocketPath(name)
	metaPath := r.metadataPath(name)

	// Check lifecycle store for crash detection.
	if r.lifecycleStore != nil {
		rec, err := r.lifecycleStore.Get(name)
		if err == nil {
			switch rec.State {
			case LifecycleActive:
				// The daemon process died but the lifecycle record
				// was never transitioned out of active — this is a crash.
				// Preserve the socket and metadata for recovery.
				logrus.WithFields(logrus.Fields{
					"component": "registry",
					"name":      name,
					"pid":       rec.DaemonPID,
				}).Warn("daemon crashed — preserving session for recovery")
				if transErr := r.lifecycleStore.Transition(name, LifecycleActive, LifecycleCrashed); transErr != nil {
					logrus.WithError(transErr).WithField("name", name).Warn("failed to transition to crashed")
				}
				// Stop the systemd scope even though we preserve files.
				// Use the exact unit from the record we already read.
				r.StopSystemdUnit(rec.SystemdUnit)
				// Do NOT delete socket/metadata — they are needed for recovery.
				return

			case LifecycleCleanlyEnded, LifecycleTerminationRequested, LifecycleDismissed:
				// Intentionally terminated or dismissed — clean up normally.

			case LifecycleCrashed:
				// Already marked as crashed, keep preserved.
				r.StopSystemdUnit(rec.SystemdUnit)
				return

			default:
				// Unknown state — clean up cautiously.
			}
		}
		// If no lifecycle record exists (pre-lifecycle daemon),
		// fall through to normal cleanup.
	}

	// Stop systemd scope before removing files.
	r.stopSystemdScope(name)

	os.Remove(sockPath)
	os.Remove(metaPath)
	logrus.WithFields(logrus.Fields{
		"component": "registry",
		"name":      name,
		"reason":    reason,
	}).Info("removed stale session files")
}

// Kill sends a FrameClose to the daemon via its socket.
// It marks the session as intentionally terminated in the lifecycle store
// so the registry can distinguish explicit kills from crashes.
// If a systemd scope unit was recorded, it is stopped as a best-effort
// cleanup (in case the daemon process doesn't exit cleanly).
func (r *Registry) Kill(name string) error {
	// Record intentional kill before sending FrameClose.
	// If the daemon process dies without transitioning to cleanly_ended,
	// the termination_requested state tells the registry this wasn't a crash.
	if r.lifecycleStore != nil {
		// Best-effort — ignore errors (the record may not exist yet or
		// the state may already have changed).
		_ = r.lifecycleStore.Transition(name, LifecycleActive, LifecycleTerminationRequested)
	}

	// Read the systemd unit once for cleanup, regardless of the path taken.
	unit := r.readSystemdUnit(name)

	socketPath := r.SocketPath(name)
	conn, err := net.DialTimeout("unix", socketPath, 1*time.Second)
	if err != nil {
		// Socket unreachable — try to stop the systemd scope anyway.
		r.StopSystemdUnit(unit)
		return fmt.Errorf("dial daemon socket %s: %w", socketPath, err)
	}
	defer conn.Close()

	frame := encodeFrame(FrameClose, nil)
	if _, err := conn.Write(frame); err != nil {
		// FrameClose failed — try to force-stop the systemd scope.
		r.StopSystemdUnit(unit)
		return fmt.Errorf("send close frame: %w", err)
	}

	// Best-effort: stop the systemd scope so it doesn't linger.
	// The daemon should exit on its own after receiving FrameClose, but
	// systemd scope cleanup is an extra safety net.
	r.StopSystemdUnit(unit)
	return nil
}

// Capture limits.
const (
	captureMaxPayload  = 10 * 1024 * 1024 // sanity: max 10 MiB
	captureDialTimeout = 1 * time.Second
	captureReadTimeout = 10 * time.Second
	captureTailTimeout = 2 * time.Second
)

// ErrCaptureTimeout is returned when a capture or tail capture exceeds its
// read deadline.
var ErrCaptureTimeout = errors.New("capture timeout")

// Capture connects to the daemon, sends FrameQueryBuffer, reads the
// FrameReplay response, and returns the text content.
// It preserves legacy byte-compatible output semantics.
func (r *Registry) Capture(name string) (string, error) {
	socketPath := r.SocketPath(name)
	conn, err := net.DialTimeout("unix", socketPath, captureDialTimeout)
	if err != nil {
		return "", fmt.Errorf("dial daemon socket %s: %w", socketPath, err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(captureReadTimeout)); err != nil {
		return "", fmt.Errorf("set read deadline: %w", err)
	}

	payload, err := r.capturePayload(conn)
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return r.legacyCleanCapture(payload), ErrCaptureTimeout
		}
		return "", fmt.Errorf("capture %s: %w", name, err)
	}
	return r.legacyCleanCapture(payload), nil
}

// CaptureTail connects to the daemon and returns at most maxBytes of the
// trailing buffer content. It shares framing and cleaning logic with Capture
// but avoids reading (and duplicating) the full payload for bounded previews.
// The returned text is cleaned of ANSI sequences and control characters; the
// first line is stripped when it would start mid-line, and partial leading
// UTF-8 continuation bytes are removed.
func (r *Registry) CaptureTail(name string, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		return "", nil
	}

	socketPath := r.SocketPath(name)
	conn, err := net.DialTimeout("unix", socketPath, captureDialTimeout)
	if err != nil {
		return "", fmt.Errorf("dial daemon socket %s: %w", socketPath, err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(captureTailTimeout)); err != nil {
		return "", fmt.Errorf("set read deadline: %w", err)
	}

	// Send bounded query so the daemon replies with at most maxBytes.
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(maxBytes))
	frame := encodeFrame(FrameQueryBuffer, payload)
	if _, err := conn.Write(frame); err != nil {
		return "", fmt.Errorf("send query buffer frame: %w", err)
	}

	// Read response header to validate length and frame type.
	header := make([]byte, 5)
	if _, err := io.ReadFull(conn, header); err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return "", ErrCaptureTimeout
		}
		return "", fmt.Errorf("read response header: %w", err)
	}

	ftype := header[0]
	plen := binary.BigEndian.Uint32(header[1:5])

	if plen > captureMaxPayload {
		return "", fmt.Errorf("response too large: %d bytes", plen)
	}

	if ftype != FrameReplay {
		return "", fmt.Errorf("unexpected frame type: %02x", ftype)
	}

	if plen == 0 {
		return "", nil
	}

	// The daemon may be older and ignore the bounded query payload. If it
	// returns a full replay, discard its head and retain only the tail.
	readLen := int(plen)
	if readLen > maxBytes {
		if _, err := io.CopyN(io.Discard, conn, int64(readLen-maxBytes)); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return "", ErrCaptureTimeout
			}
			return "", fmt.Errorf("skip replay head: %w", err)
		}
		readLen = maxBytes
	}

	buf := make([]byte, readLen)
	if _, err := io.ReadFull(conn, buf); err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return "", ErrCaptureTimeout
		}
		return "", fmt.Errorf("read response payload: %w", err)
	}

	// A response shorter than the requested window is complete. A response
	// equal to the window may be either a bounded tail or an old full replay.
	if plen < uint32(maxBytes) {
		return r.legacyCleanCapture(buf), nil
	}
	return r.cleanCapture(buf), nil
}

// capturePayload sends FrameQueryBuffer and reads the full FrameReplay payload.
// It returns the raw payload (not cleaned). On read-deadline timeout the
// captured bytes read so far are returned along with a timeout error so that
// callers can still use partial data if they choose.
func (r *Registry) capturePayload(conn net.Conn) ([]byte, error) {
	// Send query.
	frame := encodeFrame(FrameQueryBuffer, nil)
	if _, err := conn.Write(frame); err != nil {
		return nil, fmt.Errorf("send query buffer frame: %w", err)
	}

	// Read the response header.
	header := make([]byte, 5)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, fmt.Errorf("read response header: %w", err)
	}

	ftype := header[0]
	plen := binary.BigEndian.Uint32(header[1:5])

	if plen > captureMaxPayload {
		return nil, fmt.Errorf("response too large: %d bytes", plen)
	}

	if ftype != FrameReplay {
		return nil, fmt.Errorf("unexpected frame type: %02x", ftype)
	}

	payload := make([]byte, plen)
	if plen > 0 {
		if _, err := io.ReadFull(conn, payload); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				// Return what we managed to read before the deadline.
				return payload, err
			}
			return nil, fmt.Errorf("read response payload: %w", err)
		}
	}

	return payload, nil
}

// cleanCapture strips ANSI escape sequences and control chars so callers get
// clean text (like capture-pane). It also trims a partial leading UTF-8
// continuation byte and drops the first line when it starts mid-line.
func (r *Registry) cleanCapture(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}

	// Strip any partial leading UTF-8 continuation byte; the prior bytes were
	// discarded by tail capture or ended mid-run on the wire.
	start := 0
	for start < len(payload) && payload[start]&0xc0 == 0x80 {
		start++
	}
	payload = payload[start:]

	if len(payload) == 0 {
		return ""
	}

	// Drop a leading partial line only when the bounded payload was
	// actually truncated and starts with non-newline content that contains a
	// newline. If payload starts with '\n', retain it. If no newline exists,
	// retain content instead of returning empty.
	if payload[0] != '\n' {
		if nl := bytes.IndexByte(payload, '\n'); nl >= 0 {
			payload = payload[nl+1:]
		}
	}

	if len(payload) == 0 {
		return ""
	}

	// Strip ANSI escape sequences and control chars.
	clean := ansiRe.ReplaceAllString(string(payload), "")
	clean = ctrlRe.ReplaceAllString(clean, "")
	return clean
}

// legacyCleanCapture is the original Capture cleaning behavior, kept for
// compatibility. It does not strip partial leading bytes/lines.
func (r *Registry) legacyCleanCapture(payload []byte) string {
	clean := ansiRe.ReplaceAllString(string(payload), "")
	clean = ctrlRe.ReplaceAllString(clean, "")
	return clean
}

// CrashedSessions returns lifecycle records for all sessions that crashed
// (state == "crashed").  Returns nil if no lifecycle store is configured.
func (r *Registry) CrashedSessions() []LifecycleRecord {
	if r.lifecycleStore == nil {
		return nil
	}
	return r.lifecycleStore.ListByState(LifecycleCrashed)
}

// DetectAndCleanupCrashes calls DetectCrashes on the lifecycle store and
// stops the systemd scope for each newly discovered crashed session.
// This prevents orphaned cgroups from accumulating across server restarts.
// Returns the list of newly crashed records (in "crashed" state).
func (r *Registry) DetectAndCleanupCrashes() []LifecycleRecord {
	if r.lifecycleStore == nil {
		return nil
	}
	crashed := r.lifecycleStore.DetectCrashes()
	for _, rec := range crashed {
		// Stop the exact unit from the crashed record — do NOT re-read
		// lifecycle state via stopSystemdScope, which could pick up a
		// concurrently-recovered record and kill the replacement scope.
		r.StopSystemdUnit(rec.SystemdUnit)
	}
	return crashed
}

// RecoverSession re-spawns a daemon for a previously crashed session.
// It reads the saved shell/cwd from the lifecycle record, starts a new daemon,
// and transitions the state to "recovered".  The old stale socket and metadata
// files are cleaned up before the new daemon is spawned.
// Optional shellOverride and cwdOverride allow the user to choose a different
// shell or working directory at recovery time.
func (r *Registry) RecoverSession(id string, shellOverride ...string) error {
	if !validSessionID(id) {
		return fmt.Errorf("invalid session id: %q", id)
	}
	r.recoveryMu.Lock()
	defer r.recoveryMu.Unlock()
	if r.lifecycleStore == nil {
		return fmt.Errorf("no lifecycle store configured")
	}

	rec, err := r.lifecycleStore.Get(id)
	if err != nil {
		return fmt.Errorf("get lifecycle record for %s: %w", id, err)
	}
	if rec.State != LifecycleCrashed {
		return fmt.Errorf("session %s is in state %q, not crashed", id, rec.State)
	}

	shell := rec.Shell
	cwd := rec.Cwd
	if len(shellOverride) > 0 && shellOverride[0] != "" {
		shell = shellOverride[0]
	}
	if len(shellOverride) > 1 && shellOverride[1] != "" {
		cwd = shellOverride[1]
	}

	// Stop the old systemd scope before removing files and spawning
	// the replacement.  The crashed session's scope may still have
	// orphaned child processes (shell, background jobs).
	// Use the exact unit from the crashed record — do not re-read.
	r.StopSystemdUnit(rec.SystemdUnit)

	// Transition to recovered BEFORE spawning — the new daemon will
	// overwrite the lifecycle record with a fresh "active" state on
	// startup, which is the correct final state.
	if err := r.lifecycleStore.Transition(id, LifecycleCrashed, LifecycleRecovered); err != nil {
		return fmt.Errorf("transition to recovered: %w", err)
	}

	// Clean up old stale files so the new daemon can claim the socket.
	os.Remove(r.SocketPath(id))
	os.Remove(r.metadataPath(id))

	// Spawn a new daemon with the saved (or overridden) configuration.
	_, err = r.Create(id, shell, cwd, rec.Cols, rec.Rows)
	if err != nil {
		// Rollback lifecycle state on spawn failure.
		_ = r.lifecycleStore.Transition(id, LifecycleRecovered, LifecycleCrashed)
		return fmt.Errorf("re-spawn daemon for %s: %w", id, err)
	}

	logrus.WithFields(logrus.Fields{
		"component": "registry",
		"id":        id,
		"shell":     shell,
		"cwd":       cwd,
	}).Info("recovered crashed session")

	return nil
}

// DismissSession marks a crashed session as dismissed and cleans up its files.
func (r *Registry) DismissSession(id string) error {
	if !validSessionID(id) {
		return fmt.Errorf("invalid session id: %q", id)
	}
	r.recoveryMu.Lock()
	defer r.recoveryMu.Unlock()
	return r.dismissSessionLocked(id)
}

// dismissSessionLocked is the inner implementation; caller must hold recoveryMu.
func (r *Registry) dismissSessionLocked(id string) error {
	if r.lifecycleStore == nil {
		// No lifecycle store — clean up files only (no scope to stop).
		os.Remove(r.SocketPath(id))
		os.Remove(r.metadataPath(id))
		return nil
	}

	rec, err := r.lifecycleStore.Get(id)
	if err != nil {
		// No lifecycle record — clean up files only (pre-lifecycle daemon).
		os.Remove(r.SocketPath(id))
		os.Remove(r.metadataPath(id))
		return nil
	}

	// Guard: only crashed sessions can be dismissed.  Do NOT stop the
	// scope before this check — a live session must not be killed just
	// because someone asked to dismiss it.
	if rec.State != LifecycleCrashed {
		return fmt.Errorf("session %s is in state %q, not crashed", id, rec.State)
	}

	// Stop the exact systemd unit from the crashed record before
	// cleaning up files.  We use the unit from the already-read record
	// to avoid re-reading mutable lifecycle state.
	r.StopSystemdUnit(rec.SystemdUnit)

	if err := r.lifecycleStore.Transition(id, LifecycleCrashed, LifecycleDismissed); err != nil {
		return fmt.Errorf("transition to dismissed: %w", err)
	}

	os.Remove(r.SocketPath(id))
	os.Remove(r.metadataPath(id))
	return nil
}

// DismissAll marks all crashed sessions as dismissed and cleans up their files.
func (r *Registry) DismissAll() error {
	r.recoveryMu.Lock()
	defer r.recoveryMu.Unlock()
	crashed := r.CrashedSessions()
	for _, rec := range crashed {
		_ = r.dismissSessionLocked(rec.ID)
	}
	return nil
}

// CleanupCrashedIfDead removes crash-preserved files for a session if the
// daemon process is confirmed dead and the user hasn't chosen recovery.
// This is called as a fallback when a session transitions from crashed to
// cleanly_ended (e.g. the daemon's shell finally exits after a crash).
func (r *Registry) CleanupCrashedIfDead(id string) {
	if r.lifecycleStore == nil {
		return
	}
	rec, err := r.lifecycleStore.Get(id)
	if err != nil || rec.State != LifecycleCrashed {
		return
	}
	if !processAlive(rec.DaemonPID) {
		// Process long dead — safe to clean up.
		// Use the exact unit from the already-read crashed record.
		r.StopSystemdUnit(rec.SystemdUnit)
		os.Remove(r.SocketPath(id))
		os.Remove(r.metadataPath(id))
	}
}

// StopSystemdUnit stops a specific systemd user scope unit by name.
// This helper does NOT re-read lifecycle state — the caller is responsible
// for passing the correct unit (e.g., from an already-read lifecycle record).
// It is best-effort: failures are logged but not returned.
func (r *Registry) StopSystemdUnit(unit string) {
	// Test hook: if set, delegate to the injected function instead of
	// spawning a real systemctl process.
	if r.stopUnitFn != nil {
		r.stopUnitFn(unit)
		return
	}
	if unit == "" {
		return
	}

	log := logrus.WithFields(logrus.Fields{
		"component": "registry",
		"unit":      unit,
	})

	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		log.Debug("systemctl not found, skipping scope stop")
		return
	}

	// Run with a short timeout so we never block the caller.
	stop := exec.Command(systemctl, "--user", "stop", unit)
	// Detach from parent's stdin/stdout.
	stop.Stdin = nil
	stop.Stdout = nil
	stop.Stderr = nil

	if err := stop.Start(); err != nil {
		log.WithError(err).Debug("failed to start systemctl stop")
		return
	}

	// Don't wait — systemctl stop on a dead scope may hang briefly.
	// Release the process handle so it runs independently.
	_ = stop.Process.Release()
	log.Debug("requested systemd scope stop")
}

// stopSystemdScope reads the unit name for a session and stops it.
// Prefer stopSystemdUnit when you already have the unit name from an
// already-read lifecycle record, to avoid re-reading mutable state.
func (r *Registry) stopSystemdScope(name string) {
	unit := r.readSystemdUnit(name)
	r.StopSystemdUnit(unit)
}

// readSystemdUnit returns the systemd unit name from the lifecycle record
// or, as a fallback, from the metadata JSON sidecar.
func (r *Registry) readSystemdUnit(name string) string {
	// Try lifecycle record first.
	if r.lifecycleStore != nil {
		if rec, err := r.lifecycleStore.Get(name); err == nil && rec.SystemdUnit != "" {
			return rec.SystemdUnit
		}
	}

	// Fallback: read from metadata sidecar.
	metaPath := r.metadataPath(name)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return ""
	}
	var meta sessionMeta
	if json.Unmarshal(data, &meta) != nil {
		return ""
	}
	return meta.SystemdUnit
}

// SocketDirLockPath returns the path to the adoption lock file in the socket directory.
func (r *Registry) SocketDirLockPath() string {
	return filepath.Join(r.dir, ".adoption.lock")
}

// AcquireAdoptionLock acquires an exclusive flock on the socket directory.
// Used by the server during boot to prevent CLI direct-spawn races.
// Returns a closer function that releases the lock.
func (r *Registry) AcquireAdoptionLock() (func(), error) {
	lockPath := r.SocketDirLockPath()
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	// Acquire exclusive lock
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		lockFile.Close()
		return nil, fmt.Errorf("acquire lock: %w", err)
	}

	return func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}, nil
}

// CheckAdoptionLockHeld checks whether the adoption lock is currently held.
// Returns true if the lock is held by another process (server booting).
func (r *Registry) CheckAdoptionLockHeld() bool {
	lockPath := r.SocketDirLockPath()
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return false // Can't check; assume not held
	}
	defer lockFile.Close()

	// Try non-blocking lock
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		// Got the lock; release it immediately and return false (not held)
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		return false
	}
	// Lock held; another process has exclusive lock
	return true
}
