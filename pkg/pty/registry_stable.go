package pty

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	// readinessInitialDelay is a short pause so the daemon has time to bind
	// before the first probe.
	readinessInitialDelay = 25 * time.Millisecond
	// readinessInterval is the poll interval between connect/query attempts.
	readinessInterval = 25 * time.Millisecond
	// readinessTimeout caps how long Start waits for the daemon handshake.
	// Real subprocess spawn (fork+exec+socket-bind+handshake) can take well
	// over 5s under CPU contention (observed 10-15s+ in concurrent
	// multi-node E2E runs and under general host load) -- 5s was tight
	// enough that it routinely fired on a merely-busy host, not just a
	// genuinely-hung daemon, causing the create-retry loop
	// (pkg/state/reconciler.go / session_commands.go, 5 retries with
	// exponential backoff) to exhaust before the daemon ever became ready.
	readinessTimeout = 20 * time.Second

	// identityDialTimeout is the per-attempt socket dial timeout.
	identityDialTimeout = 500 * time.Millisecond
	// identityReadTimeout is the per-attempt frame read timeout.
	identityReadTimeout = 500 * time.Millisecond
	// identityMaxPayload bounds the identity response size.
	identityMaxPayload = 64 * 1024
)

// Start spawns a daemon bound to the requested stable identity, waits for it
// to answer the identity handshake, and returns its ReadyInfo.
//
// It refuses to bind a second generation to the same stable identity and
// uses process/lifecycle evidence (not just socket existence) to decide if a
// prior binding is still live. Cancellation of ctx tears down the spawned
// process so the create worker that owns ctx cannot leak an untracked daemon.
func (r *Registry) Start(ctx context.Context, req StartRequest) (ReadyInfo, error) {
	if err := req.validate(); err != nil {
		return ReadyInfo{}, err
	}
	if req.Generation == "" {
		req.Generation = NewGeneration()
	}
	key := req.EffectiveDaemonKey()

	log := logrus.WithFields(logrus.Fields{
		"component":  "registry",
		"daemon_key": key,
		"owner":      req.Owner,
		"session_id": req.SessionID,
		"generation": req.Generation,
		"command_id": req.CommandID,
	})

	// 1. Inspect any existing lifecycle record for this daemon key.
	if rec := r.getLifecycle(key); rec != nil {
		switch {
		case rec.State == LifecycleStarting, rec.State == LifecycleActive:
			if sameGeneration(*rec, req.Generation) && r.processEvidenceLive(rec) {
				return ReadyInfo{}, fmt.Errorf("%w: %s/%s@%s", ErrAlreadyBound, req.Owner, req.SessionID, req.Generation)
			}
			if !r.processEvidenceLive(rec) {
				// Dead prior binding: clean up stale files so the new daemon
				// can claim the socket.
				r.cleanStaleFiles(key, "dead prior binding")
			} else {
				return ReadyInfo{}, fmt.Errorf("%w: daemon key %q bound to a different generation", ErrBindingInUse, key)
			}
		case rec.Generation != "" && rec.Generation != req.Generation:
			// A prior generation's lifecycle record exists. Only allow
			// replacement if the old daemon is provably gone.
			if !r.processEvidenceLive(rec) {
				r.cleanStaleFiles(key, "prior generation dead")
			} else {
				return ReadyInfo{}, fmt.Errorf("%w: daemon key %q", ErrBindingInUse, key)
			}
		default:
			// Terminal state or empty legacy record: clean files and proceed.
			r.cleanStaleFiles(key, "terminal/legacy prior state")
		}
	}

	// 2. Guard against a live socket with no lifecycle record (legacy race).
	if r.isSocketLive(key) {
		return ReadyInfo{}, fmt.Errorf("%w: daemon key %q socket already live", ErrBindingInUse, key)
	}

	// 3. Write a "starting" lifecycle record so a crash between spawn and
	// ready leaves durable evidence instead of an untracked process.
	if r.lifecycleStore != nil {
		startRec := LifecycleRecord{
			ID:         key,
			State:      LifecycleStarting,
			Shell:      req.Shell,
			Cwd:        req.Cwd,
			Cols:       req.Cols,
			Rows:       req.Rows,
			Generation: req.Generation,
			Owner:      req.Owner,
			SessionID:  req.SessionID,
			DaemonKey:  key,
			CommandID:  req.CommandID,
		}
		if err := r.lifecycleStore.writeAtomic(startRec); err != nil {
			log.WithError(err).Warn("failed to write starting lifecycle record")
		}
	}

	// 4. Build daemon command.
	shell := req.Shell
	if shell == "" {
		shell = os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/bash"
		}
	}
	cols, rows := req.Cols, req.Rows
	if cols == 0 {
		cols = 120
	}
	if rows == 0 {
		rows = 40
	}

	exe, err := os.Executable()
	if err != nil {
		return ReadyInfo{}, fmt.Errorf("get executable: %w", err)
	}

	args := []string{
		"session-daemon",
		"--id", key,
		"--daemon-key", key,
		"--owner", req.Owner,
		"--session-id", req.SessionID,
		"--generation", req.Generation,
		"--shell", shell,
		"--cols", fmt.Sprintf("%d", cols),
		"--rows", fmt.Sprintf("%d", rows),
		"--cwd", req.Cwd,
		"--socket-dir", r.dir,
		"--state-dir", r.stateDirOrDefault(),
	}
	if req.CommandID != "" {
		args = append(args, "--command-id", req.CommandID)
	}

	useSystemd := false
	var systemdUnit string
	if systemdRun, err := exec.LookPath("systemd-run"); err == nil && os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		useSystemd = true
		systemdUnit = fmt.Sprintf("termyard-session-%s-%d.scope", key, time.Now().UnixMilli())
		scopeArgs := []string{"--user", "--scope", "--unit", systemdUnit, "--"}
		fullArgs := append(scopeArgs, exe)
		fullArgs = append(fullArgs, args...)
		if systemdUnit != "" {
			fullArgs = append(fullArgs, "--systemd-unit", systemdUnit)
		}
		args = fullArgs
		exe = systemdRun
	} else {
		if systemdUnit != "" {
			args = append(args, "--systemd-unit", systemdUnit)
		}
	}

	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return ReadyInfo{}, fmt.Errorf("open /dev/null: %w", err)
	}
	defer devNull.Close()
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull

	if err := cmd.Start(); err != nil {
		if useSystemd {
			log.WithError(err).Warn("systemd-run failed, falling back to direct spawn")
			cmd = exec.Command(exe, args...)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			cmd.Stdin = devNull
			cmd.Stdout = devNull
			cmd.Stderr = devNull
			if err := cmd.Start(); err != nil {
				r.clearStartingRecord(key)
				return ReadyInfo{}, fmt.Errorf("start daemon process: %w", err)
			}
		} else {
			r.clearStartingRecord(key)
			return ReadyInfo{}, fmt.Errorf("start daemon process: %w", err)
		}
	}

	// 5. Wait for identity handshake. Keep the process handle until ready
	// so ctx cancellation can tear the daemon down.
	ready, err := r.waitReady(ctx, key, req.Generation)
	if err != nil {
		if cmd.Process != nil {
			killProcessGroup(cmd.Process.Pid)
			cmd.Process.Wait()
		}
		// If cancellation happened after spawn, lifecycle remains in "starting"
		// and DetectCrashes will reconcile it later.
		log.WithError(err).Warn("stable daemon did not become ready")
		return ReadyInfo{}, err
	}

	// Release only after the daemon is proven ready.
	if err := cmd.Process.Release(); err != nil {
		log.WithError(err).Warn("failed to release daemon process handle")
	}

	log.WithField("daemon_pid", ready.DaemonPID).Info("stable session daemon ready")
	return ready, nil
}

