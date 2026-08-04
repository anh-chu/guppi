package pty

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// Lifecycle states for session daemons.
const (
	LifecycleStarting             = "starting"
	LifecycleActive               = "active"
	LifecycleTerminationRequested = "termination_requested"
	LifecycleCleanlyEnded         = "cleanly_ended"
	LifecycleCrashed              = "crashed"
	LifecycleRecovered            = "recovered"
	LifecycleDismissed            = "dismissed"
)

// terminalLifecycleStates are states where the daemon is no longer live.
var terminalLifecycleStates = map[string]bool{
	LifecycleTerminationRequested: true,
	LifecycleCleanlyEnded:         true,
	LifecycleDismissed:            true,
}

// LifecycleRecord holds durable state for a session daemon.
// Written as a JSON file under the lifecycle store directory.
type LifecycleRecord struct {
	ID            string    `json:"id"`
	State         string    `json:"state"`
	Shell         string    `json:"shell"`
	Cwd           string    `json:"cwd"`
	Cols          uint16    `json:"cols"`
	Rows          uint16    `json:"rows"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	DaemonPID     int       `json:"daemon_pid"`
	SystemdUnit   string    `json:"systemd_unit,omitempty"` // systemd scope unit name (for cleanup)
	Generation    string    `json:"generation"`
	ProcStartTime int64     `json:"proc_start_time,omitempty"` // /proc/pid/stat field 22 (starttime in clock ticks)

	// Stable identity fields support the v2 binding handshake. They are
	// immutable for a given lifecycle record and are empty for legacy
	// daemons created before the v2 transition.
	Owner     string `json:"owner,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	DaemonKey string `json:"daemon_key,omitempty"`
	CommandID string `json:"command_id,omitempty"`
}

// LifecycleStore persists LifecycleRecord files to a durable directory.
// All writes are atomic (write .tmp + rename).  Files are mode 0600.
type LifecycleStore struct {
	dir string
}

// DefaultStateDir returns the platform-appropriate state directory for
// lifecycle records, respecting XDG_STATE_HOME.
func DefaultStateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "termyard", "sessions")
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "termyard", "sessions")
	}
	return filepath.Join(os.TempDir(), "termyard-state")
}

// NewLifecycleStore creates a lifecycle store rooted at dir.  The directory
// is created (mode 0700) if it does not exist.
func NewLifecycleStore(dir string) (*LifecycleStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create lifecycle store dir %s: %w", dir, err)
	}
	return &LifecycleStore{dir: dir}, nil
}

// Dir returns the store directory.
func (s *LifecycleStore) Dir() string { return s.dir }

// path returns the full path for a session's lifecycle file.
func (s *LifecycleStore) path(id string) string {
	return filepath.Join(s.dir, id+".lifecycle.json")
}

// NewGeneration returns a random 8-byte hex nonce for lifecycle records.
func NewGeneration() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand is extremely unlikely to fail; fall back to
		// a time-based value that is still unique within a machine.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// RecordActive creates (or overwrites) a lifecycle record in state "active".
// The write is atomic: data is written to a .tmp file then renamed into place.
func (s *LifecycleStore) RecordActive(rec LifecycleRecord) error {
	rec.State = LifecycleActive
	rec.UpdatedAt = time.Now()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = rec.UpdatedAt
	}
	if rec.Generation == "" {
		rec.Generation = NewGeneration()
	}
	if rec.DaemonKey == "" {
		rec.DaemonKey = rec.ID
	}
	return s.writeAtomic(rec)
}

// Transition performs a compare-and-swap state transition.
// It returns an error if the current state does not match fromState (except
// when fromState is empty, which bypasses the check).
// The .json file is atomically updated.
func (s *LifecycleStore) Transition(id, fromState, toState string) error {
	current, err := s.Get(id)
	if err != nil {
		return fmt.Errorf("get record for %s: %w", id, err)
	}
	if fromState != "" && current.State != fromState {
		return fmt.Errorf("expected state %q for %s, got %q", fromState, id, current.State)
	}
	current.State = toState
	current.UpdatedAt = time.Now()
	return s.writeAtomic(*current)
}

