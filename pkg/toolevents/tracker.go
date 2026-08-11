package toolevents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anh-chu/termyard/pkg/config"
	"github.com/sirupsen/logrus"
)

// Tool identifies which AI coding tool sent the event
type Tool string

const (
	ToolClaude   Tool = "claude"
	ToolCodex    Tool = "codex"
	ToolCopilot  Tool = "copilot"
	ToolGemini   Tool = "gemini"
	ToolOpenCode Tool = "opencode"
	ToolPi       Tool = "pi"
)

const maxArtifactsPerSession = 100

// Status represents the current state of an agent
type Status string

const (
	StatusActive    Status = "active"    // agent is running, doing work
	StatusWaiting   Status = "waiting"   // agent needs user attention/approval
	StatusCompleted Status = "completed" // agent finished its task
	StatusError     Status = "error"     // agent encountered an error
	StatusStuck     Status = "stuck"     // agent claims active but made no observable progress
)

// writeToolAllowlist defines which tool types produce recordable artifacts.
// Only write-ish tools are included; read, grep, glob produce no artifacts.
var writeToolAllowlist = map[string]bool{
	"write":              true,
	"edit":               true,
	"multiedit":          true,
	"str_replace":        true,
	"str_replace_editor": true,
	"apply_patch":        true,
	"notebook_edit":      true,
}

// IsWriteTool returns true if the tool name is in the allowlist (case-insensitive).
func IsWriteTool(toolName string) bool {
	return writeToolAllowlist[strings.ToLower(toolName)]
}

// FileArtifact is a file an agent produced, with server-local stat metadata.
type FileArtifact struct {
	Path        string    `json:"path"`
	DisplayPath string    `json:"display_path,omitempty"`
	Name        string    `json:"name"`
	Size        int64     `json:"size,omitempty"`
	ModTime     time.Time `json:"mod_time,omitempty"`
	Tool        Tool      `json:"tool,omitempty"`
	Source      string    `json:"source"`
	FirstSeen   time.Time `json:"first_seen"`
}

// Event is a single notification from an agent hook
type Event struct {
	Tool           Tool            `json:"tool"`
	ToolName       string          `json:"tool_name,omitempty"`        // actual tool name (e.g., "write", "edit") for write-tool filtering
	Status         Status          `json:"status"`
	Kind           string          `json:"kind,omitempty"`
	Host           string          `json:"host,omitempty"`             // peer fingerprint (empty = local)
	HostName       string          `json:"host_name,omitempty"`        // peer display name
	Session        string          `json:"session"`                    // session name
	Window         int             `json:"window"`                     // window index
	Pane           string          `json:"pane,omitempty"`             // pane ID (optional)
	Message        string          `json:"message,omitempty"`          // human-readable detail
	CWD            string          `json:"cwd,omitempty"`              // current working directory when provided by the agent hook
	AgentSessionID string          `json:"agent_session_id,omitempty"` // upstream agent session/thread id when available
	Timestamp      time.Time       `json:"timestamp"`
	AutoDetected   bool            `json:"auto_detected,omitempty"` // true if detected via process tree (not hooks)
	UserPrompt     string          `json:"user_prompt,omitempty"`   // first user message for this session (set once)
	AgentMessage   string          `json:"agent_message,omitempty"` // last agent response message (updates each turn)
	Files          []string        `json:"files,omitempty"`
	Artifacts      []*FileArtifact `json:"artifacts,omitempty"`
}

// PaneKey uniquely identifies a pane
type PaneKey struct {
	Host    string
	Session string
	Window  int
	Pane    string
}

type SessionMeta struct {
	Tool             Tool
	CWD              string
	AgentSessionID   string
	Message          string
	UserPrompt       string // first user message; set once, never overwritten
	LastAgentMessage string // last agent response; updated each turn
}

// nativeWaitingTools is the set of tools that send explicit "waiting" events
// via hooks. Tools not in this set will have inactivity-based and
// silence-based waiting detection as fallbacks.
var nativeWaitingTools = map[Tool]bool{
	ToolClaude:   true,
	ToolOpenCode: true,
	ToolPi:       true,
}