// Probe returns non-destructive evidence about a binding. It never deletes
// files or transitions lifecycle state. A stale socket from a different
// generation does not satisfy liveness; a reused PID does not either.
func (r *Registry) Probe(binding StableBinding) ProbeEvidence {
	key := binding.EffectiveDaemonKey()
	ev := ProbeEvidence{Binding: binding, Status: ProbeUnknown}

	rec := r.getLifecycle(key)

	// Legacy live check: socket reachable with live process and no stable
	// lifecycle record. This keeps pre-v2 daemons attachable after migration.
	if rec == nil {
		if r.isSocketLive(key) {
			meta := r.readMetadata(key)
			if meta != nil && processAlive(meta.Pid) {
				ev.Status = ProbeLive
				ev.DaemonPID = meta.Pid
				ev.ShellPID = meta.ShellPid
				ev.Reason = "legacy socket live"
			} else {
				ev.Status = ProbeUnknown
				ev.Reason = "legacy socket without process evidence"
			}
		}
		return ev
	}

	ev.DaemonPID = rec.DaemonPID
	ev.ShellPID = rec.DaemonPID // only ReadyInfo carries shell PID; fallback

	if terminalLifecycleStates[rec.State] {
		ev.Status = ProbeClean
		ev.Reason = "lifecycle state " + rec.State
		return ev
	}
	if rec.State == LifecycleCrashed {
		ev.Status = ProbeCrashed
		ev.Reason = "lifecycle state crashed"
		return ev
	}

	// Verify socket + handshake for live daemons when we have a stable claim.
	if binding.IsStable() {
		info, err := r.queryIdentity(key)
		if err == nil && sameReadyInfo(info, binding) && processAlive(info.DaemonPID) {
			if !sameStartTime(info.DaemonPID, rec.ProcStartTime) {
				ev.Status = ProbeCrashed
				ev.Reason = "PID reused by a different process"
				return ev
			}
			ev.Status = ProbeLive
			ev.DaemonPID = info.DaemonPID
			ev.ShellPID = info.ShellPID
			ev.Reason = "identity handshake matches"
			return ev
		}
	}

	// Fallback to process/lifecycle evidence.
	if !r.processEvidenceLive(rec) {
		if rec.State == LifecycleActive || rec.State == LifecycleStarting {
			ev.Status = ProbeCrashed
			ev.Reason = "process dead"
		} else {
			ev.Status = ProbeUnknown
			ev.Reason = "process not live"
		}
		return ev
	}

	// Process is alive but handshake failed or we have no stable claim.
	if !binding.IsStable() {
		ev.Status = ProbeLive
		ev.Reason = "legacy process live"
	} else {
		ev.Status = ProbeUnknown
		ev.Reason = "process live but identity handshake mismatch"
	}
	return ev
}

