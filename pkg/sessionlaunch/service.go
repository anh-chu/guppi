// Package sessionlaunch provides the single, tested path for creating local
// daemon sessions and dispatching remote session launches to peers.
package sessionlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/state"
)

// Typed launch errors. HTTP/router layers map these to status codes without
// switching on error strings.
var (
	ErrInvalidInput    = errors.New("invalid session launch input")
	ErrPeerUnavailable = errors.New("peer not connected")
	ErrPeerQueueFull   = errors.New("peer send queue full")
	ErrWorktree        = errors.New("worktree creation failed")
	ErrSpawn           = errors.New("daemon spawn failed")
)

// Request carries everything needed to launch one session.
type Request struct {
	Name           string
	TargetOwner    state.OwnerID // empty means the local node
	Path           string
	Command        string
	AgentType      string
	WorktreeBranch string
	ScheduleID     string
	CommandID      string // stable command id for state commands (optional)

	Cols uint16
	Rows uint16
}

// Result reports the session that was launched.
type Result struct {
	Name        string
	TargetOwner state.OwnerID
	Path        string
	Remote      bool
}

// Commander executes state commands against the canonical catalog.
type Commander interface {
	ExecuteSessionCommand(ctx context.Context, cmd state.SessionCommand) (state.CommandResult, error)
}

// RemoteLauncher dispatches a launch for a non-local owner via the reliable
// remote-create path (pkg/state.RemoteCreateCoordinator, peer.Manager's
// OwnerID-routed RequestRemoteCreate). A nil launcher means remote launching
// is unavailable; requesting a remote target with a nil RemoteLauncher is an
// explicit error, never a silent local fallback.
type RemoteLauncher func(ctx context.Context, req Request) (Result, error)

// Service is the sole owner of session launch semantics. A local target
// (TargetOwner == "" or TargetOwner == LocalOwner) is routed through
// Commander; any other target is routed through RemoteCreate. There is no
// other path: no browser fan-out, no client-side uniqueness checking, and no
// fire-and-forget delivery -- SessionCommandService is the sole authority
// for unique naming, and RemoteCreate is required for any remote target.
type Service struct {
	LocalOwner   state.OwnerID
	Commander    Commander
	RemoteCreate RemoteLauncher
}

// Create validates, resolves, and launches one session. Panics if Commander
// is nil: Commander is a required dependency, not an optional one.
func (s *Service) Create(ctx context.Context, req Request) (Result, error) {
	if s.Commander == nil {
		panic("sessionlaunch: Service.Commander is required and must not be nil")
	}
	req = s.normalize(req)
	if err := s.validate(req); err != nil {
		return Result{}, err
	}

	if req.TargetOwner != "" && req.TargetOwner != s.LocalOwner {
		return s.createRemote(ctx, req)
	}
	req.TargetOwner = ""
	return s.createLocal(ctx, req)
}

func (s *Service) normalize(req Request) Request {
	req.Name = strings.TrimSpace(req.Name)
	req.Path = strings.TrimSpace(req.Path)
	req.Command = strings.TrimSpace(req.Command)
	req.AgentType = strings.TrimSpace(req.AgentType)
	req.WorktreeBranch = strings.TrimSpace(req.WorktreeBranch)
	return req
}

func (s *Service) validate(req Request) error {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = defaultSessionName(req.Command, req.Path)
	}
	if name == "" {
		return fmt.Errorf("%w: name or path is required", ErrInvalidInput)
	}
	if err := model.ValidateSessionName(name); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return nil
}

func (s *Service) createRemote(ctx context.Context, req Request) (Result, error) {
	req.Name = s.resolveName(req)
	if s.RemoteCreate == nil {
		return Result{}, fmt.Errorf("%w: %s", ErrPeerUnavailable, req.TargetOwner)
	}
	res, err := s.RemoteCreate(ctx, req)
	if err != nil {
		return Result{}, err
	}
	res.Remote = true
	return res, nil
}

func (s *Service) createLocal(ctx context.Context, req Request) (Result, error) {
	req.Name = s.resolveName(req)
	return s.createLocalViaCommander(ctx, req)
}

func (s *Service) createLocalViaCommander(ctx context.Context, req Request) (Result, error) {
	cmdID := state.CommandID(req.CommandID)
	if cmdID == "" {
		cmdID = state.NewCommandID()
	}
	params, _ := json.Marshal(state.CreateParams{
		Name:           req.Name,
		Shell:          req.Command,
		Cwd:            req.Path,
		WorktreeBranch: req.WorktreeBranch,
		Cols:           req.Cols,
		Rows:           req.Rows,
		AgentType:      req.AgentType,
		ScheduleID:     req.ScheduleID,
	})
	res, err := s.Commander.ExecuteSessionCommand(ctx, state.SessionCommand{
		ID:     cmdID,
		Ref:    state.SessionRef{},
		Action: state.ActionCreate,
		Params: params,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Name: res.DisplayName, Path: res.Path}, nil
}

// resolveName fills in a default name (derived from command/path) when the
// caller didn't supply one. Uniqueness is not checked here: the canonical
// command service (Commander) is the sole authority for unique naming.
func (s *Service) resolveName(req Request) string {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = defaultSessionName(req.Command, req.Path)
	}
	return name
}

func defaultSessionName(command, projectPath string) string {
	base := strings.TrimSpace(command)
	if idx := strings.IndexByte(base, ' '); idx >= 0 {
		base = base[:idx]
	}
	base = strings.Trim(base, `"'`)
	base = strings.TrimSpace(base)
	if base == "" {
		base = "session"
	}
	if projectPath == "" {
		return ""
	}

	projectBase := strings.TrimSpace(projectPath)
	projectBase = strings.TrimRight(projectBase, "/")
	if idx := strings.LastIndex(projectBase, "/"); idx >= 0 {
		projectBase = projectBase[idx+1:]
	}
	projectBase = sanitizeSessionSegment(projectBase)
	if projectBase == "" {
		projectBase = "workspace"
	}

	return sanitizeSessionSegment(base) + "-" + projectBase
}

func sanitizeSessionSegment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-', r == '_', r == '.', r == '/', r == ' ':
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