// Tracker tracks the latest status of AI tools per pane
type Tracker struct {
	mu        sync.RWMutex
	events    map[PaneKey]*Event
	artifacts map[string][]*FileArtifact

	// lastActive tracks the most recent "active" event per pane for tools
	// that lack native waiting detection. Used by the inactivity promoter.
	lastActive map[PaneKey]*Event

	// activePanes tracks local panes whose latest hook status is "active",
	// including native-waiting tools (Claude/Pi/OpenCode). Used by the stuck
	// monitor to detect agents that claim to be working but make no
	// observable progress (no tool events and no pane output). Keyed by
	// pane ID so the %output handler can refresh progress cheaply.
	activePanes map[string]*activeState

	// activeTurns tracks panes with an in-progress hook-based agent turn,
	// keyed by pane. Set on a hook "active" event, cleared on any terminal
	// status (completed/waiting/error). Unlike t.events it survives the
	// active-clears-tracking rule, giving the frontend an authoritative source
	// to reconcile its "working" badge against. notify -> server is HTTP (not
	// the lossy WS broadcast), so this map is reliable even when a completed
	// WS frame is dropped.
	activeTurns map[PaneKey]*Event

	// Subscribers
	subMu       sync.RWMutex
	subscribers []chan *Event

	sessionMeta map[string]SessionMeta

	// path is the on-disk file for retained state (waiting/error events +
	// session metadata) so it survives a server restart. Empty = persistence
	// disabled (the default; tests stay in-memory). Set via EnablePersistence.
	path string
}

// NewTracker creates a new tool event tracker
// activeState tracks an actively-working local pane for stuck detection.
type activeState struct {
	evt          *Event    // the originating active event (session/window/pane context)
	lastProgress time.Time // last tool event or pane output for this pane
	flagged      bool      // true once we've emitted a stuck event (avoid repeats)
}

func NewTracker() *Tracker {
	return &Tracker{
		events:      make(map[PaneKey]*Event),
		artifacts:   make(map[string][]*FileArtifact),
		lastActive:  make(map[PaneKey]*Event),
		activePanes: make(map[string]*activeState),
		activeTurns: make(map[PaneKey]*Event),
		sessionMeta: make(map[string]SessionMeta),
	}
}

// persistedState is the on-disk snapshot of retained tracker state. Events
// are stored as a slice (PaneKey is a struct and can't be a JSON map key);
// the key is reconstructed from each event's host/session/window/pane on load.
type persistedState struct {
	Events      []*Event                   `json:"events"`
	Artifacts   map[string][]*FileArtifact `json:"artifacts"`
	SessionMeta map[string]SessionMeta     `json:"session_meta"`
}

// EnablePersistence loads any previously persisted retained state (waiting/
// error events + session metadata) and, from now on, writes it back to disk
// on every Record so it survives a server restart. Best-effort: a missing or
// corrupt file is ignored, and persistence stays off if the dir can't be made.
func (t *Tracker) EnablePersistence() {
	dir, err := config.Dir()
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	t.mu.Lock()
	t.path = filepath.Join(dir, "toolevents.json")
	t.load()
	t.mu.Unlock()
}

// load reads persisted state from disk. Caller must hold t.mu.
func (t *Tracker) load() {
	data, err := os.ReadFile(t.path)
	if err != nil {
		return
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return
	}
	for _, evt := range st.Events {
		if evt == nil {
			continue
		}
		t.events[PaneKey{Host: evt.Host, Session: evt.Session, Window: evt.Window, Pane: evt.Pane}] = evt
	}
	now := time.Now()
	sevenDaysAgo := now.AddDate(0, 0, -7)
	for k, arts := range st.Artifacts {
		if len(arts) == 0 {
			continue
		}
		// Skip legacy artifact keys without the "host\x00session" separator.
		// They represent old state from before cross-host scoping was fixed.
		if !strings.Contains(k, "\x00") {
			continue
		}
		dup := make([]*FileArtifact, 0, len(arts))
		for _, art := range arts {
			if art == nil {
				continue
			}
			// Drop artifacts older than 7 days
			if art.FirstSeen.Before(sevenDaysAgo) {
				continue
			}
			dup = append(dup, art)
		}
		if len(dup) > 0 {
			t.artifacts[k] = dup
		}
	}
	for k, v := range st.SessionMeta {
		t.sessionMeta[k] = v
	}
}

