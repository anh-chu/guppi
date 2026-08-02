package state

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/git"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/namer"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

type SessionMetadata struct {
	ProjectPath      string
	AgentType        string
	PromptPreview    string
	AgentSessionID   string
	UserPrompt       string    // first user message; set once, for sidebar display
	LastUserPrompt   string    // latest user message; always updated, for AI naming
	LastAgentMessage string    // last agent response; always updated
	DisplayName      string    // AI-generated friendly label, refreshed as work evolves
	UserSetName      bool      // user manually set DisplayName; AI must not overwrite
	NameAssigned     bool      // AI naming has run at least once (informational/persisted)
	Renamed          bool      // underlying session was renamed; one-shot to avoid key churn
	LastNamedAt      time.Time // last AI naming attempt; debounces continuous refresh (not persisted)
}

// Manager holds the central state tree
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*model.Session
	meta     map[string]SessionMetadata
	namer    *namer.Namer

	// daemonReg provides metadata lookup for daemon sessions so
	// loadSessionDetails can populate CWD, PID, and synthetic panes.
	daemonReg DaemonRegistry

	// previewCache holds debounced prompt-preview snapshots so the 2-second
	// discovery refresh never blocks on expensive ring captures.
	previewMu  sync.Mutex
	previews   map[string]*previewCacheEntry
	previewGen uint64

	// onRename, when set, fires after a rename is applied (manual, AI naming, or
	// peer-driven) so external per-session stores keyed by session name can
	// migrate their entries.
	onRename func(oldName, newName string)

	// namesPath persists name metadata across restarts so AI/manual display
	// names survive a server reload (session names persist on their own,
	// but shell DisplayNames and non-renamed agent names live only in meta).
	namesPath string

	// Subscribers for state changes
	subMu       sync.RWMutex
	subscribers []chan StateEvent
}

// CrashedSessionInfo carries a crashed session record from the daemon
// lifecycle store for state-change broadcasts.
type CrashedSessionInfo struct {
	ID         string `json:"id"`
	Shell      string `json:"shell"`
	Cwd        string `json:"cwd"`
	Cols       uint16 `json:"cols"`
	Rows       uint16 `json:"rows"`
	CreatedAt  string `json:"created_at"`
	DaemonPID  int    `json:"daemon_pid"`
	CrashTime  string `json:"crash_time,omitempty"`
	Generation string `json:"generation"`
}

// DaemonRegistry is the subset of pty.Registry needed by the state manager.
type DaemonRegistry interface {
	List() []pty.SessionInfo
	Capture(name string) (string, error)
	CrashedSessions() []CrashedSessionInfo
	IsSessionDead(name string) bool
}

// TailCapturer is the optional subset of pty.Registry that supports a bounded
// tail capture. The state manager uses it when available to avoid the cost of
// replaying the entire ring buffer on every refresh.
type TailCapturer interface {
	CaptureTail(name string, maxBytes int) (string, error)
}

const (
	// promptPreviewInterval throttles ring captures so the 2-second discovery
	// refresh never pays for repeated expensive buffer replays.
	promptPreviewInterval = 30 * time.Second

	// promptPreviewTailBytes is the upper bound passed to CaptureTail. A tail
	// this size is enough to find the most recent prompt while keeping CPU low.
	promptPreviewTailBytes = 64 * 1024

	// promptPreviewEmptyLimit clears a stale preview after this many consecutive
	// successful captures that produce no extractable preview.
	promptPreviewEmptyLimit = 3
)



// SetDaemonRegistry wires the daemon registry into the state manager.
func (m *Manager) SetDaemonRegistry(reg DaemonRegistry) {
	m.daemonReg = reg
}

// StateEvent represents a change in the state tree
type StateEvent struct {
	Type     string      `json:"type"`
	Session  string      `json:"session,omitempty"`
	Host     string      `json:"host,omitempty"`
	HostName string      `json:"host_name,omitempty"`
	Data     interface{} `json:"data,omitempty"`
}

// NewManager creates a new state manager
func NewManager() *Manager {
	m := &Manager{
		sessions: make(map[string]*model.Session),
		meta:     make(map[string]SessionMetadata),
	}
	if home, err := os.UserHomeDir(); err == nil {
		m.namesPath = filepath.Join(home, ".config", "termyard", "session-names.json")
		m.loadNames()
	}
	return m
}

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

// Subscribe returns a channel that receives state events
func (m *Manager) Subscribe() chan StateEvent {
	ch := make(chan StateEvent, 64)
	m.subMu.Lock()
	m.subscribers = append(m.subscribers, ch)
	m.subMu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel
func (m *Manager) Unsubscribe(ch chan StateEvent) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for i, sub := range m.subscribers {
		if sub == ch {
			m.subscribers = append(m.subscribers[:i], m.subscribers[i+1:]...)
			close(ch)
			return
		}
	}
}

