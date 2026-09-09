package state

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/git"
	"github.com/anh-chu/termyard/pkg/namer"
)

// persistedName is the on-disk shape for name metadata that must survive a
// server restart, plus the prompt/message context the AI namer reads so a
// post-restart rename has something to work from instead of a stale name with
// no context. Hooks still refresh these on the next agent turn.
type persistedName struct {
	DisplayName      string `json:"display_name"`
	UserSetName      bool   `json:"user_set_name"`
	NameAssigned     bool   `json:"name_assigned"`
	Renamed          bool   `json:"renamed"`
	UserPrompt       string `json:"user_prompt,omitempty"`
	LastUserPrompt   string `json:"last_user_prompt,omitempty"`
	LastAgentMessage string `json:"last_agent_message,omitempty"`
}

// loadNames seeds meta with persisted display names. Called once at startup
// before any concurrent access, so it takes no lock.
func (m *Manager) loadNames() {
	if m.namesPath == "" {
		return
	}
	raw, err := os.ReadFile(m.namesPath)
	if err != nil {
		return
	}
	var saved map[string]persistedName
	if err := json.Unmarshal(raw, &saved); err != nil {
		logrus.WithError(err).Debug("session names: parse failed")
		return
	}
	for name, pn := range saved {
		meta := m.meta[name]
		meta.DisplayName = pn.DisplayName
		meta.UserSetName = pn.UserSetName
		meta.NameAssigned = pn.NameAssigned
		meta.Renamed = pn.Renamed
		meta.UserPrompt = pn.UserPrompt
		meta.LastUserPrompt = pn.LastUserPrompt
		meta.LastAgentMessage = pn.LastAgentMessage
		m.meta[name] = meta
	}
}

// saveNames writes current name metadata to disk. Takes its own read lock, so
// callers must NOT hold m.mu. Best-effort: errors are logged at debug.
func (m *Manager) saveNames() {
	if m.namesPath == "" {
		return
	}
	m.mu.RLock()
	snapshot := make(map[string]persistedName, len(m.meta))
	for name, meta := range m.meta {
		if meta.DisplayName == "" && !meta.UserSetName {
			continue
		}
		snapshot[name] = persistedName{
			DisplayName:      meta.DisplayName,
			UserSetName:      meta.UserSetName,
			NameAssigned:     meta.NameAssigned,
			Renamed:          meta.Renamed,
			UserPrompt:       meta.UserPrompt,
			LastUserPrompt:   meta.LastUserPrompt,
			LastAgentMessage: meta.LastAgentMessage,
		}
	}
	m.mu.RUnlock()

	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		logrus.WithError(err).Debug("session names: marshal failed")
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.namesPath), 0o755); err != nil {
		logrus.WithError(err).Debug("session names: mkdir failed")
		return
	}
	if err := os.WriteFile(m.namesPath, raw, 0o644); err != nil {
		logrus.WithError(err).Debug("session names: write failed")
	}
}

// SetNamer attaches an optional AI session namer. Safe to pass a disabled
// namer or call with nil; naming becomes a no-op.
func (m *Manager) SetNamer(n *namer.Namer) {
	m.mu.Lock()
	m.namer = n
	m.mu.Unlock()
}

// SetRenameHook installs an optional callback fired after a session rename is
// applied. Used to migrate external per-session stores (e.g. shared session
// attributes) keyed by session name.
func (m *Manager) SetRenameHook(fn func(oldName, newName string)) {
	m.mu.Lock()
	m.onRename = fn
	m.mu.Unlock()
}

// logAutomaticNamingFailure records the failure in the authoritative gate and
// in the SessionMetadata mirror fields, then logs the failure silently (no
// frontend notice). Manual/explicit renames never call this path.
func (m *Manager) logAutomaticNamingFailure(sessionName, kind string, err error) {
	gate := m.automaticGate()
	gate.Failure(sessionName)

	m.mu.Lock()
	meta := m.meta[sessionName]
	meta.NamingFailureCount++
	count := meta.NamingFailureCount
	m.meta[sessionName] = meta
	m.mu.Unlock()

	nextEligible := gate.NextEligible(sessionName)
	logrus.WithFields(logrus.Fields{
		"session":       sessionName,
		"kind":          kind,
		"failure_count": count,
		"next_eligible": nextEligible,
	}).WithError(err).Warn("automatic session naming failed")
}

