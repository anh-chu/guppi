// Package sessionlaunch provides the single, tested path for creating local
// daemon sessions and dispatching remote session launches to peers.
package sessionlaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/git"
	"github.com/anh-chu/termyard/pkg/model"
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

// DaemonRegistry is the session backend needed to spawn and kill daemon sessions.
type DaemonRegistry interface {
	Create(name, shell, cwd string, cols, rows uint16) error
	Kill(name string) error
}

// StateManager manages session state, including agent overrides and removal.
type StateManager interface {
	SetSessionAgentType(sessionName, agentType string)
	GetSessions() []*model.Session
	RemoveSession(name string)
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

// Service is the sole owner of session launch and kill semantics.
type Service struct {
	DaemonReg DaemonRegistry
	StateMgr  StateManager
	Attrs     ScheduleAttrStore
	Hub       BrowserHub
	Identity  Identity
	Remote    RemoteLauncher
	Fanout    PeerFanout
	Names     ExistingNames
	Refresh   RefreshFunc
	Forget    func(name string) error // optional; if nil, ForgetSession is skipped during Kill
}

// Create validates, resolves, and launches one session.
func (s *Service) Create(ctx context.Context, req Request) (Result, error) {
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
	if s.Remote == nil {
		return Result{}, fmt.Errorf("%w: %s", ErrPeerUnavailable, req.Host)
	}

	req.Name = s.resolveName(req)

	res, err := s.Remote(ctx, req)
	if err != nil {
		return Result{}, err
	}
	res.Remote = true

	if s.Attrs != nil && req.ScheduleID != "" {
		key := sessionKey(req.Host, res.Name)
		attr, err := s.Attrs.SetScheduleID(key, req.ScheduleID)
		if err != nil {
			logrus.WithError(err).Warn("failed to store schedule id for remote session")
		} else {
			if s.Fanout != nil {
				s.Fanout(key, attr)
			}
			if s.Hub != nil {
				s.Hub.BroadcastJSON(map[string]interface{}{"type": "session-attrs-updated", "key": key})
			}
		}
	}

	return res, nil
}

func (s *Service) createLocal(ctx context.Context, req Request) (Result, error) {
	req.Name = s.resolveName(req)

	cwd, err := s.prepareLocalPath(req)
	if err != nil {
		return Result{}, err
	}

	if err := s.daemonCreate(ctx, req.Name, req.Command, cwd, req.Cols, req.Rows); err != nil {
		if cwd != req.Path && req.Path != "" {
			_ = git.RemoveWorktree(cwd)
		}
		return Result{}, err
	}

	if s.StateMgr != nil && req.AgentType != "" {
		s.StateMgr.SetSessionAgentType(req.Name, req.AgentType)
	}

	if s.Attrs != nil && req.ScheduleID != "" {
		key := sessionKey(req.LocalHost, req.Name)
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

	return Result{Name: req.Name, Host: req.Host, Path: cwd}, nil
}

// Kill terminates a session daemon, removes it from state, and forgets it from recovery.
// It combines the results of all cleanup steps and logs errors with session name and reason.
// If any step fails, remaining steps still execute (error joined and returned at end).
func (s *Service) Kill(name, reason string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: session name required", ErrInvalidInput)
	}

	log := logrus.WithFields(logrus.Fields{
		"session": name,
		"reason":  reason,
	})

	var errs []error

	// Kill daemon (includes lifecycle transition internally).
	if s.DaemonReg != nil {
		if err := s.DaemonReg.Kill(name); err != nil {
			log.WithError(err).Warn("daemon kill failed")
			errs = append(errs, err)
		}
	}

	// Remove from state manager.
	if s.StateMgr != nil {
		s.StateMgr.RemoveSession(name)
	}

	// Forget from recovery manifest.
	if s.Forget != nil {
		if err := s.Forget(name); err != nil {
			log.WithError(err).Warn("failed to forget session from recovery")
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (s *Service) prepareLocalPath(req Request) (string, error) {
	cwd := req.Path
	if cwd == "~" {
		cwd = ""
	}

	if req.WorktreeBranch != "" && cwd != "" {
		expanded := expandPath(cwd)
		sanitized := strings.ReplaceAll(req.WorktreeBranch, "/", "-")
		worktreesDir := filepath.Join(expanded, ".worktrees")
		if err := os.MkdirAll(worktreesDir, 0755); err != nil {
			return "", fmt.Errorf("%w: mkdir .worktrees: %w", ErrWorktree, err)
		}
		destPath := filepath.Join(worktreesDir, sanitized)
		if err := git.CreateWorktree(expanded, req.WorktreeBranch, destPath); err != nil {
			return "", fmt.Errorf("%w: git worktree add: %w", ErrWorktree, err)
		}
		cwd = destPath
	}

	return cwd, nil
}

func (s *Service) daemonCreate(ctx context.Context, name, command, cwd string, cols, rows uint16) error {
	shell := command
	if shell == "" || shell == "shell" {
		shell = ""
	}
	if err := s.DaemonReg.Create(name, shell, cwd, cols, rows); err != nil {
		return fmt.Errorf("%w: %w", ErrSpawn, err)
	}
	return nil
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

func expandPath(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			p = home + p[1:]
		}
	}
	if !filepath.IsAbs(p) {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p)
		}
	}
	return p
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