// persist snapshots retained state under the read lock and writes it to disk.
// No-op when persistence is disabled. ponytail: writes the whole snapshot on
// every Record (mirrors auth's per-call save); add a dirty-flag if event rate
// ever makes this I/O-bound.
func (t *Tracker) persist() {
	t.mu.RLock()
	if t.path == "" {
		t.mu.RUnlock()
		return
	}
	st := persistedState{
		Events:      make([]*Event, 0, len(t.events)),
		Artifacts:   make(map[string][]*FileArtifact, len(t.artifacts)),
		SessionMeta: make(map[string]SessionMeta, len(t.sessionMeta)),
	}
	for _, evt := range t.events {
		st.Events = append(st.Events, evt)
	}
	for k, arts := range t.artifacts {
		if len(arts) == 0 {
			continue
		}
		dup := make([]*FileArtifact, 0, len(arts))
		for _, art := range arts {
			if art == nil {
				continue
			}
			dupArt := *art
			dup = append(dup, &dupArt)
		}
		if len(dup) > 0 {
			st.Artifacts[k] = dup
		}
	}
	for k, v := range t.sessionMeta {
		st.SessionMeta[k] = v
	}
	path := t.path
	t.mu.RUnlock()

	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

// Record stores a new event and broadcasts it to subscribers
func (t *Tracker) Record(evt *Event) {
	evt.Timestamp = time.Now()

	log := logrus.WithFields(logrus.Fields{
		"tool":          evt.Tool,
		"status":        evt.Status,
		"session":       evt.Session,
		"window":        evt.Window,
		"pane":          evt.Pane,
		"host":          evt.Host,
		"message":       evt.Message,
		"auto_detected": evt.AutoDetected,
	})
	log.Debug("recording tool event")

	key := PaneKey{
		Host:    evt.Host,
		Session: evt.Session,
		Window:  evt.Window,
		Pane:    evt.Pane,
	}
	sessionKey := evt.Host + "\x00" + evt.Session

	t.mu.Lock()
	if evt.Status == StatusCompleted || evt.Status == StatusActive {
		// Completed and active events clear the tracking — active is transient
		// and only serves to signal that waiting/error state should be dismissed
		_, existed := t.events[key]
		delete(t.events, key)
		log.WithFields(logrus.Fields{
			"action": "clear", "had_existing": existed, "tracked_count": len(t.events),
		}).Trace("tracker: cleared event (active/completed)")
	} else {
		t.events[key] = evt
		log.WithField("tracked_count", len(t.events)).Trace("tracker: stored event (waiting/error)")
	}

	// Track last activity for tools without native waiting detection.
	// The inactivity promoter uses this to generate synthetic "waiting"
	// events when a tool goes quiet. Auto-detected events are excluded —
	// only hook-based activity should trigger the inactivity timer,
	// since we can't tell if an auto-detected agent is working or idle.
	if !nativeWaitingTools[evt.Tool] && !evt.AutoDetected {
		switch evt.Status {
		case StatusActive:
			t.lastActive[key] = evt
			log.Trace("tracker: added to lastActive for inactivity promotion")
		default:
			// Any explicit waiting/error/completed clears the inactivity tracker
			delete(t.lastActive, key)
			log.Trace("tracker: cleared from lastActive")
		}
	} else if evt.AutoDetected {
		log.Trace("tracker: skipping lastActive (auto-detected)")
	}

	// Maintain activePanes for stuck detection. Only local panes (Host=="")
	// with a pane ID, driven by hook events (not auto-detected). Unlike
	// lastActive, this includes native-waiting tools (Claude/Pi/OpenCode):
	// those tools clear their tracked event on "active", so a hung agent
	// mid-tool would otherwise be invisible. A "stuck" event itself does not
	// clear activePanes (the agent still claims to be working); any output
	// or new tool event refreshes lastProgress and unflags it.
	if evt.Host == "" && evt.Pane != "" && !evt.AutoDetected {
		switch evt.Status {
		case StatusActive:
			t.activePanes[evt.Pane] = &activeState{evt: evt, lastProgress: evt.Timestamp}
			log.Trace("tracker: tracking active pane for stuck detection")
		case StatusStuck:
			// keep the entry; the monitor sets flagged itself
		default:
			// waiting/error/completed mean the agent no longer claims to be working
			delete(t.activePanes, evt.Pane)
			log.Trace("tracker: cleared active pane (non-active status)")
		}
	}

	// Authoritative active-turn tracking for the frontend "working" badge.
	// Hook "active" opens a turn; any terminal status closes it. Stuck keeps
	// the turn open (the agent still claims to be working).
	if !evt.AutoDetected {
		switch evt.Status {
		case StatusActive:
			t.activeTurns[key] = evt
		case StatusStuck:
			// keep the turn open
		default:
			delete(t.activeTurns, key)
		}
	}

	meta := t.sessionMeta[sessionKey]
	if evt.Tool != "" {
		meta.Tool = evt.Tool
	}
	if evt.CWD != "" {
		meta.CWD = evt.CWD
	}
	if evt.AgentSessionID != "" {
		meta.AgentSessionID = evt.AgentSessionID
	}
	if evt.Message != "" && !evt.AutoDetected {
		meta.Message = evt.Message
	}
	if evt.UserPrompt != "" && meta.UserPrompt == "" {
		meta.UserPrompt = evt.UserPrompt // set once
	}
	if evt.AgentMessage != "" {
		meta.LastAgentMessage = evt.AgentMessage
	}
	if meta != (SessionMeta{}) {
		t.sessionMeta[sessionKey] = meta
	}
	t.mu.Unlock()
	if len(evt.Artifacts) > 0 {
		t.storeArtifacts(evt.Host, evt.Session, evt.Artifacts)
	}

	// Broadcast to subscribers
	t.subMu.RLock()
	defer t.subMu.RUnlock()
	sent := 0
	for _, ch := range t.subscribers {
		select {
		case ch <- evt:
			sent++
		default:
			log.Debug("tool event subscriber channel full, dropping")
		}
	}
	log.WithField("subscribers", sent).Trace("tool event broadcast complete")

	t.persist()
}

// RecordArtifacts stores server-detected file artifacts for a session (the
// tier-2 regex-scanner path, as opposed to Record's tier-1 hook path) and
// broadcasts them to subscribers as a Kind:"artifact" event. Unlike Record,
// this never touches t.events/t.lastActive/t.activePanes — there is no real
// tool-call lifecycle here, just "some files were seen and verified to exist".
func (t *Tracker) RecordArtifacts(session string, arts []*FileArtifact) {
	if len(arts) == 0 {
		return
	}
	t.storeArtifacts("", session, arts)

	evt := &Event{Kind: "artifact", Session: session, Artifacts: arts, Timestamp: time.Now()}
	t.subMu.RLock()
	sent := 0
	for _, ch := range t.subscribers {
		select {
		case ch <- evt:
			sent++
		default:
			logrus.Debug("tool event subscriber channel full, dropping")
		}
	}
	t.subMu.RUnlock()
	logrus.WithField("subscribers", sent).Trace("artifact broadcast complete")
	t.persist()
}

// StaleTimeout is how long an event can sit without an update before being
// considered stale and automatically cleared. This is intentionally long
// because agents like Claude can wait for user input indefinitely.
const StaleTimeout = 24 * time.Hour

// GetActivePaneEvents returns events for locally-tracked hook-based active panes.
// Active hook events clear t.events, so they are invisible to GetAll. This method
// exposes them so the reconciler can detect process exits for panes that sent
// an active hook but never sent a subsequent waiting/completed hook.
// RetainedWaitingForPane returns the retained waiting event for a local pane,
// or nil if the pane has no outstanding waiting event. Used by the silence
// monitor's reaper to clear stale native-agent waiting (e.g. Claude) once the
// input dialog is dismissed, since some agents emit no hook on cancel.
func (t *Tracker) RetainedWaitingForPane(paneID string) *Event {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, evt := range t.events {
		if evt.Pane == paneID && evt.Status == StatusWaiting {
			return evt
		}
	}
	return nil
}

func (t *Tracker) GetActivePaneEvents() []*Event {
	t.mu.RLock()
	defer t.mu.RUnlock()
	events := make([]*Event, 0, len(t.activePanes))
	for _, as := range t.activePanes {
		events = append(events, as.evt)
	}
	return events
}

// ActiveTurns returns the events for panes with an in-progress hook-based
// agent turn, pruning stale ones. The frontend rebuilds its "working" badge
// state from this so a dropped "completed" WS frame self-heals on the next
// periodic refresh.
func (t *Tracker) ActiveTurns() []*Event {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	turns := make([]*Event, 0, len(t.activeTurns))
	for key, evt := range t.activeTurns {
		if now.Sub(evt.Timestamp) > StaleTimeout {
			delete(t.activeTurns, key)
			continue
		}
		turns = append(turns, evt)
	}
	return turns
}

// GetAll returns all currently tracked (non-completed) events, pruning stale ones.
func (t *Tracker) GetAll() []*Event {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	events := make([]*Event, 0, len(t.events))
	for key, evt := range t.events {
		if now.Sub(evt.Timestamp) > StaleTimeout {
			delete(t.events, key)
			continue
		}
		events = append(events, evt)
	}
	return events
}

// Clear removes a specific event by session/window/pane
// Clear removes a specific event by host/session/window/pane
func (t *Tracker) Clear(host, session string, window int, pane string) {
	t.mu.Lock()
	key := PaneKey{Host: host, Session: session, Window: window, Pane: pane}
	delete(t.events, key)
	t.mu.Unlock()
}

// ClearAll removes all tracked events
func (t *Tracker) ClearAll() {
	t.mu.Lock()
	t.events = make(map[PaneKey]*Event)
	t.activeTurns = make(map[PaneKey]*Event)
	t.mu.Unlock()
}

// artifactSessionKey scopes artifacts per host+session so same-named
// sessions on different hosts never see each other's files. Matches the
// sessionMeta key format ("host\x00session"); empty host = local.
func artifactSessionKey(host, session string) string {
	return host + "\x00" + session
}

func (t *Tracker) storeArtifacts(host, session string, arts []*FileArtifact) {
	if len(arts) == 0 {
		return
	}
	key := artifactSessionKey(host, session)
	t.mu.Lock()
	defer t.mu.Unlock()

	cur := t.artifacts[key]
	for _, art := range arts {
		if art == nil || art.Path == "" {
			continue
		}
		updated := false
		for i := range cur {
			existing := cur[i]
			if existing == nil || existing.Path != art.Path {
				continue
			}
			existing.Size = art.Size
			existing.ModTime = art.ModTime
			if art.DisplayPath != "" {
				existing.DisplayPath = art.DisplayPath
			}
			if art.Name != "" {
				existing.Name = art.Name
			}
			if art.Tool != "" {
				existing.Tool = art.Tool
			}
			if art.Source != "" {
				existing.Source = art.Source
			}
			cur = append(cur[:i], cur[i+1:]...)
			cur = append(cur, existing)
			updated = true
			break
		}
		if updated {
			continue
		}
		dup := *art
		cur = append(cur, &dup)
		if len(cur) > maxArtifactsPerSession {
			cur = cur[len(cur)-maxArtifactsPerSession:]
		}
	}
	if len(cur) == 0 {
		delete(t.artifacts, key)
		return
	}
	t.artifacts[key] = cur
}

func (t *Tracker) GetArtifacts(host, session string) []*FileArtifact {
	t.mu.RLock()
	defer t.mu.RUnlock()
	arts := t.artifacts[artifactSessionKey(host, session)]
	if len(arts) == 0 {
		return nil
	}
	out := make([]*FileArtifact, 0, len(arts))
	for _, art := range arts {
		if art == nil {
			continue
		}
		dup := *art
		out = append(out, &dup)
	}
	return out
}

// clearArtifactsLocked removes all artifacts for a session, assuming the lock is held
func (t *Tracker) clearArtifactsLocked(host, session string) {
	key := artifactSessionKey(host, session)
	delete(t.artifacts, key)
}

// ClearArtifacts removes all artifacts for a session and persists the change
func (t *Tracker) ClearArtifacts(host, session string) {
	t.mu.Lock()
	t.clearArtifactsLocked(host, session)
	t.mu.Unlock()
	t.persist()
}

// evictArtifactLocked removes a single artifact by path from a session's artifact list,
// assuming the lock is held. Returns true if any artifacts were removed.
func (t *Tracker) evictArtifactLocked(host, session, path string) bool {
	key := artifactSessionKey(host, session)
	arts := t.artifacts[key]
	if len(arts) == 0 {
		return false
	}
	filtered := make([]*FileArtifact, 0, len(arts))
	removed := false
	for _, art := range arts {
		if art == nil || art.Path == path {
			removed = true
			continue
		}
		filtered = append(filtered, art)
	}
	if !removed {
		return false
	}
	if len(filtered) == 0 {
		delete(t.artifacts, key)
	} else {
		t.artifacts[key] = filtered
	}
	return true
}

// EvictArtifact removes a single artifact by path from a session's artifact list
// and persists the change.
func (t *Tracker) EvictArtifact(host, session, path string) {
	t.mu.Lock()
	removed := t.evictArtifactLocked(host, session, path)
	t.mu.Unlock()
	if removed {
		t.persist()
	}
}

func (t *Tracker) SessionMetaFor(host, session string) SessionMeta {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sessionMeta[host+"\x00"+session]
}

// GetForSession returns events for a specific session
func (t *Tracker) GetForSession(session string) []*Event {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var events []*Event
	for key, evt := range t.events {
		if key.Session == session {
			events = append(events, evt)
		}
	}
	return events
}

// Subscribe returns a channel that receives tool events
func (t *Tracker) Subscribe() chan *Event {
	ch := make(chan *Event, 64)
	t.subMu.Lock()
	t.subscribers = append(t.subscribers, ch)
	t.subMu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber
func (t *Tracker) Unsubscribe(ch chan *Event) {
	t.subMu.Lock()
	defer t.subMu.Unlock()
	for i, sub := range t.subscribers {
		if sub == ch {
			t.subscribers = append(t.subscribers[:i], t.subscribers[i+1:]...)
			close(ch)
			return
		}
	}
}

// DefaultInactivityTimeout is how long a non-native-waiting tool can be quiet
// before the tracker promotes it to "waiting". This balances responsiveness
// (catching idle agents quickly) with avoiding false positives during normal
// tool use bursts.
const DefaultInactivityTimeout = 30 * time.Second

// RunInactivityPromoter starts a background goroutine that checks for tools
// without native waiting support that have gone quiet. If a tool's last
// "active" event is older than the timeout, a synthetic "waiting" event is
// recorded. This only affects tools NOT in nativeWaitingTools (e.g. copilot,
// codex, opencode) and never interferes with Claude's explicit waiting hooks.
func (t *Tracker) RunInactivityPromoter(ctx context.Context, timeout time.Duration) {
	log := logrus.WithField("component", "inactivity-promoter")
	log.WithField("timeout", timeout).Info("starting inactivity promoter")

	// Check at half the timeout interval for responsiveness
	interval := timeout / 2
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("stopping inactivity promoter")
			return
		case <-ticker.C:
			t.promoteInactive(timeout, log)
		}
	}
}