// Terminate requests an exact-generation shutdown and returns a typed outcome.
func (r *Registry) Terminate(ctx context.Context, binding StableBinding) TerminateOutcome {
	key := binding.EffectiveDaemonKey()
	rec := r.getLifecycle(key)

	// No durable record: fall back to socket evidence.
	if rec == nil {
		if !r.isSocketLive(key) {
			return TerminateUnknown
		}
		if binding.IsStable() {
			info, err := r.queryIdentity(key)
			if err != nil || !sameReadyInfo(info, binding) {
				return TerminateUnknown
			}
		}
		if r.sendClose(key) {
			return TerminateAcknowledged
		}
		return TerminateUnknown
	}

	// Exact generation protection.
	if rec.Generation != "" && binding.Generation != "" && rec.Generation != binding.Generation {
		return TerminateGenerationMismatch
	}

	if terminalLifecycleStates[rec.State] {
		return TerminateAlreadyEnded
	}
	if rec.State == LifecycleCrashed {
		return TerminateAlreadyEnded
	}

	if binding.IsStable() {
		info, err := r.queryIdentity(key)
		if err == nil && binding.Generation != "" && info.Generation != binding.Generation {
			return TerminateGenerationMismatch
		}
	}

	// Pre-mark intentional termination so a crash during shutdown isn't
	// misclassified.
	if r.lifecycleStore != nil {
		_ = r.lifecycleStore.Transition(key, rec.State, LifecycleTerminationRequested)
	}

	unit := r.readSystemdUnit(key)
	if r.sendClose(key) {
		r.stopSystemdUnit(unit)
		return TerminateAcknowledged
	}

	// Close frame failed. Classify by current process evidence.
	if r.processEvidenceLive(rec) {
		r.stopSystemdUnit(unit)
		return TerminateUnknown
	}
	return TerminateAlreadyEnded
}

// waitReady polls the daemon socket with identity queries until the daemon
// responds with the expected generation, ctx is cancelled, or the timeout
// expires.
func (r *Registry) waitReady(ctx context.Context, key, generation string) (ReadyInfo, error) {
	deadline := time.Now().Add(readinessTimeout)
	timer := time.NewTimer(readinessInitialDelay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ReadyInfo{}, ctx.Err()
		case <-timer.C:
		}

		info, err := r.queryIdentity(key)
		if err == nil {
			if info.Generation != generation {
				// Stale socket from another generation: keep waiting. The
				// expected daemon should overwrite the socket once ready.
			} else if processAlive(info.DaemonPID) {
				return info, nil
			}
		}

		if !time.Now().Before(deadline) {
			return ReadyInfo{}, fmt.Errorf("daemon %s not ready within %v", key, readinessTimeout)
		}

		timer = time.NewTimer(readinessInterval)
	}
}

// queryIdentity connects to a daemon, asks for its stable identity, and
// returns the ReadyInfo. It does not trust socket existence alone.
func (r *Registry) queryIdentity(key string) (ReadyInfo, error) {
	socketPath := r.SocketPath(key)
	conn, err := net.DialTimeout("unix", socketPath, identityDialTimeout)
	if err != nil {
		return ReadyInfo{}, err
	}
	defer conn.Close()

	if err := conn.SetReadDeadline(time.Now().Add(identityReadTimeout)); err != nil {
		return ReadyInfo{}, err
	}

	if _, err := conn.Write(encodeFrame(FrameQueryIdentity, nil)); err != nil {
		return ReadyInfo{}, err
	}

	header := make([]byte, 5)
	if _, err := io.ReadFull(conn, header); err != nil {
		return ReadyInfo{}, err
	}
	ftype := header[0]
	plen := binary.BigEndian.Uint32(header[1:5])
	if plen > identityMaxPayload {
		return ReadyInfo{}, fmt.Errorf("identity response too large: %d", plen)
	}
	if ftype != FrameIdentity {
		return ReadyInfo{}, fmt.Errorf("unexpected identity frame type: %02x", ftype)
	}

	payload := make([]byte, plen)
	if plen > 0 {
		if _, err := io.ReadFull(conn, payload); err != nil {
			return ReadyInfo{}, err
		}
	}
	return UnmarshalReadyInfo(payload)
}