// broadcast sends an event to all subscribers
func (m *Manager) broadcast(evt StateEvent) {
	m.subMu.RLock()
	defer m.subMu.RUnlock()
	for _, ch := range m.subscribers {
		select {
		case ch <- evt:
		default:
			// subscriber too slow, drop event
		}
	}
}

// Notice carries a human-readable backend message to the frontend so silent
// background failures (AI naming, session rename, etc.) are visible in the UI
// instead of only in server logs.
type Notice struct {
	Severity string `json:"severity"` // "error", "warn", or "info"
	Source   string `json:"source"`   // short origin tag, e.g. "ai-naming"
	Message  string `json:"message"`  // human-readable detail
}

// notice broadcasts a Notice to the frontend and mirrors it to the server log.
func (m *Manager) notice(severity, source, session, message string) {
	switch severity {
	case "error":
		logrus.WithFields(logrus.Fields{"source": source, "session": session}).Error(message)
	case "warn":
		logrus.WithFields(logrus.Fields{"source": source, "session": session}).Warn(message)
	default:
		logrus.WithFields(logrus.Fields{"source": source, "session": session}).Info(message)
	}
	m.broadcast(StateEvent{
		Type:    "notice",
		Session: session,
		Data:    Notice{Severity: severity, Source: source, Message: message},
	})
}

// UpdateSessions takes a snapshot of sessions from discovery, diffs against
// previous state, and broadcasts changes
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

// loadSessionDetails fills in windows and panes for a session
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

// previewCacheEntry holds a debounced prompt-preview snapshot for a single
// session. It is guarded by Manager.previewMu.
type previewCacheEntry struct {
	preview     string
	lastAttempt time.Time
	refreshing  bool
	emptyStreak int
	generation  uint64
}

// lazyPreviews initializes the previews map for struct-literal Manager values.
func (m *Manager) lazyPreviews() {
	if m.previews == nil {
		m.previews = make(map[string]*previewCacheEntry)
	}
}

// preview returns the latest cached preview snapshot for a session. The
// returned string is a copy captured under previewMu, so callers read a stable
// value without racing async refresh goroutines.
func (m *Manager) preview(name string) string {
	m.previewMu.Lock()
	defer m.previewMu.Unlock()
	m.lazyPreviews()
	if entry := m.previews[name]; entry != nil {
		return entry.preview
	}
	return ""
}

// shouldRefreshPreview reports whether a new capture is due for the session.
func (m *Manager) shouldRefreshPreview(name string) bool {
	m.previewMu.Lock()
	defer m.previewMu.Unlock()
	m.lazyPreviews()
	entry := m.previews[name]
	if entry == nil {
		return true
	}
	if entry.refreshing {
		return false
	}
	return time.Since(entry.lastAttempt) >= promptPreviewInterval
}

// refreshPreview triggers an asynchronous refresh of the cached preview.
func (m *Manager) refreshPreview(name string) {
	m.refreshPreviewSync(name)
}

// refreshPreviewSync performs a synchronous refresh, respecting the throttle.
// Tests use this helper for deterministic assertions.
func (m *Manager) refreshPreviewSync(name string) {
	m.runPreviewRefresh(name)
}

// previewForceRefresh bypasses the throttle and refreshes synchronously.
// Intended for tests that need to check error behaviour deterministically.
func (m *Manager) previewForceRefresh(name string) {
	m.previewMu.Lock()
	m.lazyPreviews()
	entry := m.previews[name]
	if entry == nil {
		entry = &previewCacheEntry{}
		m.previews[name] = entry
	}
	entry.lastAttempt = time.Time{}
	m.previewMu.Unlock()
	m.refreshPreviewSync(name)
}

// startPreviewRefresh claims the refresh slot for a session. It returns false
// when another refresh is already running or the last attempt was recent.
func (m *Manager) startPreviewRefresh(name string) (uint64, bool) {
	m.previewMu.Lock()
	defer m.previewMu.Unlock()
	m.lazyPreviews()
	entry := m.previews[name]
	if entry == nil {
		entry = &previewCacheEntry{}
		m.previews[name] = entry
	}
	now := time.Now()
	if entry.refreshing || now.Sub(entry.lastAttempt) < promptPreviewInterval {
		return 0, false
	}
	entry.refreshing = true
	entry.lastAttempt = now
	m.previewGen++
	entry.generation = m.previewGen
	return entry.generation, true
}

// runPreviewRefresh captures prompt data and stores the extracted preview.
func (m *Manager) runPreviewRefresh(name string) {
	gen, ok := m.startPreviewRefresh(name)
	if !ok {
		return
	}
	text, usedTail, err := m.capturePreview(name)
	if err != nil {
		m.previewUpdateError(name, gen)
		return
	}
	if !usedTail && len(text) > promptPreviewTailBytes {
		text = truncateBytesTail(text, promptPreviewTailBytes)
	}
	m.previewUpdateResult(name, gen, model.ExtractPromptPreview(text))
}