// triggerAgentNaming runs the AI namer for an agent session, on its first user
// prompt and on later completed turns as the work evolves. It refreshes the
// DisplayName each time; the underlying rename stays one-shot (guarded by
// meta.Renamed inside applyGeneratedName). Manual names (UserSetName) win.
func (m *Manager) triggerAgentNaming(sessionName string) {
	m.mu.RLock()
	n := m.namer
	meta := m.meta[sessionName]
	sess := m.sessions[sessionName]
	projectPath := meta.ProjectPath
	if sess != nil && sess.ProjectPath != "" {
		projectPath = sess.ProjectPath
	}
	prompt := meta.LastUserPrompt
	if prompt == "" {
		prompt = meta.UserPrompt
	}
	nc := namer.Context{
		Kind:       namer.KindAgent,
		Workdir:    projectPath,
		Agent:      meta.AgentType,
		UserPrompt: prompt,
		AgentMsg:   meta.LastAgentMessage,
		Current:    meta.DisplayName,
		Taken:      m.otherDisplayNames(sessionName),
	}
	blocked := meta.UserSetName
	m.mu.RUnlock()

	if n == nil || !n.Enabled() || blocked {
		return
	}
	if projectPath != "" {
		if branch, err := git.CurrentBranch(projectPath); err == nil {
			nc.Branch = branch
		}
	}

	gate := m.automaticGate()
	if ok, _ := gate.Begin(sessionName); !ok {
		return
	}

	// Update the mirror field for compatibility/logging introspection; the
	// gate is authoritative for actual gating decisions.
	m.mu.Lock()
	meta = m.meta[sessionName]
	meta.LastNamingAttemptAt = time.Now()
	m.meta[sessionName] = meta
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	name, err := n.Generate(ctx, nc)
	if err != nil {
		m.logAutomaticNamingFailure(sessionName, "agent", err)
		return
	}
	logrus.WithFields(logrus.Fields{"session": sessionName, "name": name}).Info("agent session named")

	m.applyGeneratedName(sessionName, name)
}

// applyGeneratedName stores displayName for sessionName. The DisplayName is
// refreshed on every call (unless the user manually set it) so the label tracks
// the evolving work. All sessions are daemon-backed, so only the DisplayName
// is changed; the underlying session key (socket ID) is never renamed.
func (m *Manager) applyGeneratedName(sessionName, displayName string) {
	if displayName == "" {
		return
	}
	m.mu.Lock()
	meta, ok := m.meta[sessionName]
	if !ok {
		meta = SessionMetadata{}
	}
	if meta.UserSetName {
		m.mu.Unlock()
		return
	}
	now := time.Now()
	meta.LastNamedAt = now
	meta.NamingFailureCount = 0
	nameChanged := meta.DisplayName != displayName
	meta.DisplayName = displayName
	meta.NameAssigned = true
	m.meta[sessionName] = meta
	if sess := m.sessions[sessionName]; sess != nil {
		sess.DisplayName = displayName
	}

	// The name was accepted/stored; record success in the authoritative gate.
	// This is called even for forced/manual agent renames so the automatic
	// cooldown is respected after a successful explicit rename.
	m.automaticGate().Success(sessionName)

	m.mu.Unlock()

	// All sessions are daemon-backed now — the session key is the socket ID
	// and must not change. DisplayName is sufficient for the UI label.
	if nameChanged {
		m.saveNames()
		m.broadcast(StateEvent{Type: "sessions-changed"})
	}
}

// TriggerShellNaming runs the AI namer for a non-agent shell session and stores
// the result as DisplayName. Unlike agent naming this never renames the underlying
// session and is not one-shot — it refreshes on each new detected process.
// No-ops if the session has an agent type, the name is user-set, or the namer
// is disabled.
func (m *Manager) TriggerShellNaming(sessionName string, commands []string) {
	m.mu.RLock()
	n := m.namer
	meta := m.meta[sessionName]
	sess := m.sessions[sessionName]
	projectPath := meta.ProjectPath
	agentType := meta.AgentType
	if sess != nil {
		if sess.ProjectPath != "" {
			projectPath = sess.ProjectPath
		}
		if sess.AgentType != "" {
			agentType = sess.AgentType
		}
	}
	userSet := meta.UserSetName
	taken := m.otherDisplayNames(sessionName)
	m.mu.RUnlock()

	if n == nil || !n.Enabled() || sess == nil || userSet || agentType != "" || len(commands) == 0 {
		return
	}

	nc := namer.Context{Kind: namer.KindShell, Workdir: projectPath, Commands: commands, Current: meta.DisplayName, Taken: taken}
	if projectPath != "" {
		if b, err := git.CurrentBranch(projectPath); err == nil {
			nc.Branch = b
		}
	}

	gate := m.automaticGate()
	if ok, _ := gate.Begin(sessionName); !ok {
		return
	}

	m.mu.Lock()
	meta = m.meta[sessionName]
	meta.LastNamingAttemptAt = time.Now()
	m.meta[sessionName] = meta
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	name, err := n.Generate(ctx, nc)
	if err != nil {
		m.logAutomaticNamingFailure(sessionName, "shell", err)
		return
	}
	logrus.WithFields(logrus.Fields{"session": sessionName, "name": name}).Info("shell session named")

	m.applyGeneratedName(sessionName, name)
}

// SetDisplayName stores a manual display name for a session and flags it so the
// AI namer never overwrites it. Pass userSet=false to clear the manual flag.
func (m *Manager) SetDisplayName(sessionName, displayName string, userSet bool) {
	m.mu.Lock()
	meta := m.meta[sessionName]
	meta.DisplayName = displayName
	meta.UserSetName = userSet
	if userSet {
		meta.NameAssigned = true
	}
	m.meta[sessionName] = meta
	if sess := m.sessions[sessionName]; sess != nil {
		sess.DisplayName = displayName
		sess.UserSetName = userSet
	}
	m.mu.Unlock()
	m.saveNames()
	m.broadcast(StateEvent{Type: "sessions-changed"})
}