// sendClose dials a daemon and sends FrameClose. Returns true on success.
func (r *Registry) sendClose(key string) bool {
	conn, err := net.DialTimeout("unix", r.SocketPath(key), identityDialTimeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	_, err = conn.Write(encodeFrame(FrameClose, nil))
	return err == nil
}

// isSocketLive reports whether the socket accepts a plain connection. This is
// intentionally cheap: it says nothing about generation.
func (r *Registry) isSocketLive(key string) bool {
	conn, err := net.DialTimeout("unix", r.SocketPath(key), identityDialTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// getLifecycle returns the lifecycle record for key, or nil if missing/unset.
func (r *Registry) getLifecycle(key string) *LifecycleRecord {
	if r.lifecycleStore == nil {
		return nil
	}
	rec, err := r.lifecycleStore.Get(key)
	if err != nil {
		return nil
	}
	return rec
}

// GenerationFor returns the current stable generation for a daemon key, or
// empty when no stable generation is recorded (legacy daemons). It prefers
// the lifecycle record and falls back to the sidecar metadata so both v2 and
// legacy daemons can be generation-gated.
func (r *Registry) GenerationFor(key string) string {
	if rec := r.getLifecycle(key); rec != nil && rec.Generation != "" {
		return rec.Generation
	}
	if meta := r.readMetadata(key); meta != nil {
		return meta.Generation
	}
	return ""
}

// readMetadata reads the sidecar JSON file for key.
func (r *Registry) readMetadata(key string) *sessionMeta {
	data, err := os.ReadFile(r.metadataPath(key))
	if err != nil {
		return nil
	}
	var meta sessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil
	}
	return &meta
}

// processEvidenceLive returns true when the lifecycle record's daemon process
// is provably alive and the starttime matches (when available). A reused PID
// is never considered live.
func (r *Registry) processEvidenceLive(rec *LifecycleRecord) bool {
	if rec.DaemonPID <= 0 {
		return false
	}
	if !processAlive(rec.DaemonPID) {
		return false
	}
	return sameStartTime(rec.DaemonPID, rec.ProcStartTime)
}

// sameStartTime returns true when no expected starttime is recorded, when the
// platform cannot read one, or when the current starttime matches expected.
func sameStartTime(pid int, expected int64) bool {
	if expected <= 0 {
		return true
	}
	current := procStartTime(pid)
	if current == 0 {
		return true
	}
	return current == expected
}

// sameGeneration reports whether the lifecycle record matches the requested
// generation and stable identity.
func sameGeneration(rec LifecycleRecord, generation string) bool {
	if rec.Generation == "" || generation == "" {
		return false
	}
	return rec.Generation == generation
}

// sameReadyInfo reports whether the daemon's identity claim matches the
// binding. The daemon key is not checked over the wire; owner/session/generation
// are the authoritative identifiers.
func sameReadyInfo(info ReadyInfo, binding StableBinding) bool {
	return info.Owner == binding.Owner &&
		info.SessionID == binding.SessionID &&
		info.Generation == binding.Generation
}

// cleanStaleFiles removes socket and metadata for a key after proving the old
// daemon is gone. It does not touch lifecycle records so crash preservation
// semantics remain intact.
func (r *Registry) cleanStaleFiles(key, reason string) {
	os.Remove(r.SocketPath(key))
	os.Remove(r.metadataPath(key))
	logrus.WithFields(logrus.Fields{
		"component":  "registry",
		"daemon_key": key,
		"reason":     reason,
	}).Debug("cleaned stale daemon files")
}

// clearStartingRecord marks a starting lifecycle record as crashed when the
// spawn itself failed, so it cannot be mistaken for a live binding later.
func (r *Registry) clearStartingRecord(key string) {
	if r.lifecycleStore == nil {
		return
	}
	rec, err := r.lifecycleStore.Get(key)
	if err != nil || rec == nil {
		return
	}
	if rec.State == LifecycleStarting {
		_ = r.lifecycleStore.Transition(key, LifecycleStarting, LifecycleCrashed)
	}
}