// Get reads the lifecycle record for a session.
func (s *LifecycleStore) Get(id string) (*LifecycleRecord, error) {
	p := s.path(id)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var rec LifecycleRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("parse lifecycle record %s: %w", p, err)
	}
	return &rec, nil
}

// ListByState returns all lifecycle records whose State field matches state.
func (s *LifecycleStore) ListByState(state string) []LifecycleRecord {
	entries, err := filepath.Glob(filepath.Join(s.dir, "*.lifecycle.json"))
	if err != nil {
		return nil
	}

	var out []LifecycleRecord
	for _, p := range entries {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var rec LifecycleRecord
		if json.Unmarshal(data, &rec) != nil {
			continue
		}
		if rec.State == state {
			out = append(out, rec)
		}
	}
	return out
}

// Remove deletes the lifecycle file for a session.
func (s *LifecycleStore) Remove(id string) error {
	return os.Remove(s.path(id))
}

// procStartTime reads field 22 (starttime) from /proc/<pid>/stat.
// Returns 0 if the file cannot be read or parsed.
func procStartTime(pid int) int64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// Fields are space-separated; field 2 (comm) is in parens and may
	// contain spaces, so find the last ')' first.
	s := string(data)
	idx := strings.LastIndex(s, ")")
	if idx < 0 || idx+2 >= len(s) {
		return 0
	}
	fields := strings.Fields(s[idx+2:])
	// starttime is field 22 in stat (1-indexed), which is fields[19]
	// after skipping the first 3 fields (state, ppid, pgrp at positions 3-5).
	// After ')' we have fields starting at position 3, so starttime is at index 19.
	if len(fields) < 20 {
		return 0
	}
	v, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// DetectCrashes finds records in state "active" whose daemon process is no
// longer alive, transitions them to "crashed", and returns the affected
// records (in their new "crashed" state).
//
// Process liveness is checked with syscall.Kill(pid, 0), guarded by
// /proc/<pid>/stat start time to prevent PID-reuse false negatives.
func (s *LifecycleStore) DetectCrashes() []LifecycleRecord {
	actives := s.ListByState(LifecycleActive)

	var crashed []LifecycleRecord
	for _, rec := range actives {
		if rec.DaemonPID <= 0 {
			continue
		}
		if processAlive(rec.DaemonPID) {
			// PID exists, but is it the same process? Check start time.
			if rec.ProcStartTime > 0 {
				current := procStartTime(rec.DaemonPID)
				if current > 0 && current != rec.ProcStartTime {
					// PID was reused by a different process — treat as dead.
					logrus.WithFields(logrus.Fields{
						"id":             rec.ID,
						"daemon_pid":     rec.DaemonPID,
						"expected_start": rec.ProcStartTime,
						"actual_start":   current,
					}).Warn("PID reused by different process — treating as crashed")
				} else {
					continue // same process, still alive
				}
			} else {
				continue // no start time recorded, trust PID
			}
		}
		// Process is dead — transition to crashed.
		logrus.WithFields(logrus.Fields{
			"id":         rec.ID,
			"daemon_pid": rec.DaemonPID,
		}).Warn("daemon process died — marking session as crashed")
		if err := s.Transition(rec.ID, LifecycleActive, LifecycleCrashed); err != nil {
			logrus.WithError(err).WithField("id", rec.ID).Warn("failed to transition to crashed")
			continue
		}
		rec.State = LifecycleCrashed
		rec.UpdatedAt = time.Now()
		crashed = append(crashed, rec)
	}
	return crashed
}

// writeAtomic writes rec to a .tmp file and then renames it into place,
// guaranteeing an all-or-nothing update.
func (s *LifecycleStore) writeAtomic(rec LifecycleRecord) error {
	p := s.path(rec.ID)
	tmp := p + ".tmp"

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lifecycle record: %w", err)
	}

	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp) // best-effort cleanup
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
