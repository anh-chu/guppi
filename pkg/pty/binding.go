package pty

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// StableBinding identifies one exact daemon generation. The daemon key is
// the socket/file name used for this generation; the session ID is the
// durable logical identity that survives renames.
type StableBinding struct {
	Owner      string
	SessionID  string
	Generation string
	DaemonKey  string // socket name; defaults to SessionID when empty
}

// EffectiveDaemonKey returns the socket identifier for this binding,
// defaulting to the session ID.
func (b StableBinding) EffectiveDaemonKey() string {
	if b.DaemonKey != "" {
		return b.DaemonKey
	}
	return b.SessionID
}

// IsStable reports whether the binding carries a non-empty stable identity.
func (b StableBinding) IsStable() bool {
	return b.Owner != "" && b.SessionID != "" && b.Generation != ""
}

// StartRequest asks the registry to spawn a daemon bound to a stable identity.
type StartRequest struct {
	StableBinding
	Shell     string
	Cwd       string
	Cols      uint16
	Rows      uint16
	CommandID string // idempotent create command receipt
}

func (r StartRequest) validate() error {
	if r.Owner == "" {
		return fmt.Errorf("owner is required")
	}
	if r.SessionID == "" {
		return fmt.Errorf("session id is required")
	}
	key := r.EffectiveDaemonKey()
	if key == "" {
		return fmt.Errorf("daemon key is required")
	}
	if !validSessionID(key) {
		return fmt.Errorf("invalid daemon key %q", key)
	}
	return nil
}

// ReadyInfo is returned by a daemon in response to an identity query.
type ReadyInfo struct {
	Owner      string `json:"owner"`
	SessionID  string `json:"session_id"`
	Generation string `json:"generation"`
	DaemonPID  int    `json:"daemon_pid"`
	ShellPID   int    `json:"shell_pid"`
}

// Marshal returns a JSON payload for the wire identity frame.
func (r ReadyInfo) Marshal() ([]byte, error) { return json.Marshal(r) }

// UnmarshalReadyInfo parses a JSON identity payload.
func UnmarshalReadyInfo(data []byte) (ReadyInfo, error) {
	var ri ReadyInfo
	if err := json.Unmarshal(data, &ri); err != nil {
		return ri, err
	}
	return ri, nil
}

// ProbeStatus classifies the result of Probe.
type ProbeStatus string

const (
	ProbeLive    ProbeStatus = "live"
	ProbeClean   ProbeStatus = "clean"
	ProbeCrashed ProbeStatus = "crashed"
	ProbeUnknown ProbeStatus = "unknown"
)

// ProbeEvidence is a non-destructive summary of a binding check.
type ProbeEvidence struct {
	Status    ProbeStatus
	Binding   StableBinding
	DaemonPID int
	ShellPID  int
	Reason    string
}

// IsLive reports whether the probe found a matching live daemon.
func (e ProbeEvidence) IsLive() bool { return e.Status == ProbeLive }

// TerminateOutcome is the typed result of Terminate.
type TerminateOutcome string

const (
	TerminateAcknowledged       TerminateOutcome = "acknowledged"
	TerminateAlreadyEnded       TerminateOutcome = "already_ended"
	TerminateGenerationMismatch TerminateOutcome = "generation_mismatch"
	TerminateUnknown            TerminateOutcome = "unknown"
)

// StableRegistry is the v2 daemon identity contract implemented by Registry.
type StableRegistry interface {
	Start(ctx context.Context, req StartRequest) (ReadyInfo, error)
	Probe(binding StableBinding) ProbeEvidence
	Terminate(ctx context.Context, binding StableBinding) TerminateOutcome
}

// Common stable-binding errors.
var (
	ErrAlreadyBound       = errors.New("stable binding already bound")
	ErrBindingInUse       = errors.New("daemon key already in use by another binding")
	ErrGenerationMismatch = errors.New("generation mismatch")
)

// ensure Registry implements StableRegistry.
var _ StableRegistry = (*Registry)(nil)
