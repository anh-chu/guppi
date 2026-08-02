package state

import (
	"fmt"
	"os"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/git"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

// UpdateSessions takes a snapshot of sessions from discovery, diffs against
// previous state, and broadcasts changes.
func (m *Manager) UpdateSessions(sessions []*model.Session) {
	// Load full details for each session
	for _, session := range sessions {
		if err := m.loadSessionDetails(session); err != nil {
			logrus.WithError(err).WithField("session", session.Name).Warn("failed to load session details")
		}
	}

	m.mu.Lock()
	// Build new map
	newMap := make(map[string]*model.Session, len(sessions))
	for _, s := range sessions {
		newMap[s.Name] = s
	}

	// --- Mass-removal safety guards ---
	// These protect against transient discovery failures (e.g. socket directory
	// temporarily unreadable) that would otherwise wipe every tracked session
	// from state, violating the "sessions must not disappear without explicit
	// user action" guarantee.

	// Guard 1: if ALL sessions vanished from discovery, this is almost always a
	// transient discovery failure, so skip the cycle — UNLESS every vanished
	// session is individually confirmed dead (clean exit, intentional kill, or
	// dismiss). That exception covers the "killed the last session" case: the
	// daemon removes its socket on exit, discovery returns empty, and without
	// this the dead session would linger in the sidebar forever as
	// "disconnected — reconnecting".
	allDead := false
	if len(m.sessions) > 0 && len(newMap) == 0 {
		allDead = m.daemonReg != nil
		if allDead {
			for name := range m.sessions {
				if !m.daemonReg.IsSessionDead(name) {
					allDead = false
					break
				}
			}
		}
		if !allDead {
			logrus.Warn("state: all sessions disappeared from discovery — skipping removal (likely transient)")
			m.mu.Unlock()
			return
		}
		logrus.Info("state: all sessions disappeared from discovery and are confirmed dead — removing")
	}

	// Compute which sessions would be removed (deferred action so we can guard).
	removed := make([]string, 0)
	for name := range m.sessions {
		if _, ok := newMap[name]; !ok {
			removed = append(removed, name)
		}
	}

	// Guard 2: don't remove more than 50% of sessions in one cycle (unless we
	// only had 2 or fewer — a single intentional kill would look like 50%).
	// Removing 1-2 sessions is fine; removing MOST sessions is almost certainly
	// a discovery bug, not real session death. Skip when Guard 1 already
	// confirmed every vanished session is genuinely dead.
	if !allDead && len(removed) > len(m.sessions)/2 && len(m.sessions) > 2 {
		logrus.WithFields(logrus.Fields{
			"current":      len(m.sessions),
			"would_remove": len(removed),
		}).Warn("state: would remove majority of sessions — skipping removal (likely transient)")
		m.mu.Unlock()
		return
	}

	// Now perform the actual removals. A session that vanishes from discovery
	// (e.g. killed outside termyard's UI) must also have its metadata dropped,
	// otherwise a later session reusing the same name inherits stale state.
	for _, name := range removed {
		delete(m.meta, name)
		m.evictPreview(name)
		m.mu.Unlock()
		m.broadcast(StateEvent{Type: "session-removed", Session: name})
		m.mu.Lock()
	}

	// Detect added sessions
	for name := range newMap {
		if _, ok := m.sessions[name]; !ok {
			m.mu.Unlock()
			m.broadcast(StateEvent{Type: "session-added", Session: name})
			m.mu.Lock()
		}
	}

	m.sessions = newMap
	m.mu.Unlock()

	// Persist name changes when sessions were removed so the on-disk names file
	// does not resurrect stale labels for a future same-name session after restart.
	if len(removed) > 0 {
		m.saveNames()
	}

	// Broadcast a general refresh event
	m.broadcast(StateEvent{Type: "sessions-changed"})
}

// loadSessionDetails fills in windows and panes for a session.
func (m *Manager) loadSessionDetails(session *model.Session) error {
	// All sessions are daemon-backed now.
	m.loadDaemonSessionDetails(session)

	// Detect linked git worktrees so the UI can offer cleanup on kill.
	if session.ProjectPath != "" {
		if ok, err := git.IsWorktree(session.ProjectPath); err == nil {
			session.IsWorktree = ok
			if ok {
				if root, err := git.FindMainWorktreeRoot(session.ProjectPath); err == nil {
					session.WorktreeParent = root
				}
			}
		} else {
			logrus.WithError(err).WithField("path", session.ProjectPath).Debug("git worktree check failed")
		}
	}

	return nil
}

// loadDaemonSessionDetails populates a daemon session with a synthetic
// single-window, single-pane structure using daemon registry metadata.
func (m *Manager) loadDaemonSessionDetails(session *model.Session) {
	if m.daemonReg == nil {
		m.applyMetadata(session)
		return
	}

	// Find this session's daemon metadata.
	var info *pty.SessionInfo
	for _, di := range m.daemonReg.List() {
		if di.ID == session.Name {
			info = &di
			break
		}
	}

	cwd := ""
	pid := 0
	if info != nil {
		cwd = info.Cwd
		pid = info.ShellPid
		if pid == 0 {
			pid = info.Pid
		}
		// Try to read live CWD from the shell process.
		if pid > 0 {
			if liveCwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid)); err == nil && liveCwd != "" {
				cwd = liveCwd
			}
		}
	}

	// Build synthetic pane.
	pane := &model.Pane{
		ID:          session.Name + ":0.0",
		Active:      true,
		CurrentPath: cwd,
		PID:         pid,
	}
	win := &model.Window{
		ID:     session.Name + ":0",
		Name:   session.Name,
		Index:  0,
		Active: true,
		Panes:  []*model.Pane{pane},
	}
	session.Windows = []*model.Window{win}

	if cwd != "" {
		session.ProjectPath = cwd
	}

	// Use cached prompt preview; refresh asynchronously so discovery never
	// waits on an expensive full ring capture.
	if preview := m.preview(session.Name); preview != "" {
		session.PromptPreview = preview
	}
	if m.shouldRefreshPreview(session.Name) {
		go m.refreshPreview(session.Name)
	}

	m.applyMetadata(session)
}

