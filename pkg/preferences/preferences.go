package preferences

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/anh-chu/termyard/pkg/config"
)

type Terminal struct {
	FontSize         int    `json:"font_size"`
	FontFamily       string `json:"font_family"`
	Scrollback       int    `json:"scrollback"`
	Renderer         string `json:"renderer"`
	UnicodeGraphemes bool   `json:"unicode_graphemes"`
	PredictiveEcho   bool   `json:"predictive_echo"`
}

type Sidebar struct {
	DefaultCollapsed bool   `json:"default_collapsed"`
	CollapseMode     string `json:"collapse_mode"`
}

type Notifications struct {
	Statuses []string `json:"statuses"`
}

type AgentBanner struct {
	AutoDismissSeconds int `json:"auto_dismiss_seconds"`
}

// APIKeyMask is the placeholder returned in place of a stored AI naming API
// key on reads, so the secret is never sent to the browser. On write, a value
// equal to this mask means "keep the existing key".
const APIKeyMask = "••••••••"

// AINaming configures the optional AI session namer. When Enabled is false the
// namer is off regardless of endpoint. Empty Endpoint/Model fall back to
// TERMYARD_NAMER_* / TERMYARD_OPENAI_* environment variables.
type AINaming struct {
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"api_key"`
	Model    string `json:"model"`
}

type Preferences struct {
	Terminal                Terminal      `json:"terminal"`
	Theme                   string        `json:"theme"`
	Sidebar                 Sidebar       `json:"sidebar"`
	DefaultView             string        `json:"default_view"`
	Notifications           Notifications `json:"notifications"`
	AgentBanner             AgentBanner   `json:"agent_banner"`
	LockTimeoutMinutes      int           `json:"lock_timeout_minutes"`
	FullscreenHideAlerts    bool          `json:"fullscreen_hide_alerts"`
	DefaultAgent            string        `json:"default_agent"`
	AINaming                AINaming      `json:"ai_naming"`
	// Inverted so the zero value means enabled. PUT /api/preferences decodes
	// the body into a zero-valued Preferences and replaces the store wholesale,
	// so any client that omits this key would persist it as false. A default-true
	// field would therefore be switched off for good by a single save from a tab
	// running a bundle older than this field, with no way back but the toggle.
	WikiDisabled            bool          `json:"wiki_disabled"`
}

func Default() *Preferences {
	return &Preferences{
		Terminal: Terminal{
			FontSize:         13,
			FontFamily:       "Space Mono",
			Scrollback:       5000,
			Renderer:         "dom",
			UnicodeGraphemes: false,
			PredictiveEcho:   false,
		},
		Theme: "dark",
		Sidebar: Sidebar{
			DefaultCollapsed: false,
			CollapseMode:     "small",
		},
		DefaultView: "overview",
		Notifications: Notifications{
			Statuses: []string{"waiting", "error", "completed"},
		},
		AgentBanner: AgentBanner{
			AutoDismissSeconds: 0,
		},
		LockTimeoutMinutes:      30,
		FullscreenHideAlerts:    true,
		DefaultAgent:            "claude",
		AINaming: AINaming{
			Enabled: false,
			Model:   "gpt-4o-mini",
		},
	}
}

type Store struct {
	mu   sync.RWMutex
	path string
	data *Preferences
}

func NewStore() (*Store, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	s := &Store{
		path: filepath.Join(dir, "preferences.json"),
		data: Default(),
	}

	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	return s, nil
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	// Start from defaults, then overlay saved values
	prefs := Default()
	if err := json.Unmarshal(raw, prefs); err != nil {
		return err
	}
	s.data = prefs
	return nil
}

func (s *Store) save() error {
	return config.WriteJSON(s.path, s.data, 0o644)
}

func (s *Store) Get() *Preferences {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Return a copy
	cp := *s.data
	cp.Notifications.Statuses = append([]string{}, s.data.Notifications.Statuses...)
	return &cp
}

func (s *Store) Update(prefs *Preferences) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Store a copy so callers can't mutate our internal state via the pointer
	// they passed in (e.g. masking an API key after Update).
	cp := *prefs
	cp.Notifications.Statuses = append([]string{}, prefs.Notifications.Statuses...)
	s.data = &cp
	return s.save()
}
