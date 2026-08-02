package state

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/namer"
	"github.com/anh-chu/termyard/pkg/pty"
)

// SessionMetadata holds mutable, display-oriented state for a single session.
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
	LastNamedAt      time.Time // last time a generated name was successfully applied (not persisted)

	// LastNamingAttemptAt records the most recent start of an automatic naming
	// attempt, whether it succeeded or failed. It drives the debounce/backoff
	// timer for automatic naming and is not persisted.
	LastNamingAttemptAt time.Time `json:"-"`

	// NamingFailureCount tracks consecutive automatic naming failures for this
	// session. It resets to zero when an automatic naming attempt succeeds.
	// It is not persisted and does not affect manual/explicit renames.
	NamingFailureCount int `json:"-"`
}

// Manager holds the central state tree.
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

	// Subscribers for state changes.
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

// StateEvent represents a change in the state tree.
type StateEvent struct {
	Type     string      `json:"type"`
	Session  string      `json:"session,omitempty"`
	Host     string      `json:"host,omitempty"`
	HostName string      `json:"host_name,omitempty"`
	Data     interface{} `json:"data,omitempty"`
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

	// nameRefreshInterval debounces continuous AI re-naming of agent sessions so a
	// burst of completed turns does not hammer the namer endpoint.
	nameRefreshInterval = 45 * time.Second
)

// SetDaemonRegistry wires the daemon registry into the state manager.
func (m *Manager) SetDaemonRegistry(reg DaemonRegistry) {
	m.daemonReg = reg
}

// NewManager creates a new state manager.
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