// RegenerateName forces an AI name refresh for a session on demand (manual
// button), bypassing the one-shot NameAssigned guard and clearing any prior
// manual UserSetName lock. Updates DisplayName only; the underlying session
// key is never changed. Returns the new name, or namer.ErrDisabled when AI
// naming is off.
func (m *Manager) RegenerateName(sessionName string) (string, error) {
	m.mu.RLock()
	n := m.namer
	meta := m.meta[sessionName]
	sess := m.sessions[sessionName]
	projectPath := meta.ProjectPath
	agentType := meta.AgentType
	if sess != nil {
		if sess.ProjectPath != "" {
			projectPath = sess.ProjectPath
		}
		if sess.AgentType != "" {
			agentType = sess.AgentType
		}
	}
	prompt := meta.LastUserPrompt
	if prompt == "" {
		prompt = meta.UserPrompt
	}
	nc := namer.Context{
		Workdir:    projectPath,
		Current:    meta.DisplayName,
		Agent:      agentType,
		UserPrompt: prompt,
		AgentMsg:   meta.LastAgentMessage,
		Taken:      m.otherDisplayNames(sessionName),
	}
	m.mu.RUnlock()

	if n == nil || !n.Enabled() {
		m.notice("warn", "ai-naming", sessionName, "AI naming is disabled. Enable it in Settings or set TERMYARD_NAMER_ENDPOINT.")
		return "", namer.ErrDisabled
	}

	if agentType != "" {
		nc.Kind = namer.KindAgent
	} else {
		nc.Kind = namer.KindShell
		nc.Commands = m.foregroundCommands(sessionName)
	}
	if projectPath != "" {
		if b, err := git.CurrentBranch(projectPath); err == nil {
			nc.Branch = b
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	name, err := n.Generate(ctx, nc)
	if err != nil {
		m.notice("warn", "ai-naming", sessionName, fmt.Sprintf("AI rename failed: %v", err))
		return "", err
	}

	return m.ApplyAIName(sessionName, name), nil
}

// GenerateName runs the AI namer over an arbitrary context and returns a
// sanitized name. Used by the hub to name remote-peer sessions locally (the
// peer process may not have a namer configured). Returns ErrDisabled when AI
// naming is off on this node.
func (m *Manager) GenerateName(ctx context.Context, nc namer.Context) (string, error) {
	m.mu.RLock()
	n := m.namer
	m.mu.RUnlock()
	if n == nil || !n.Enabled() {
		return "", namer.ErrDisabled
	}
	return n.Generate(ctx, nc)
}

// ApplyAIName stores an already-generated AI name for a session, bypassing the
// one-shot guard and clearing any prior manual lock so the name applies.
// Updates DisplayName only; the underlying session key is never changed.
// Returns the applied name.
func (m *Manager) ApplyAIName(sessionName, name string) string {
	if name == "" {
		return ""
	}
	m.mu.Lock()
	meta := m.meta[sessionName]
	agentType := meta.AgentType
	sess := m.sessions[sessionName]
	if sess != nil && sess.AgentType != "" {
		agentType = sess.AgentType
	}
	// Reset guards + manual lock so the forced name applies even when the
	// session was already named or user-set.
	meta.NameAssigned = false
	meta.Renamed = false
	meta.UserSetName = false
	m.meta[sessionName] = meta
	if sess != nil {
		sess.UserSetName = false
	}
	m.mu.Unlock()

	if agentType != "" {
		// applyGeneratedName re-checks the (now-cleared) guard and stores the name.
		m.applyGeneratedName(sessionName, name)
		return name
	}

	// Shell: store DisplayName only, never rename the session.
	m.mu.Lock()
	meta = m.meta[sessionName]
	meta.DisplayName = name
	meta.NameAssigned = true
	m.meta[sessionName] = meta
	if s := m.sessions[sessionName]; s != nil {
		s.DisplayName = name
	}
	m.mu.Unlock()
	m.saveNames()
	m.broadcast(StateEvent{Type: "sessions-changed"})
	return name
}

// foregroundCommands returns the active pane's foreground command for a
// session, used as shell-naming context for a manual name refresh.
func (m *Manager) foregroundCommands(session string) []string {
	// Daemon sessions don't have foreground command tracking.
	_ = session
	return nil
}

// otherDisplayNames returns the labels of every session except exclude, so the
// namer can pick something distinct. Prefers DisplayName, falls back to the
// session/meta key. Caller must hold m.mu (read or write).
func (m *Manager) otherDisplayNames(exclude string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(name string) {
		if name = strings.TrimSpace(name); name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for name, meta := range m.meta {
		if name == exclude {
			continue
		}
		if meta.DisplayName != "" {
			add(meta.DisplayName)
		} else {
			add(name)
		}
	}
	for name := range m.sessions {
		if name == exclude {
			continue
		}
		if _, ok := m.meta[name]; !ok {
			add(name)
		}
	}
	return out
}