// promoteInactive checks lastActive entries and promotes stale ones to waiting.
func (t *Tracker) promoteInactive(timeout time.Duration, log *logrus.Entry) {
	now := time.Now()

	t.mu.Lock()
	var toPromote []*Event
	for key, evt := range t.lastActive {
		if now.Sub(evt.Timestamp) > timeout {
			toPromote = append(toPromote, evt)
			delete(t.lastActive, key)
		}
	}
	t.mu.Unlock()

	for _, evt := range toPromote {
		log.WithFields(logrus.Fields{
			"tool":    evt.Tool,
			"session": evt.Session,
			"window":  evt.Window,
			"pane":    evt.Pane,
		}).Debug("promoting inactive tool to waiting")

		t.Record(&Event{
			Tool:    evt.Tool,
			Status:  StatusWaiting,
			Host:    evt.Host,
			Session: evt.Session,
			Window:  evt.Window,
			Pane:    evt.Pane,
			Message: "May need attention",
		})
	}
}

// RecordProgress refreshes the progress timestamp for an active pane. Called
// from the control-mode %output handler so that an agent producing terminal
// output (e.g. a long but healthy build) is not flagged as stuck. Also clears
// any prior stuck flag so the pane can be re-evaluated and the alert dismissed.
func (t *Tracker) RecordProgress(paneID string) {
	t.mu.Lock()
	as, ok := t.activePanes[paneID]
	if !ok {
		t.mu.Unlock()
		return
	}
	now := time.Now()
	as.lastProgress = now
	wasFlagged := as.flagged
	as.flagged = false
	evt := as.evt
	t.mu.Unlock()

	// If we'd previously flagged this pane as stuck, output means it's alive
	// again. Emit an active event to clear the stuck alert.
	if wasFlagged {
		t.Record(&Event{
			Tool:    evt.Tool,
			Status:  StatusActive,
			Host:    evt.Host,
			Session: evt.Session,
			Window:  evt.Window,
			Pane:    evt.Pane,
		})
	}
}

