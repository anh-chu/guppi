package state

import (
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/model"
)

// ProjectCatalogToSessions builds v1 model.Session values from a v2 catalog
// snapshot. It never writes to the store; it only projects.
func ProjectCatalogToSessions(catalog *Catalog, enricher RuntimeEnricher) []*model.Session {
	snap := catalog.LocalCatalogSnapshot()
	out := make([]*model.Session, 0, len(snap.Sessions))
	for _, rec := range snap.Sessions {
		// Ended/dismissed sessions are not part of the active v1 session tree.
		if rec.Phase == SessionPhaseCleanlyEnded || rec.Phase == SessionPhaseDismissed {
			continue
		}
		name := rec.Compat.Name
		if name == "" {
			name = string(rec.Ref.Session)
		}

		view := NewSessionView(rec, enricher)
		projectPath := view.Runtime.CurrentPath
		if projectPath == "" {
			projectPath = rec.Compat.Cwd
		}

		session := &model.Session{
			ID:            string(rec.Ref.Session),
			Name:          name,
			Backend:       "daemon",
			Created:       rec.Created,
			ProjectPath:   projectPath,
			DisplayName:   rec.Compat.Name,
			PromptPreview: view.Runtime.PromptPreview,
			LastActivity:  view.Runtime.LastActivity,
		}
		if session.LastActivity.IsZero() {
			session.LastActivity = time.Now()
		}

		pid := view.Runtime.ShellPID
		if pid == 0 {
			pid = view.Runtime.DaemonPID
		}
		paneID := fmt.Sprintf("%s:%d.%d", rec.Ref.Session, rec.Ref.Window, rec.Ref.Pane)
		pane := &model.Pane{
			ID:             paneID,
			Active:         true,
			CurrentPath:    projectPath,
			CurrentCommand: view.Runtime.CurrentCommand,
			PID:            pid,
		}
		win := &model.Window{
			ID:     string(rec.Ref.Session) + ":0",
			Name:   name,
			Index:  0,
			Active: true,
			Panes:  []*model.Pane{pane},
		}
		session.Windows = []*model.Window{win}
		out = append(out, session)
	}
	return out
}

// compareV2Shadow logs differences between the legacy v1 session set and the
// v2 catalog projection. It is a read-only diagnostic while v2 stays in shadow.
func (m *Manager) compareV2Shadow() {
	if m.v2Catalog == nil {
		return
	}

	v2Sessions := ProjectCatalogToSessions(m.v2Catalog, m.v2Enricher)
	v2Set := make(map[string]*model.Session, len(v2Sessions))
	for _, s := range v2Sessions {
		if s != nil {
			v2Set[s.Name] = s
		}
	}

	m.mu.RLock()
	legacySet := make(map[string]*model.Session, len(m.sessions))
	for name, s := range m.sessions {
		legacySet[name] = s
	}
	m.mu.RUnlock()

	for name, v2 := range v2Set {
		legacy, ok := legacySet[name]
		if !ok {
			logrus.WithFields(logrus.Fields{
				"session":     name,
				"v2_phase":    v2PhaseLabel(v2),
				"reason_code": "v2_only",
			}).Debug("v2 catalog session not present in legacy state")
			continue
		}
		v2Live := v2IsLive(v2)
		legacyLive := legacy != nil
		if v2Live != legacyLive {
			logrus.WithFields(logrus.Fields{
				"session":      name,
				"v2_phase":     v2PhaseLabel(v2),
				"legacy_phase": "live",
				"reason_code":  "phase_mismatch",
			}).Debug("v2/legacy phase mismatch")
			continue
		}
		if v2.ProjectPath != legacy.ProjectPath {
			logrus.WithFields(logrus.Fields{
				"session":     name,
				"v2_path":     v2.ProjectPath,
				"legacy_path": legacy.ProjectPath,
				"reason_code": "path_mismatch",
			}).Debug("v2/legacy project path mismatch")
		}
	}

	for name, legacy := range legacySet {
		if _, ok := v2Set[name]; !ok {
			logrus.WithFields(logrus.Fields{
				"session":     name,
				"reason_code": "legacy_only",
			}).Debug("legacy session not present in v2 catalog")
			_ = legacy
		}
	}
}

func v2IsLive(s *model.Session) bool {
	if s == nil {
		return false
	}
	return true
}

func v2PhaseLabel(s *model.Session) string {
	if s == nil {
		return "nil"
	}
	// The projection carries phase in a hidden marker for diagnostics.
	if s.Backend == "daemon" {
		return "live"
	}
	return "unknown"
}
