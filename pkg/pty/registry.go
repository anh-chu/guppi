package pty

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
)

// ansiRe matches ANSI escape sequences (CSI, OSC, and simple escapes).
var ansiRe = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[a-zA-Z]|\][^\x1b\x07]*(?:\x07|\x1b\\)|[()][AB012]|\[\?[0-9;]*[hl]|=|>|\x1b)`)

// ctrlRe matches carriage returns and other non-newline control chars.
var ctrlRe = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f]`)

// SessionInfo holds metadata about a running session daemon.
type SessionInfo struct {
	ID       string
	Pid      int
	ShellPid int
	Shell    string
	Cwd      string
	Created  string // RFC3339
	Cols     uint16
	Rows     uint16
	Socket   string // full path to .sock file
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
// It waits up to 2s for the socket file to appear, then returns.
func (r *Registry) Create(name, shell, cwd string, cols, rows uint16) error {
	log := logrus.WithFields(logrus.Fields{
		"component": "registry",
		"name":      name,
		"shell":     shell,
		"cwd":       cwd,
	})

	// Guard against duplicate names — the socket file would be overwritten,
	// causing two terminals to share a single daemon.
	if _, err := os.Stat(r.SocketPath(name)); err == nil {
		return fmt.Errorf("session %q already exists", name)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable: %w", err)
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

	args := []string{
		"session-daemon",
		"--id", name,
		"--shell", shell,
		"--cols", fmt.Sprintf("%d", cols),
		"--rows", fmt.Sprintf("%d", rows),
		"--cwd", cwd,
		"--socket-dir", r.dir,
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
		return fmt.Errorf("open /dev/null: %w", err)
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
				return fmt.Errorf("start daemon process: %w", err)
			}
		} else {
			return fmt.Errorf("start daemon process: %w", err)
		}
	}

	// Release the process handle so the daemon is fully independent.
	// Don't wait for the socket to appear here — the terminal connects
	// via NewDaemonSession, which retries the dial until the daemon is
	// ready. Returning immediately lets the UI mount in parallel with
	// the daemon's cold-start (and the systemd-run DBus round-trip).
	if err := cmd.Process.Release(); err != nil {
		log.WithError(err).Warn("failed to release daemon process handle")
	}

	log.WithField("socket", r.SocketPath(name)).Info("session daemon created")
	return nil
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

// processAlive returns true if the process with the given PID exists.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
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
				r.stopSystemdUnit(rec.SystemdUnit)
				// Do NOT delete socket/metadata — they are needed for recovery.
				return

			case LifecycleCleanlyEnded, LifecycleTerminationRequested, LifecycleDismissed:
				// Intentionally terminated or dismissed — clean up normally.

			case LifecycleCrashed:
				// Already marked as crashed, keep preserved.
				r.stopSystemdUnit(rec.SystemdUnit)
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
		r.stopSystemdUnit(unit)
		return fmt.Errorf("dial daemon socket %s: %w", socketPath, err)
	}
	defer conn.Close()

	frame := encodeFrame(FrameClose, nil)
	if _, err := conn.Write(frame); err != nil {
		// FrameClose failed — try to force-stop the systemd scope.
		r.stopSystemdUnit(unit)
		return fmt.Errorf("send close frame: %w", err)
	}

	// Best-effort: stop the systemd scope so it doesn't linger.
	// The daemon should exit on its own after receiving FrameClose, but
	// systemd scope cleanup is an extra safety net.
	r.stopSystemdUnit(unit)
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
		r.stopSystemdUnit(rec.SystemdUnit)
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
	r.stopSystemdUnit(rec.SystemdUnit)

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
	if err := r.Create(id, shell, cwd, rec.Cols, rec.Rows); err != nil {
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
	r.stopSystemdUnit(rec.SystemdUnit)

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
		r.stopSystemdUnit(rec.SystemdUnit)
		os.Remove(r.SocketPath(id))
		os.Remove(r.metadataPath(id))
	}
}

// stopSystemdUnit stops a specific systemd user scope unit by name.
// This helper does NOT re-read lifecycle state — the caller is responsible
// for passing the correct unit (e.g., from an already-read lifecycle record).
// It is best-effort: failures are logged but not returned.
func (r *Registry) stopSystemdUnit(unit string) {
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
	r.stopSystemdUnit(unit)
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
