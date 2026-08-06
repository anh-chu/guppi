package state

import (
	"fmt"
	"time"

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
		name := rec.Name
		if name == "" {
			name = string(rec.Ref.Session)
		}

		view := NewSessionView(rec, enricher)
		projectPath := view.Runtime.CurrentPath
		if projectPath == "" {
			projectPath = rec.Cwd
		}

		session := &model.Session{
			ID:            string(rec.Ref.Session),
			Name:          name,
			Backend:       "daemon",
			Created:       rec.Created,
			ProjectPath:   projectPath,
			DisplayName:   rec.Name,
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

