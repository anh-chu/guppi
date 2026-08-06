// Package sessionlaunch provides the single, tested path for creating local
// daemon sessions and dispatching remote session launches to peers.
package sessionlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

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
	Host           string // empty, the current node ID, or a remote target host ID
	Path           string
	Command        string
	AgentType      string
	WorktreeBranch string
	ScheduleID     string
	LocalHost      string // the current node ID, used both for target classification (local vs remote) and for local schedule-metadata key construction
	Fallback       string // used only for local requests when a name cannot be derived
	CommandID      string // stable command id for state commands (optional)

	Cols uint16
	Rows uint16
}

// Result reports the session that was launched.
type Result struct {
	Name   string
	Host   string
	Path   string
	Remote bool
}

// ScheduleAttr is the metadata snapshot the launch service stores and fans out.
type ScheduleAttr struct {
	Background bool
	Hidden     bool
	ScheduleID string
	UpdatedAt  time.Time
}

// AttrStoreFunc lets callers satisfy ScheduleAttrStore with a plain function.
type AttrStoreFunc func(key, scheduleID string) (ScheduleAttr, error)

// SetScheduleID implements ScheduleAttrStore.
func (f AttrStoreFunc) SetScheduleID(key, scheduleID string) (ScheduleAttr, error) {
	return f(key, scheduleID)
}

// ScheduleAttrStore records schedule ownership for a session key.
type ScheduleAttrStore interface {
	SetScheduleID(key, scheduleID string) (ScheduleAttr, error)
}

// BrowserHub pushes lightweight JSON notifications to browsers.
type BrowserHub interface {
	BroadcastJSON(v interface{})
}

// Identity provides the local host fingerprint for peer fan-out.
type Identity interface {
	Fingerprint() string
}

// RemoteLauncher dispatches a launch for a non-local host. A nil launcher
// means remote launching is unavailable.
type RemoteLauncher func(ctx context.Context, req Request) (Result, error)

// PeerFanout broadcasts a single-key schedule-attribute delta to other peers.
// nil skips fan-out.
type PeerFanout func(key string, attr ScheduleAttr)

// ExistingNames returns the current session names for a host. An empty host
// means the local node. nil is treated as "no existing sessions".
type ExistingNames func(host string) []string

// RefreshFunc triggers a state refresh so browsers see the new session.
// nil skips the refresh.
type RefreshFunc func()

// Commander executes state commands against the canonical catalog.
type Commander interface {
	ExecuteSessionCommand(ctx context.Context, cmd state.SessionCommand) (state.CommandResult, error)
}

// Service is the sole owner of session launch semantics.
type Service struct {
	Attrs     ScheduleAttrStore
	Hub       BrowserHub
	Identity  Identity
	Remote    RemoteLauncher
	Fanout    PeerFanout
	Names     ExistingNames
	Refresh   RefreshFunc
	Commander Commander // required: all session creation is routed through the canonical state commander

	// ReliableRemote dispatches a remote-host create through the
	// remote-create coordinator + command RPC (pkg/state.RemoteCreateCoordinator,
	// pkg/peer's Manager.SendRemoteCreate), instead of the fire-and-forget
	// Remote path. When ReliableRemote is set, createRemote prefers it
	// unconditionally, since Remote's fire-and-forget delivery cannot confirm
	// the remote session was actually created. nil falls back to the
	// fire-and-forget Remote path for any remote target.
	ReliableRemote RemoteLauncher
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

	if req.Host != "" && req.Host != req.LocalHost {
		return s.createRemote(ctx, req)
	}
	req.Host = ""
	return s.createLocal(ctx, req)
}

func (s *Service) normalize(req Request) Request {
	req.Name = strings.TrimSpace(req.Name)
	req.Host = strings.TrimSpace(req.Host)
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
		name = strings.TrimSpace(req.Fallback)
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

	// The fire-and-forget RemoteLauncher (Remote) reports success once the
	// frame is merely enqueued on the peer connection, not once the remote
	// session genuinely exists. When a reliable remote-create path is wired
	// (ReliableRemote != nil), always prefer it for any non-local target.
	if s.ReliableRemote != nil {
		res, err := s.ReliableRemote(ctx, req)
		if err != nil {
			return Result{}, err
		}
		res.Remote = true
		s.applyRemoteScheduleAttr(req, res)
		return res, nil
	}

	if s.Remote == nil {
		return Result{}, fmt.Errorf("%w: %s", ErrPeerUnavailable, req.Host)
	}

	res, err := s.Remote(ctx, req)
	if err != nil {
		return Result{}, err
	}
	res.Remote = true
	s.applyRemoteScheduleAttr(req, res)
	return res, nil
}

// applyRemoteScheduleAttr records and fans out schedule ownership for a
// remotely-created session. Shared by both the fire-and-forget and reliable
// remote-create paths.
func (s *Service) applyRemoteScheduleAttr(req Request, res Result) {
	if s.Attrs == nil || req.ScheduleID == "" {
		return
	}
	key := sessionKey(req.Host, res.Name)
	attr, err := s.Attrs.SetScheduleID(key, req.ScheduleID)
	if err != nil {
		logrus.WithError(err).Warn("failed to store schedule id for remote session")
		return
	}
	if s.Fanout != nil {
		s.Fanout(key, attr)
	}
	if s.Hub != nil {
		s.Hub.BroadcastJSON(map[string]interface{}{"type": "session-attrs-updated", "key": key})
	}
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
	if s.Attrs != nil && req.ScheduleID != "" {
		key := sessionKey(req.LocalHost, res.DisplayName)
		attr, err := s.Attrs.SetScheduleID(key, req.ScheduleID)
		if err != nil {
			logrus.WithError(err).Warn("failed to store schedule id")
		} else {
			if s.Fanout != nil {
				s.Fanout(key, attr)
			}
			if s.Hub != nil {
				s.Hub.BroadcastJSON(map[string]interface{}{"type": "session-attrs-updated", "key": key})
			}
		}
	}
	if s.Refresh != nil {
		s.Refresh()
	}
	return Result{Name: res.DisplayName, Host: req.Host, Path: res.Path}, nil
}

func (s *Service) resolveName(req Request) string {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = defaultSessionName(req.Command, req.Path)
	}
	if name == "" {
		name = strings.TrimSpace(req.Fallback)
	}
	if name == "" {
		return ""
	}
	existing := []string(nil)
	if s.Names != nil {
		existing = s.Names(req.Host)
	}
	return ensureUniqueSessionName(name, existing)
}

func sessionKey(host, name string) string {
	if host != "" {
		return host + "/" + name
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

func ensureUniqueSessionName(name string, existing []string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	used := make(map[string]struct{}, len(existing))
	for _, candidate := range existing {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			used[candidate] = struct{}{}
		}
	}

	if _, exists := used[name]; !exists {
		return name
	}

	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
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