// capturePreview prefers TailCapturer when available and falls back to the
// full Capture method implemented by every DaemonRegistry.
func (m *Manager) capturePreview(name string) (string, bool, error) {
	if m.daemonReg == nil {
		return "", false, nil
	}
	if tc, ok := m.daemonReg.(TailCapturer); ok && tc != nil {
		text, err := tc.CaptureTail(name, promptPreviewTailBytes)
		return text, true, err
	}
	text, err := m.daemonReg.Capture(name)
	return text, false, err
}

// previewUpdateResult stores a successful extraction, clearing the cache only
// after several consecutive empty extractions. If the session's cache entry
// was evicted or recreated while the refresh was in flight, the result is
// dropped so a removed session cannot resurrect stale preview state.
func (m *Manager) previewUpdateResult(name string, gen uint64, extracted string) {
	m.previewMu.Lock()
	defer m.previewMu.Unlock()
	entry := m.previews[name]
	if entry == nil || entry.generation != gen {
		return
	}
	entry.refreshing = false
	if extracted == "" {
		entry.emptyStreak++
		if entry.emptyStreak >= promptPreviewEmptyLimit {
			entry.preview = ""
			entry.emptyStreak = 0
		}
	} else {
		entry.preview = extracted
		entry.emptyStreak = 0
	}
}

// previewUpdateError keeps the last cached preview on capture failure so the
// UI still has a fallback, but records that an attempt occurred. If the
// session's cache entry was evicted or recreated while the refresh was in
// flight, it stays gone and no new stale state is introduced.
func (m *Manager) previewUpdateError(name string, gen uint64) {
	m.previewMu.Lock()
	defer m.previewMu.Unlock()
	entry := m.previews[name]
	if entry == nil || entry.generation != gen {
		return
	}
	entry.refreshing = false
}

// evictPreview removes a session's cached preview.
func (m *Manager) evictPreview(name string) {
	m.previewMu.Lock()
	defer m.previewMu.Unlock()
	delete(m.previews, name)
}

// truncateBytesTail returns the last n bytes of s, trimming to a valid UTF-8
// boundary so ExtractPromptPreview never sees a broken rune.
func truncateBytesTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	b := []byte(s)
	b = b[len(b)-n:]
	for len(b) > 0 && b[0]&0xC0 == 0x80 {
		b = b[1:]
	}
	return string(b)
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

// nameRefreshInterval debounces continuous AI re-naming of agent sessions so a
// burst of completed turns does not hammer the namer endpoint.
const nameRefreshInterval = 45 * time.Second

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

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	name, err := n.Generate(ctx, nc)
	if err != nil {
		m.notice("warn", "ai-naming", sessionName, fmt.Sprintf("agent session naming failed: %v", err))
		return
	}
	logrus.WithFields(logrus.Fields{"session": sessionName, "name": name}).Info("agent session named")

	m.applyGeneratedName(sessionName, name)
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

// GenerateGroupName synthesizes a single label for a layout group from its
// member session labels. Groups are a frontend-only concept, so this is a
// stateless helper: it does not persist anything. Returns ErrDisabled when the
// namer is off and an error on any network/parse failure; callers keep the
// existing name on error.
func (m *Manager) GenerateGroupName(members []namer.GroupMember, current string) (string, error) {
	m.mu.RLock()
	n := m.namer
	m.mu.RUnlock()
	if n == nil || !n.Enabled() {
		return "", namer.ErrDisabled
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	return n.Generate(ctx, namer.Context{
		Kind:    namer.KindGroup,
		Members: members,
		Current: current,
	})
}

// SetRenameHook installs an optional callback fired after a session rename is
// applied. Used to migrate external per-session stores (e.g. shared session
// attributes) keyed by session name.
func (m *Manager) SetRenameHook(fn func(oldName, newName string)) {
	m.mu.Lock()
	m.onRename = fn
	m.mu.Unlock()
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
	meta.LastNamedAt = time.Now()
	nameChanged := meta.DisplayName != displayName
	meta.DisplayName = displayName
	meta.NameAssigned = true
	m.meta[sessionName] = meta
	if sess := m.sessions[sessionName]; sess != nil {
		sess.DisplayName = displayName
	}
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

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	name, err := n.Generate(ctx, nc)
	if err != nil {
		m.notice("warn", "ai-naming", sessionName, fmt.Sprintf("shell session naming failed: %v", err))
		return
	}
	logrus.WithFields(logrus.Fields{"session": sessionName, "name": name}).Info("shell session named")

	m.mu.Lock()
	meta = m.meta[sessionName]
	if meta.UserSetName {
		m.mu.Unlock()
		return
	}
	if meta.DisplayName == name {
		m.mu.Unlock()
		return
	}
	meta.DisplayName = name
	m.meta[sessionName] = meta
	if s := m.sessions[sessionName]; s != nil {
		s.DisplayName = name
	}
	m.mu.Unlock()
	m.saveNames()
	m.broadcast(StateEvent{Type: "sessions-changed"})
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
