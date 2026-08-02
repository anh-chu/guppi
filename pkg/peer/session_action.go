package peer

import (
	"context"
	"encoding/json"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/recovery"
	"github.com/anh-chu/termyard/pkg/sessionlaunch"
)

// handleActionMessage routes a single session-action request.
func handleActionMessage(msg *Message, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	var p SessionActionPayload
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		return
	}
	handleSessionAction(&p, pc, deps, log)
}

func handleSessionAction(payload *SessionActionPayload, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	if deps.Launch == nil {
		log.Warn("no launch service available for session action")
		return
	}
	switch payload.Action {
	case "new":
		var params struct {
			Name           string `json:"name"`
			Path           string `json:"path,omitempty"`
			Command        string `json:"command,omitempty"`
			WorktreeBranch string `json:"worktree_branch,omitempty"`
			ScheduleID     string `json:"schedule_id,omitempty"`
		}
		if err := json.Unmarshal(payload.Params, &params); err != nil || params.Name == "" {
			return
		}
		if deps.Launch == nil {
			log.Warn("no launch service available for session action")
			return
		}
		req := sessionlaunch.Request{
			Name:           params.Name,
			Path:           params.Path,
			Command:        params.Command,
			WorktreeBranch: params.WorktreeBranch,
			ScheduleID:     params.ScheduleID,
		}
		if _, err := deps.Launch.Create(context.Background(), req); err != nil {
			log.WithError(err).Warn("new session via peer failed")
			return
		}
		sendStateUpdate(pc, deps)

	case "rename":
		// Daemon sessions don't support rename — no-op.

	case "select-window":
		// Daemon sessions are single-pane — no-op.

	case "kill":
		var params struct {
			ID   string `json:"id,omitempty"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(payload.Params, &params); err != nil || params.Name == "" {
			return
		}
		if err := deps.DaemonReg.Kill(params.Name); err != nil {
			log.WithError(err).Warn("kill session via peer failed")
		}
		if err := recovery.ForgetSession(params.Name); err != nil {
			log.WithError(err).Warn("failed to remove session from recovery manifest")
		}
		sendStateUpdate(pc, deps)

	case "regenerate-name":
		var params struct {
			Session string `json:"session"`
			Name    string `json:"name"`
		}
		if err := json.Unmarshal(payload.Params, &params); err != nil || params.Session == "" {
			return
		}
		if deps.LocalMgr == nil {
			log.Warn("no state manager available for regenerate-name action")
			return
		}
		if params.Name != "" {
			deps.LocalMgr.ApplyAIName(params.Session, params.Name)
		} else if _, err := deps.LocalMgr.RegenerateName(params.Session); err != nil {
			log.WithError(err).Warn("regenerate name via peer failed")
			return
		}
		sendStateUpdate(pc, deps)

	case "set-display-name":
		var params struct {
			Session     string `json:"session"`
			DisplayName string `json:"display_name"`
			Clear       bool   `json:"clear"`
		}
		if err := json.Unmarshal(payload.Params, &params); err != nil || params.Session == "" {
			return
		}
		if deps.LocalMgr == nil {
			log.Warn("no state manager available for set-display-name action")
			return
		}
		deps.LocalMgr.SetDisplayName(params.Session, params.DisplayName, !params.Clear)
		sendStateUpdate(pc, deps)
	default:
		log.WithField("action", payload.Action).Debug("unknown session action")
	}
}