// sessionHasLiveAgent reports whether any pane in the session currently has a
// recognized coding agent process running in its tree. Used to distinguish a
// live agent (keep its identity) from a session that used to run one but has
// reverted to a shell or other process (drop the stale identity).
func sessionHasLiveAgent(windows []*model.Window) bool {
	for _, win := range windows {
		for _, pane := range win.Panes {
			if pane.PID <= 0 {
				continue
			}
			if _, ok := toolevents.DetectAgentInProcessTree(pane.PID); ok {
				return true
			}
		}
	}
	return false
}

func (m *Manager) applyMetadata(session *model.Session) {
	m.mu.RLock()
	meta := m.meta[session.Name]
	m.mu.RUnlock()

	if meta.ProjectPath != "" && session.ProjectPath == "" {
		session.ProjectPath = meta.ProjectPath
	}
	// Agent-derived metadata (identity, prompt, last message, AI-generated name)
	// is only valid while the agent still runs in the session. Once it exits and
	// the pane reverts to a shell or another command (e.g. a `pnpm dev` server
	// that fronts as `node`), those values are stale and must not render. The
	// process-tree check is done lazily and cached so sessions without agent
	// metadata never pay for it.
	agentChecked := false
	agentPresent := false
	agentAlive := func() bool {
		if !agentChecked {
			agentPresent = sessionHasLiveAgent(session.Windows)
			agentChecked = true
		}
		return agentPresent
	}

	if session.AgentType == "" && meta.AgentType != "" {
		if agentAlive() {
			session.AgentType = model.NormalizeAgentType(meta.AgentType)
		} else {
			m.mu.Lock()
			stored := m.meta[session.Name]
			stored.AgentType = ""
			m.meta[session.Name] = stored
			m.mu.Unlock()
		}
	}
	if meta.PromptPreview != "" && session.PromptPreview == "" {
		session.PromptPreview = meta.PromptPreview
	}
	if meta.AgentSessionID != "" {
		session.AgentSessionID = meta.AgentSessionID
	}
	if meta.UserPrompt != "" && session.UserPrompt == "" && agentAlive() {
		session.UserPrompt = meta.UserPrompt
	}
	if meta.LastAgentMessage != "" && agentAlive() {
		session.LastAgentMessage = meta.LastAgentMessage
	}
	// A user-set name is kept always; an AI-generated name is suppressed once
	// the agent that produced it is gone, so the session reverts to its raw name.
	if meta.DisplayName != "" && (meta.UserSetName || agentAlive()) {
		session.DisplayName = meta.DisplayName
	}
	session.UserSetName = meta.UserSetName
}

