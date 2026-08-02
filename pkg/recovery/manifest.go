package recovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/anh-chu/termyard/pkg/config"
)

const currentVersion = 1

type paneSnapshot struct {
	ID             string `json:"id"`
	Index          int    `json:"index"`
	Active         bool   `json:"active"`
	CWD            string `json:"cwd"`
	StartCommand   string `json:"start_command"`
	CurrentCommand string `json:"current_command"`
	AgentType      string `json:"agent_type,omitempty"`
}

type windowSnapshot struct {
	Index  int            `json:"index"`
	Name   string         `json:"name"`
	Active bool           `json:"active"`
	Layout string         `json:"layout"`
	Panes  []paneSnapshot `json:"panes"`
}

type sessionSnapshot struct {
	Name           string           `json:"name"`
	ProjectPath    string           `json:"project_path,omitempty"`
	AgentType      string           `json:"agent_type,omitempty"`
	AgentSessionID string           `json:"agent_session_id,omitempty"`
	ScheduleID     string           `json:"schedule_id,omitempty"`
	Windows        []windowSnapshot `json:"windows"`
}

// manifest stores crash-recovery snapshot data.
type manifest struct {
	Version    int               `json:"version"`
	UpdatedAt  time.Time         `json:"updated_at"`
	Generation uint64            `json:"generation"`
	Sessions   []sessionSnapshot `json:"sessions"`
}

func manifestPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "recovery-manifest.json"), nil
}

func load() (*manifest, error) {
	path, err := manifestPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &manifest{Version: currentVersion}, nil
		}
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if m.Version == 0 {
		m.Version = currentVersion
	}
	return &m, nil
}

// ForgetSession removes a session from the persisted recovery manifest synchronously.
//
// Call this on an intentional kill so crash recovery cannot resurrect it.
func ForgetSession(name string) error {
	if name == "" {
		return nil
	}
	m, err := load()
	if err != nil {
		return err
	}
	if m == nil || len(m.Sessions) == 0 {
		return nil
	}
	filtered := m.Sessions[:0]
	removed := false
	for _, s := range m.Sessions {
		if s.Name == name {
			removed = true
			continue
		}
		filtered = append(filtered, s)
	}
	if !removed {
		return nil
	}
	m.Sessions = filtered
	m.Generation++
	m.UpdatedAt = time.Now()
	return m.save()
}

func (m *manifest) save() error {
	if m.Version == 0 {
		m.Version = currentVersion
	}
	path, err := manifestPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