// DefaultStuckTimeout is how long a pane claiming "active" can go without any
// tool event or terminal output before the stuck monitor flags it. A genuinely
// busy agent emits tool events or output well within this window; silence this
// long usually means a hang, infinite loop, or a tool waiting on something that
// never arrives.
const DefaultStuckTimeout = 5 * time.Minute

// promptChecker reports whether captured pane content shows an input prompt.
// Used by the stuck monitor to avoid flagging a pane as stuck when it is
// actually waiting for user input (that's the silence monitor's job).
type promptChecker func(paneID string) (isPrompt bool, ok bool)

// RunStuckMonitor starts a background goroutine that flags local panes which
// claim to be "active" but have produced no tool events and no terminal output
// for longer than timeout, and are not sitting at an input prompt. When found,
// it records a synthetic "stuck" event. Pass a nil checkPrompt to skip the
// input-prompt guard (everything quiet is treated as stuck).
func (t *Tracker) RunStuckMonitor(ctx context.Context, timeout time.Duration, checkPrompt promptChecker) {
	log := logrus.WithField("component", "stuck-monitor")
	log.WithField("timeout", timeout).Info("starting stuck monitor")

	interval := timeout / 4
	if interval < 15*time.Second {
		interval = 15 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("stopping stuck monitor")
			return
		case <-ticker.C:
			t.checkStuck(timeout, checkPrompt, log)
		}
	}
}