// UpdateSessionMetadataFromEvent stores stable metadata derived from agent
// hooks so it remains available after transient status events are cleared.
func (m *Manager) UpdateSessionMetadataFromEvent(evt *toolevents.Event) {
	if evt == nil || evt.Session == "" {
		return
	}

	m.mu.Lock()
	meta := m.meta[evt.Session]
	changed := false
	if evt.CWD != "" {
		if meta.ProjectPath != evt.CWD {
			changed = true
		}
		meta.ProjectPath = evt.CWD
	}
	if evt.Tool != "" {
		tool := string(evt.Tool)
		if meta.AgentType != tool {
			changed = true
		}
		meta.AgentType = tool
	}
	// Only update PromptPreview from meaningful (non-transient) messages.
	// Transient active-phase labels like "Working" / "Using tool" must not
	// clobber the last meaningful agent message shown in the sidebar.
	if evt.Message != "" && (meta.PromptPreview == "" || evt.Status != toolevents.StatusActive) {
		if meta.PromptPreview != evt.Message {
			changed = true
		}
		meta.PromptPreview = evt.Message
	}
	if evt.AgentSessionID != "" {
		if meta.AgentSessionID != evt.AgentSessionID {
			changed = true
		}
		meta.AgentSessionID = evt.AgentSessionID
	}
	firstPrompt := false
	nameRefresh := false
	if evt.UserPrompt != "" {
		if meta.UserPrompt == "" {
			meta.UserPrompt = evt.UserPrompt // first message; sidebar display, set once
			if !meta.NameAssigned && !meta.UserSetName {
				firstPrompt = true
			}
		}
		if meta.LastUserPrompt != evt.UserPrompt {
			meta.LastUserPrompt = evt.UserPrompt // always track latest for AI naming
			changed = true
			// A new user prompt steers the work; re-name (debounced) unless the
			// first-prompt pass below already handles it.
			if !firstPrompt && !meta.UserSetName &&
				time.Since(meta.LastNamedAt) > nameRefreshInterval {
				nameRefresh = true
				meta.LastNamedAt = time.Now()
			}
		}
	}
	if evt.AgentMessage != "" && meta.LastAgentMessage != evt.AgentMessage {
		meta.LastAgentMessage = evt.AgentMessage
		changed = true
		// Re-name on completed turns as the work evolves, debounced per session.
		// firstPrompt / a fresh user prompt already cover the other naming passes.
		if !firstPrompt && !nameRefresh && !meta.UserSetName && evt.Status == toolevents.StatusCompleted &&
			time.Since(meta.LastNamedAt) > nameRefreshInterval {
			nameRefresh = true
			meta.LastNamedAt = time.Now()
		}
	}

	if !changed {
		m.mu.Unlock()
		return
	}

	m.meta[evt.Session] = meta
	if session := m.sessions[evt.Session]; session != nil {
		if session.ProjectPath == "" && meta.ProjectPath != "" {
			session.ProjectPath = meta.ProjectPath
		}
		if session.AgentType == "" && meta.AgentType != "" {
			session.AgentType = model.NormalizeAgentType(meta.AgentType)
		}
		if session.PromptPreview == "" && meta.PromptPreview != "" {
			session.PromptPreview = meta.PromptPreview
		}
		if meta.AgentSessionID != "" {
			session.AgentSessionID = meta.AgentSessionID
		}
		if session.UserPrompt == "" && meta.UserPrompt != "" {
			session.UserPrompt = meta.UserPrompt
		}
		if meta.LastAgentMessage != "" {
			session.LastAgentMessage = meta.LastAgentMessage
		}
	}
	m.mu.Unlock()

	m.broadcast(StateEvent{Type: "sessions-changed"})

	if firstPrompt || nameRefresh {
		go m.triggerAgentNaming(evt.Session)
	} else if meta.DisplayName != "" || meta.UserSetName {
		// Named session whose prompt/message changed without a re-name: persist so
		// the namer has fresh context after a restart instead of a stale prompt.
		go m.saveNames()
	}
}

// SetSessionAgentType explicitly stores an agent type for a session,
// overriding inference. Used when a session is created with a known preset.
func (m *Manager) SetSessionAgentType(sessionName, agentType string) {
	m.mu.Lock()
	meta := m.meta[sessionName]
	meta.AgentType = agentType
	m.meta[sessionName] = meta
	if session := m.sessions[sessionName]; session != nil && session.AgentType == "" {
		session.AgentType = agentType
	}
	m.mu.Unlock()
}

// GetSessionProjectPath returns the ProjectPath for a session, or empty string if unknown.
func (m *Manager) GetSessionProjectPath(name string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.sessions[name]; ok {
		return s.ProjectPath
	}
	if meta, ok := m.meta[name]; ok {
		return meta.ProjectPath
	}
	return ""
}

// GetSessions returns all tracked sessions with full details.
func (m *Manager) GetSessions() []*model.Session {
	m.mu.RLock()
	result := make([]*model.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		result = append(result, s)
	}
	m.mu.RUnlock()
	return result
}

// SnapshotForManifest returns deep copies of current tracked sessions.
func (m *Manager) SnapshotForManifest() []*model.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*model.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s == nil {
			continue
		}
		out = append(out, deepCopySession(s))
	}
	return out
}

func deepCopySession(s *model.Session) *model.Session {
	if s == nil {
		return nil
	}
	copySession := *s
	if len(s.Windows) > 0 {
		copySession.Windows = make([]*model.Window, 0, len(s.Windows))
		for _, win := range s.Windows {
			if win == nil {
				continue
			}
			copyWin := *win
			if len(win.Panes) > 0 {
				copyWin.Panes = make([]*model.Pane, 0, len(win.Panes))
				for _, pane := range win.Panes {
					if pane == nil {
						continue
					}
					copyPane := *pane
					copyWin.Panes = append(copyWin.Panes, &copyPane)
				}
			}
			copySession.Windows = append(copySession.Windows, &copyWin)
		}
	}
	return &copySession
}

// RemoveSession removes a session from the in-memory state, broadcasting
// removal events. Use this when a session no longer exists but the
// state manager still holds a reference to it.
func (m *Manager) RemoveSession(name string) {
	m.mu.Lock()
	delete(m.sessions, name)
	delete(m.meta, name)
	m.evictPreview(name)
	m.mu.Unlock()
	m.saveNames()
	m.broadcast(StateEvent{Type: "session-removed", Session: name})
	m.broadcast(StateEvent{Type: "sessions-changed"})
}