// checkStuck scans activePanes for entries quiet longer than timeout and
// records stuck events for them.
func (t *Tracker) checkStuck(timeout time.Duration, checkPrompt promptChecker, log *logrus.Entry) {
	now := time.Now()

	type candidate struct {
		paneID string
		evt    *Event
	}
	var candidates []candidate

	t.mu.Lock()
	for paneID, as := range t.activePanes {
		if as.flagged {
			continue
		}
		if now.Sub(as.lastProgress) > timeout {
			candidates = append(candidates, candidate{paneID: paneID, evt: as.evt})
		}
	}
	t.mu.Unlock()

	for _, c := range candidates {
		// Skip panes sitting at an input prompt: that's "waiting", not stuck.
		if checkPrompt != nil {
			if isPrompt, ok := checkPrompt(c.paneID); ok && isPrompt {
				log.WithField("pane", c.paneID).Trace("quiet pane is at an input prompt, not stuck")
				continue
			}
		}

		t.mu.Lock()
		as, ok := t.activePanes[c.paneID]
		if !ok || as.flagged {
			t.mu.Unlock()
			continue
		}
		as.flagged = true
		t.mu.Unlock()

		log.WithFields(logrus.Fields{
			"tool":    c.evt.Tool,
			"session": c.evt.Session,
			"window":  c.evt.Window,
			"pane":    c.paneID,
		}).Debug("flagging pane as stuck")

		t.Record(&Event{
			Tool:    c.evt.Tool,
			Status:  StatusStuck,
			Host:    c.evt.Host,
			Session: c.evt.Session,
			Window:  c.evt.Window,
			Pane:    c.paneID,
			Message: "No progress for a while, may be stuck",
		})
	}
}
