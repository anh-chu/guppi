package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	wp "github.com/SherClockHolmes/webpush-go"

	"github.com/anh-chu/termyard/pkg/agentcheck"
	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/git"
	"github.com/anh-chu/termyard/pkg/groupsync"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/namer"
	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/preferences"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/sessionlaunch"
	"github.com/anh-chu/termyard/pkg/stats"
	"github.com/anh-chu/termyard/pkg/toolevents"
	"github.com/anh-chu/termyard/pkg/ws"
)

// registerSessionsRoutes mounts the protected session/activity/stats/sync
// endpoints under /api. Callers must apply auth middleware separately.
func registerSessionsRoutes(r chi.Router, opts *Options, hub *ws.Hub, coordinator *groupNamingCoordinator) {

	// Agent status -- check which agents are installed/configured
	r.Get("/agent-status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(agentcheck.CheckAgents())
	})
	r.Get("/update", func(w http.ResponseWriter, r *http.Request) {
		handleUpdateStatus(w, r)
	})
	r.Post("/update/apply", func(w http.ResponseWriter, r *http.Request) {
		handleUpdateApply(w, r, opts)
	})
	r.Post("/update/check", func(w http.ResponseWriter, r *http.Request) {
		handleUpdateCheck(w, r, opts)
	})

	r.Get("/sessions", func(w http.ResponseWriter, r *http.Request) {
		var sessions []*model.Session
		if opts.PeerMgr != nil {
			sessions = opts.PeerMgr.GetAllSessions()
		} else {
			sessions = opts.StateMgr.GetSessions()
		}
		localHost := ""
		if opts.PeerMgr != nil {
			localHost = opts.PeerMgr.LocalID()
		}
		enrichSessionsFromTracker(sessions, opts.Tracker, localHost)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
	})

	r.Get("/hosts", func(w http.ResponseWriter, r *http.Request) {
		if opts.PeerMgr != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(opts.PeerMgr.GetHosts())
		} else {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]interface{}{})
		}
	})

	// Read-only snapshot of a session's primary pane visible buffer.
	// Works for local and remote (peer) sessions; no PTY attach.
	r.Get("/pane-capture", func(w http.ResponseWriter, r *http.Request) {
		session := r.URL.Query().Get("session")
		if session == "" {
			http.Error(w, "session is required", http.StatusBadRequest)
			return
		}
		lines := 40
		if v, err := strconv.Atoi(r.URL.Query().Get("lines")); err == nil && v > 0 {
			lines = v
		}
		host := r.URL.Query().Get("host")

		// Remote peer -- request capture over the control link.
		if host != "" && opts.PeerMgr != nil && !opts.PeerMgr.IsLocal(host) {
			if opts.CaptureReg == nil {
				http.Error(w, "capture unavailable", http.StatusInternalServerError)
				return
			}
			peerConn := opts.PeerMgr.GetPeerConnection(host)
			if peerConn == nil {
				http.Error(w, "peer not connected", http.StatusBadGateway)
				return
			}
			token := peer.NewToken()
			msg, err := peer.NewMessage(peer.MsgCapturePane, peer.CapturePanePayload{
				Token: token, Session: session, Lines: lines,
			})
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			// Register before enqueue so a fast reply cannot be dropped.
			ch, cancel := opts.CaptureReg.Register(token)
			defer cancel()
			if !peerConn.Enqueue(msg) {
				http.Error(w, "peer send queue full", http.StatusBadGateway)
				return
			}
			select {
			case res := <-ch:
				if res.Error != "" {
					http.Error(w, "capture failed: "+res.Error, http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"text": res.Text})
			case <-time.After(3 * time.Second):
				http.Error(w, "peer capture timed out", http.StatusGatewayTimeout)
			}
			return
		}

		// Local session -- capture from daemon registry.
		if opts.DaemonReg == nil {
			http.Error(w, "daemon registry unavailable", http.StatusInternalServerError)
			return
		}
		text, err := opts.DaemonReg.Capture(session)
		if err != nil {
			http.Error(w, "capture failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"text": model.LastLines(text, lines)})
	})

	r.Post("/session/new", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name           string `json:"name"`
			Host           string `json:"host,omitempty"`
			Path           string `json:"path,omitempty"`
			Command        string `json:"command,omitempty"`
			AgentType      string `json:"agent_type,omitempty"`
			WorktreeBranch string `json:"worktree_branch,omitempty"`
			ScheduleID     string `json:"schedule_id,omitempty"`
			Backend        string `json:"backend,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		localHost := ""
		if opts.PeerMgr != nil {
			localHost = opts.PeerMgr.LocalID()
		}
		launchReq := sessionlaunch.Request{
			Name:           req.Name,
			Host:           req.Host,
			Path:           req.Path,
			Command:        req.Command,
			AgentType:      req.AgentType,
			WorktreeBranch: req.WorktreeBranch,
			ScheduleID:     req.ScheduleID,
			LocalHost:      localHost,
		}
		if req.Host == "" || opts.PeerMgr == nil || opts.PeerMgr.IsLocal(req.Host) {
			launchReq.Fallback = fmt.Sprintf("shell-%d", time.Now().UnixMilli())
		}
		res, err := opts.Launch.Create(r.Context(), launchReq)
		if err != nil {
			switch {
			case errors.Is(err, sessionlaunch.ErrInvalidInput):
				http.Error(w, err.Error(), http.StatusBadRequest)
			case errors.Is(err, sessionlaunch.ErrPeerUnavailable), errors.Is(err, sessionlaunch.ErrPeerQueueFull):
				http.Error(w, err.Error(), http.StatusBadGateway)
			default:
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"name": res.Name})
	})

	r.Post("/session/display-name", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Session     string `json:"session"`
			DisplayName string `json:"display_name"`
			Clear       bool   `json:"clear,omitempty"`
			Host        string `json:"host,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Session == "" {
			http.Error(w, "session is required", http.StatusBadRequest)
			return
		}

		// Remote host -- forward via peer connection. The new label propagates
		// back through the peer's state update broadcast.
		if req.Host != "" && opts.PeerMgr != nil && !opts.PeerMgr.IsLocal(req.Host) {
			peerConn := opts.PeerMgr.GetPeerConnection(req.Host)
			if peerConn == nil {
				http.Error(w, "peer not connected", http.StatusBadGateway)
				return
			}
			params, _ := json.Marshal(map[string]any{
				"session":      req.Session,
				"display_name": req.DisplayName,
				"clear":        req.Clear,
			})
			msg, _ := peer.NewMessage(peer.MsgSessionAction, peer.SessionActionPayload{
				Action: "set-display-name",
				Params: params,
			})
			if peerConn.Enqueue(msg) {
				w.WriteHeader(http.StatusNoContent)
			} else {
				http.Error(w, "peer send queue full", http.StatusBadGateway)
			}
			return
		}

		if opts.StateMgr == nil {
			http.Error(w, "state manager unavailable", http.StatusInternalServerError)
			return
		}
		// clear=true resets to AI/auto naming; otherwise mark user-set.
		opts.StateMgr.SetDisplayName(req.Session, req.DisplayName, !req.Clear)
		w.WriteHeader(http.StatusNoContent)
	})

	// Manually (re)generate an AI display name for a session on demand.
	// Bypasses the one-shot guard and clears any prior manual name.
	r.Post("/session/regenerate-name", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Session string `json:"session"`
			Host    string `json:"host,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Session == "" {
			http.Error(w, "session is required", http.StatusBadRequest)
			return
		}

		// Remote host -- name it here (the peer process may have no namer
		// configured) and forward the chosen name to the peer to apply. The
		// applied name propagates back via the peer's state update.
		if req.Host != "" && opts.PeerMgr != nil && !opts.PeerMgr.IsLocal(req.Host) {
			peerConn := opts.PeerMgr.GetPeerConnection(req.Host)
			if peerConn == nil {
				http.Error(w, "peer not connected", http.StatusBadGateway)
				return
			}
			if opts.StateMgr == nil {
				http.Error(w, "state manager unavailable", http.StatusInternalServerError)
				return
			}

			// Find the target session and its siblings on that host.
			nc := namer.Context{Kind: namer.KindShell}
			found := false
			for _, s := range opts.PeerMgr.GetAllSessions() {
				if s.Host != req.Host {
					continue
				}
				if s.Name == req.Session {
					found = true
					nc.Workdir = s.ProjectPath
					nc.Current = s.DisplayName
					nc.Agent = s.AgentType
					nc.UserPrompt = s.UserPrompt
					nc.AgentMsg = s.LastAgentMessage
					if s.AgentType != "" {
						nc.Kind = namer.KindAgent
					}
				} else {
					label := s.DisplayName
					if label == "" {
						label = s.Name
					}
					nc.Taken = append(nc.Taken, label)
				}
			}
			if !found {
				http.Error(w, "session not found on host", http.StatusNotFound)
				return
			}

			ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
			name, err := opts.StateMgr.GenerateName(ctx, nc)
			cancel()
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}

			params, _ := json.Marshal(map[string]string{"session": req.Session, "name": name})
			msg, _ := peer.NewMessage(peer.MsgSessionAction, peer.SessionActionPayload{
				Action: "regenerate-name",
				Params: params,
			})
			if !peerConn.Enqueue(msg) {
				http.Error(w, "peer send queue full", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"name": name})
			return
		}

		if opts.StateMgr == nil {
			http.Error(w, "state manager unavailable", http.StatusInternalServerError)
			return
		}
		name, err := opts.StateMgr.RegenerateName(req.Session)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"name": name})
	})

	// AI-name a layout group from its member session labels. Groups are
	// a frontend-only concept, so this is stateless: it returns a name,
	// the client persists it.
	r.Post("/group/name", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID      string              `json:"id,omitempty"`
			Members []namer.GroupMember `json:"members"`
			Current string              `json:"current,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		// Explicit force path: name a persisted group from its tree and persist
		// the result server-side. Preferred by new clients.
		if req.ID != "" && coordinator != nil {
			group, err := coordinator.Force(r.Context(), req.ID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"name": group.Name, "group": group})
			return
		}

		// Legacy stateless path: clients send members and persist the name
		// themselves. Kept for one release for older callers.
		if len(req.Members) == 0 {
			http.Error(w, "members is required", http.StatusBadRequest)
			return
		}
		if opts.StateMgr == nil {
			http.Error(w, "state manager unavailable", http.StatusInternalServerError)
			return
		}
		name, err := opts.StateMgr.GenerateGroupName(req.Members, req.Current)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"name": name})
	})

	r.Post("/session/rename", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OldName string `json:"old_name"`
			NewName string `json:"new_name"`
			Host    string `json:"host,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OldName == "" || req.NewName == "" {
			http.Error(w, "old_name and new_name are required", http.StatusBadRequest)
			return
		}
		if err := model.ValidateSessionName(req.NewName); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Remote host -- forward via peer connection
		if req.Host != "" && opts.PeerMgr != nil && !opts.PeerMgr.IsLocal(req.Host) {
			peerConn := opts.PeerMgr.GetPeerConnection(req.Host)
			if peerConn == nil {
				http.Error(w, "peer not connected", http.StatusBadGateway)
				return
			}
			params, _ := json.Marshal(map[string]string{
				"old_name": req.OldName,
				"new_name": req.NewName,
			})
			msg, _ := peer.NewMessage(peer.MsgSessionAction, peer.SessionActionPayload{
				Action: "rename",
				Params: params,
			})
			if peerConn.Enqueue(msg) {
				w.WriteHeader(http.StatusNoContent)
			} else {
				http.Error(w, "peer send queue full", http.StatusBadGateway)
			}
			return
		}

		// Daemon sessions can't be renamed at the OS level; update display name only.
		opts.StateMgr.SetDisplayName(req.OldName, req.NewName, true)
		if opts.RefreshSessions != nil {
			opts.RefreshSessions()
		}
		w.WriteHeader(http.StatusNoContent)
	})

	r.Post("/session/select-window", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Session string `json:"session"`
			Window  int    `json:"window"`
			Host    string `json:"host,omitempty"`
			Pane    string `json:"pane,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Session == "" {
			http.Error(w, "session and window are required", http.StatusBadRequest)
			return
		}

		// Remote host -- forward via peer connection
		if req.Host != "" && opts.PeerMgr != nil && !opts.PeerMgr.IsLocal(req.Host) {
			peerConn := opts.PeerMgr.GetPeerConnection(req.Host)
			if peerConn == nil {
				http.Error(w, "peer not connected", http.StatusBadGateway)
				return
			}
			params, _ := json.Marshal(map[string]interface{}{
				"session": req.Session,
				"window":  req.Window,
				"pane":    req.Pane,
			})
			msg, _ := peer.NewMessage(peer.MsgSessionAction, peer.SessionActionPayload{
				Action: "select-window",
				Params: params,
			})
			if peerConn.Enqueue(msg) {
				w.WriteHeader(http.StatusNoContent)
			} else {
				http.Error(w, "peer send queue full", http.StatusBadGateway)
			}
			return
		}

		// Daemon sessions are single-window/single-pane; select is a no-op.
		w.WriteHeader(http.StatusNoContent)
	})

	r.Post("/session/kill", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID             string `json:"id,omitempty"`
			Name           string `json:"name"`
			Host           string `json:"host,omitempty"`
			RemoveWorktree bool   `json:"remove_worktree,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}

		// Remote host -- forward via peer connection
		if req.Host != "" && opts.PeerMgr != nil && !opts.PeerMgr.IsLocal(req.Host) {
			peerConn := opts.PeerMgr.GetPeerConnection(req.Host)
			if peerConn == nil {
				http.Error(w, "peer not connected", http.StatusBadGateway)
				return
			}
			params, _ := json.Marshal(map[string]string{"id": req.ID, "name": req.Name})
			msg, _ := peer.NewMessage(peer.MsgSessionAction, peer.SessionActionPayload{
				Action: "kill",
				Params: params,
			})
			if peerConn.Enqueue(msg) {
				w.WriteHeader(http.StatusNoContent)
			} else {
				http.Error(w, "peer send queue full", http.StatusBadGateway)
			}
			return
		}

		// Capture worktree path before state is cleared.
		var worktreePath string
		if req.RemoveWorktree && opts.StateMgr != nil {
			worktreePath = opts.StateMgr.GetSessionProjectPath(req.Name)
		}

		// Kill daemon, remove from state, and forget from recovery.
		// Service.Kill logs failures with full context (session + reason), so we only
		// check the error for control flow, not re-log it.
		_ = opts.Launch.Kill(req.Name, "rest-kill")

		// Remove the linked worktree if requested. Non-fatal -- session is
		// already gone; log and continue.

		if req.RemoveWorktree && worktreePath != "" {
			if err := git.RemoveWorktree(worktreePath); err != nil {
				logrus.WithError(err).WithField("path", worktreePath).Warn("git worktree remove failed")
			}
		}

		w.WriteHeader(http.StatusNoContent)
	})

	// Crashed sessions recovery endpoints
	r.Get("/crashed-sessions", func(w http.ResponseWriter, r *http.Request) {
		if opts.DaemonReg == nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]interface{}{})
			return
		}
		crashed := opts.DaemonReg.CrashedSessions()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(crashed)
	})

	r.Post("/crashed-sessions/{id}/recover", func(w http.ResponseWriter, r *http.Request) {
		if opts.DaemonReg == nil {
			http.Error(w, "daemon registry unavailable", http.StatusServiceUnavailable)
			return
		}
		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		var body struct {
			Shell string `json:"shell,omitempty"`
			Cwd   string `json:"cwd,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			// Empty body is fine; use crashed-record defaults.
		}
		if err := opts.DaemonReg.RecoverSession(id, body.Shell, body.Cwd); err != nil {
			http.Error(w, "recover failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if opts.RefreshSessions != nil {
			opts.RefreshSessions()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"ok": "true", "session": id})
	})

	r.Delete("/crashed-sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		if opts.DaemonReg == nil {
			http.Error(w, "daemon registry unavailable", http.StatusServiceUnavailable)
			return
		}
		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		if err := opts.DaemonReg.DismissSession(id); err != nil {
			http.Error(w, "dismiss failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	r.Delete("/crashed-sessions", func(w http.ResponseWriter, r *http.Request) {
		if opts.DaemonReg == nil {
			http.Error(w, "daemon registry unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := opts.DaemonReg.DismissAll(); err != nil {
			http.Error(w, "dismiss all failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Tool event query/management (auth-protected)
	r.Get("/tool-events", func(w http.ResponseWriter, r *http.Request) {
		session := r.URL.Query().Get("session")
		var events []*toolevents.Event
		if session != "" {
			events = opts.Tracker.GetForSession(session)
		} else {
			events = opts.Tracker.GetAll()
		}

		// Merge in auto-detected agents that don't have a tracked event.
		// These are "active" agents found via process-tree inspection
		// (e.g. codex/copilot running as node).
		if opts.Detector != nil {
			tracked := make(map[string]bool)
			for _, evt := range events {
				if evt.Pane != "" {
					tracked[evt.Pane] = true
				}
			}
			for paneID, tool := range opts.Detector.DetectedPanes() {
				if tracked[paneID] {
					continue
				}
				info := opts.Detector.PaneInfo(paneID)
				if session != "" && info.Session != session {
					continue
				}
				evt := &toolevents.Event{
					Tool:         tool,
					Status:       toolevents.StatusActive,
					Session:      info.Session,
					Window:       info.Window,
					Pane:         paneID,
					Message:      "auto-detected",
					AutoDetected: true,
				}
				// Stamp local host identity so frontend session key matching works
				if opts.PeerMgr != nil {
					evt.Host = opts.PeerMgr.LocalID()
					evt.HostName = opts.PeerMgr.LocalName()
				}
				events = append(events, evt)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(events)
	})

	r.Get("/artifacts", func(w http.ResponseWriter, r *http.Request) {
		session := r.URL.Query().Get("session")
		if session == "" {
			http.Error(w, "session is required", http.StatusBadRequest)
			return
		}
		host := r.URL.Query().Get("host")
		artifacts := opts.Tracker.GetArtifacts(host, session)
		for _, art := range artifacts {
			if art == nil || art.Path == "" {
				continue
			}
			info, err := os.Stat(art.Path)
			if err != nil || info.IsDir() {
				art.Stale = true
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"artifacts": artifacts})
	})

	// Dedicated file upload -- streams a browser-supplied file into
	// private temp storage on the session's host and returns the
	// shell-quoted path for PTY injection. No product size cap.
	// Route: POST /api/upload?session=<name>&host=<id>&filename=<name>
	r.Post("/upload", func(w http.ResponseWriter, r *http.Request) {
		handleUpload(w, r, opts)
	})

	// Authoritative set of in-progress hook-based agent turns. The frontend
	// reconciles its "working" badge against this on each periodic refresh so
	// a dropped "completed" WebSocket frame self-heals.
	r.Get("/active-turns", func(w http.ResponseWriter, r *http.Request) {
		type turn struct {
			Host    string `json:"host,omitempty"`
			Session string `json:"session"`
		}
		turns := opts.Tracker.ActiveTurns()
		out := make([]turn, 0, len(turns))
		for _, evt := range turns {
			out = append(out, turn{Host: evt.Host, Session: evt.Session})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	r.Delete("/tool-events", func(w http.ResponseWriter, r *http.Request) {
		opts.Tracker.ClearAll()
		w.WriteHeader(http.StatusNoContent)
	})

	r.Delete("/tool-event", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Host    string `json:"host"`
			Session string `json:"session"`
			Window  int    `json:"window"`
			Pane    string `json:"pane"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Session == "" {
			http.Error(w, "session is required", http.StatusBadRequest)
			return
		}
		opts.Tracker.Clear(req.Host, req.Session, req.Window, req.Pane)
		w.WriteHeader(http.StatusNoContent)
	})

	// Stats endpoint -- aggregate overview data
	r.Get("/stats", func(w http.ResponseWriter, r *http.Request) {
		sessions := opts.StateMgr.GetSessions()
		// Enumerate panes from daemon registry.
		var allPanes []*model.Pane
		if opts.DaemonReg != nil {
			for _, d := range opts.DaemonReg.List() {
				allPanes = append(allPanes, &model.Pane{
					ID:             d.ID + ":0.0",
					CurrentCommand: d.Shell,
					CurrentPath:    d.Cwd,
				})
			}
		}

		agentCommands := map[string]bool{
			"claude": true, "codex": true, "copilot": true, "opencode": true,
		}
		totalWindows := 0
		attachedSessions := 0
		agentPanes := 0
		for _, s := range sessions {
			if s.Attached {
				attachedSessions++
			}
			totalWindows += len(s.Windows)
		}

		// Build a set of panes with known agent tool events (from hooks
		// or process-tree detection). This catches agents like codex and
		// copilot that show up as "node" in pane_current_command.
		toolEvents := opts.Tracker.GetAll()
		agentEventPanes := make(map[string]bool)
		for _, evt := range toolEvents {
			if evt.Pane != "" {
				agentEventPanes[evt.Pane] = true
			}
		}
		// Also include panes detected via process tree inspection
		if opts.Detector != nil {
			for paneID := range opts.Detector.DetectedPanes() {
				agentEventPanes[paneID] = true
			}
		}

		for _, p := range allPanes {
			if agentCommands[p.CurrentCommand] || agentEventPanes[p.ID] {
				agentPanes++
			}
		}
		waitingAgents := 0
		errorAgents := 0
		stuckAgents := 0
		for _, evt := range toolEvents {
			switch evt.Status {
			case "waiting":
				waitingAgents++
			case "error":
				errorAgents++
			case "stuck":
				stuckAgents++
			}
		}

		result := map[string]interface{}{
			"sessions": map[string]int{
				"total":    len(sessions),
				"attached": attachedSessions,
				"detached": len(sessions) - attachedSessions,
			},
			"windows":     totalWindows,
			"panes":       len(allPanes),
			"agent_panes": agentPanes,
			"agents": map[string]int{
				"active":  agentPanes,
				"waiting": waitingAgents,
				"stuck":   stuckAgents,
				"error":   errorAgents,
			},
			"processes": stats.ProcessCountsFromSessions(sessions),
			"system":    stats.SystemStats(),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})

	// Activity endpoints
	r.Get("/activity", func(w http.ResponseWriter, r *http.Request) {
		session := r.URL.Query().Get("session")
		w.Header().Set("Content-Type", "application/json")
		if session != "" {
			snap := opts.ActivityTracker.Get(session)
			// Stamp host on local snapshot in multi-host mode
			if snap != nil && opts.PeerMgr != nil && snap.Host == "" {
				snap.Host = opts.PeerMgr.LocalID()
			}
			json.NewEncoder(w).Encode(snap)
		} else {
			snapshots := opts.ActivityTracker.GetAll()
			// Stamp host on local snapshots in multi-host mode
			if opts.PeerMgr != nil {
				localID := opts.PeerMgr.LocalID()
				for _, s := range snapshots {
					if s.Host == "" {
						s.Host = localID
					}
				}
				peerActivity := opts.PeerMgr.GetAllActivity()
				snapshots = append(snapshots, peerActivity...)
			}
			json.NewEncoder(w).Encode(snapshots)
		}
	})

	// Push notification endpoints
	r.Get("/push/vapid-key", func(w http.ResponseWriter, r *http.Request) {
		if opts.PushKeys == nil {
			http.Error(w, "push notifications not configured", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"public_key": opts.PushKeys.PublicKey,
		})
	})

	r.Post("/push/subscribe", func(w http.ResponseWriter, r *http.Request) {
		if opts.PushStore == nil {
			http.Error(w, "push notifications not configured", http.StatusServiceUnavailable)
			return
		}
		var sub wp.Subscription
		if err := json.NewDecoder(r.Body).Decode(&sub); err != nil || sub.Endpoint == "" {
			http.Error(w, "invalid subscription", http.StatusBadRequest)
			return
		}
		opts.PushStore.Add(&sub)
		w.WriteHeader(http.StatusNoContent)
	})

	r.Post("/push/unsubscribe", func(w http.ResponseWriter, r *http.Request) {
		if opts.PushStore == nil {
			http.Error(w, "push notifications not configured", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Endpoint string `json:"endpoint"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Endpoint == "" {
			http.Error(w, "endpoint is required", http.StatusBadRequest)
			return
		}
		opts.PushStore.Remove(req.Endpoint)
		w.WriteHeader(http.StatusNoContent)
	})

	// Preferences endpoints
	r.Get("/preferences", func(w http.ResponseWriter, r *http.Request) {
		var prefs *preferences.Preferences
		if opts.PrefStore != nil {
			prefs = opts.PrefStore.Get()
		} else {
			prefs = preferences.Default()
		}
		// Never leak the stored API key to the browser; send a mask
		// placeholder when one is set.
		if prefs.AINaming.APIKey != "" {
			prefs.AINaming.APIKey = preferences.APIKeyMask
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(prefs)
	})

	r.Put("/preferences", func(w http.ResponseWriter, r *http.Request) {
		if opts.PrefStore == nil {
			http.Error(w, "preferences not available", http.StatusServiceUnavailable)
			return
		}
		var prefs preferences.Preferences
		if err := json.NewDecoder(r.Body).Decode(&prefs); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		// A masked API key means "keep the existing one"; restore it from
		// the store so the masked placeholder is never persisted.
		if prefs.AINaming.APIKey == preferences.APIKeyMask {
			prefs.AINaming.APIKey = opts.PrefStore.Get().AINaming.APIKey
		}
		// Defensive: never persist the mask itself, even if the store was
		// previously corrupted with it.
		if prefs.AINaming.APIKey == preferences.APIKeyMask {
			prefs.AINaming.APIKey = ""
		}
		if err := opts.PrefStore.Update(&prefs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if opts.OnPrefsChanged != nil {
			opts.OnPrefsChanged(&prefs)
		}
		// Echo a masked COPY; never mutate the stored struct (Update keeps
		// the pointer, so mutating prefs here would corrupt the store).
		echo := prefs
		if echo.AINaming.APIKey != "" {
			echo.AINaming.APIKey = preferences.APIKeyMask
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&echo)
	})

	// Wiki status and install endpoints
	r.Get("/wiki/status", func(w http.ResponseWriter, r *http.Request) {
		if opts.WikiLite == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "wiki not enabled"})
			return
		}
		st := opts.WikiLite.Status()
		st.DefaultRoot, _ = os.UserHomeDir()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(st)
	})

	r.Post("/wiki/install", func(w http.ResponseWriter, r *http.Request) {
		if opts.WikiLite == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "wiki not enabled"})
			return
		}
		// StartInstall reserves the installing state and returns at
		// once, so a 40MB fetch plus extract is never bound to this
		// request's lifetime and cannot be cancelled mid-swap.
		err := opts.WikiLite.StartInstall()
		if err != nil {
			code := http.StatusInternalServerError
			if strings.Contains(err.Error(), "not found on PATH") {
				code = http.StatusServiceUnavailable
			}
			if err.Error() == "already installing" {
				code = http.StatusConflict
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]bool{"installing": true})
	})

	// Session-attribute endpoints -- server-authoritative, mesh-wide shared
	// per-session UI bits (backgrounded / hidden). Keys are global and
	// host-qualified ("<owner-fp>/<name>"), identical to the frontend's
	// sessionKey(). No localStorage source of truth, no namespace
	// translation, no whole-blob writes.
	r.Get("/session-attrs", func(w http.ResponseWriter, r *http.Request) {
		if opts.AttrsStore == nil {
			http.Error(w, "session attrs not available", http.StatusServiceUnavailable)
			return
		}
		// Opportunistically GC sessions that are genuinely gone (owner
		// online but session absent) before returning the live sets.
		pruneSessionAttrs(opts, hub)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(opts.AttrsStore.Sets())
	})

	r.Post("/session-attrs", func(w http.ResponseWriter, r *http.Request) {
		if opts.AttrsStore == nil {
			http.Error(w, "session attrs not available", http.StatusServiceUnavailable)
			return
		}
		var body struct {
			Key        string `json:"key"`
			Background bool   `json:"background"`
			Hidden     bool   `json:"hidden"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
			http.Error(w, "key is required", http.StatusBadRequest)
			return
		}
		a, err := opts.AttrsStore.Set(body.Key, body.Background, body.Hidden)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		hub.BroadcastJSON(map[string]interface{}{
			"type": "session-attrs-updated",
			"key":  body.Key,
		})
		fanoutAttrsDeltaToPeers(opts, body.Key, a)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(opts.AttrsStore.Sets())
	})

	// Session-order endpoints -- server-authoritative, per-session rank map.
	r.Get("/session-order", func(w http.ResponseWriter, r *http.Request) {
		if opts.OrderStore == nil {
			http.Error(w, "session order not available", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(opts.OrderStore.Ranks())
	})
	r.Post("/session-order", func(w http.ResponseWriter, r *http.Request) {
		if opts.OrderStore == nil {
			http.Error(w, "session order not available", http.StatusServiceUnavailable)
			return
		}
		var body struct {
			Key  string `json:"key"`
			Rank string `json:"rank"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
			http.Error(w, "key is required", http.StatusBadRequest)
			return
		}
		order, err := opts.OrderStore.Set(body.Key, body.Rank)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if hub != nil {
			hub.BroadcastJSON(map[string]interface{}{"type": "session-order-updated", "key": body.Key})
		}
		fanoutOrderDeltaToPeers(opts, body.Key, order)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(opts.OrderStore.Ranks())
	})

	// Group endpoints -- server-authoritative, durable field-LWW records.
	r.Get("/groups", func(w http.ResponseWriter, r *http.Request) {
		if opts.GroupStore == nil {
			http.Error(w, "groups not available", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(opts.GroupStore.Live())
	})
	r.Post("/groups", func(w http.ResponseWriter, r *http.Request) {
		if opts.GroupStore == nil {
			http.Error(w, "groups not available", http.StatusServiceUnavailable)
			return
		}
		var body struct {
			ID   string          `json:"id"`
			Op   string          `json:"op"`
			Tree json.RawMessage `json:"tree,omitempty"`
			Name string          `json:"name,omitempty"`
			Mode string          `json:"mode,omitempty"`
			Rank string          `json:"rank,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" || body.Op == "" {
			http.Error(w, "id and op are required", http.StatusBadRequest)
			return
		}
		var (
			group groupsync.Group
			err   error
		)
		switch body.Op {
		case "tree":
			if len(body.Tree) == 0 {
				http.Error(w, "tree is required", http.StatusBadRequest)
				return
			}
			before, _ := opts.GroupStore.Get(body.ID)
			group, err = opts.GroupStore.SetTree(body.ID, body.Tree)
			if err == nil && coordinator != nil {
				coordinator.ObserveTreeMutation(body.ID, before, group)
			}
		case "name":
			mode := groupsync.NameModeManual
			if body.Mode != "" {
				switch body.Mode {
				case string(groupsync.NameModeAuto), string(groupsync.NameModeManual):
					mode = groupsync.NameMode(body.Mode)
				default:
					http.Error(w, "invalid mode", http.StatusBadRequest)
					return
				}
			}
			group, err = opts.GroupStore.SetName(body.ID, body.Name, mode)
		case "ai-name":
			if coordinator == nil {
				http.Error(w, "group naming unavailable", http.StatusServiceUnavailable)
				return
			}
			group, err = coordinator.Force(r.Context(), body.ID)
			if err != nil {
				code := http.StatusInternalServerError
				msg := err.Error()
				switch {
				case strings.Contains(msg, "not found"):
					code = http.StatusNotFound
				case strings.Contains(msg, "needs at least 2 members"),
					strings.Contains(msg, "is deleted"),
					strings.Contains(msg, "malformed tree"),
					strings.Contains(msg, "membership changed during naming"),
					strings.Contains(msg, "disappeared during naming"):
					code = http.StatusUnprocessableEntity
				case strings.Contains(msg, "state manager unavailable"),
					strings.Contains(msg, "generation failed"),
					strings.Contains(msg, "persist group name"):
					code = http.StatusServiceUnavailable
				}
				http.Error(w, msg, code)
				return
			}
		case "rank":
			group, err = opts.GroupStore.SetRank(body.ID, body.Rank)
		case "delete":
			group, err = opts.GroupStore.Delete(body.ID)
		default:
			http.Error(w, "invalid op", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if hub != nil {
			hub.BroadcastJSON(map[string]interface{}{"type": "groups-updated", "id": body.ID, "op": body.Op})
		}
		fanoutGroupDeltaToPeers(opts, body.ID, groupsync.Group(group))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(opts.GroupStore.Live())
	})

}

func registerWSRoutes(r chi.Router, opts *Options, hub *ws.Hub) {
	ptyHandler := ws.NewPTYTerminalHandler(opts.ActivityTracker, opts.Tracker)

	daemonWS := func(w http.ResponseWriter, req *http.Request) {
		// Route remote sessions through PTY relay
		hostID := req.URL.Query().Get("host")
		if opts.PeerMgr != nil && hostID != "" && !opts.PeerMgr.IsLocal(hostID) {
			handleRemoteSession(w, req, opts, hostID)
			return
		}
		handleDaemonSession(w, req, opts)
	}

	if opts.AuthEnabled {
		authMw := auth.Middleware(opts.SessionMgr)
		r.With(authMw).Get("/ws/events", hub.HandleEvents)
		r.With(authMw).Get("/ws/session", daemonWS)
		r.With(authMw).Get("/ws/direct-session", ptyHandler.HandleDirectSession)
		r.With(authMw).Get("/ws/daemon-session", daemonWS)
	} else {
		r.Get("/ws/events", hub.HandleEvents)
		r.Get("/ws/session", daemonWS)
		r.Get("/ws/direct-session", ptyHandler.HandleDirectSession)
		r.Get("/ws/daemon-session", daemonWS)
	}
}

func handleRemoteSession(w http.ResponseWriter, r *http.Request, opts *Options, hostID string) {
	sessionName := r.URL.Query().Get("name")
	if sessionName == "" {
		http.Error(w, "missing session name", http.StatusBadRequest)
		return
	}

	cols := uint16(80)
	rows := uint16(24)
	if c := r.URL.Query().Get("cols"); c != "" {
		if v, err := strconv.ParseUint(c, 10, 16); err == nil && v > 0 {
			cols = uint16(v)
		}
	}
	if rv := r.URL.Query().Get("rows"); rv != "" {
		if v, err := strconv.ParseUint(rv, 10, 16); err == nil && v > 0 {
			rows = uint16(v)
		}
	}

	if opts.PeerMgr == nil {
		http.Error(w, "peer not connected", http.StatusBadGateway)
		return
	}
	peerConn := opts.PeerMgr.GetPeerConnection(hostID)
	if peerConn == nil {
		http.Error(w, "peer not connected", http.StatusBadGateway)
		return
	}

	if !peerConn.HasCapability(peer.CapPerStream) {
		http.Error(w, "peer does not support per-stream terminal connections -- upgrade the peer first", http.StatusUpgradeRequired)
		return
	}

	upgrader := websocket.Upgrader{
		CheckOrigin:    ws.CheckSameOrigin,
		ReadBufferSize: 1024, WriteBufferSize: 1024 * 32,
	}
	browserWS, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer browserWS.Close()
	ok := serveViewerPerStream(browserWS, peerConn, opts, hostID, sessionName, cols, rows)
	if !ok {
		// Write a close frame so the browser knows this is a terminal failure,
		// not a normal closure. Use application-level close code 4000 + reason.
		_ = browserWS.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(4000, "per-stream setup failed"))
	}
}

func serveViewerPerStream(browserWS *websocket.Conn, peerConn *peer.PeerConnection, opts *Options, hostID, session string, cols, rows uint16) bool {
	if opts == nil || opts.PeerMgr == nil || opts.Identity == nil || opts.StreamReg == nil {
		return false
	}
	streamID := peer.GenerateStreamID()
	token := peer.NewToken()
	log := logrus.WithFields(logrus.Fields{"stream": streamID, "session": session, "host": hostID})
	openMsg, _ := peer.NewMessage(peer.MsgOpenTerminal, peer.OpenTerminalPayload{
		StreamID:     streamID,
		Session:      session,
		Cols:         cols,
		Rows:         rows,
		Token:        token,
		ViewerHostID: opts.PeerMgr.LocalID(),
	})

	dial := peerConn.Role == peer.RoleDialer
	var conn *websocket.Conn
	if dial {
		addr := opts.PeerMgr.GetPeerAddress(hostID)
		c, err := peer.DialPeerStream(context.Background(), addr, opts.Identity, token)
		if err != nil {
			log.WithError(err).Debug("viewer data-conn dial failed")
			return false
		}
		conn = c
		if !peerConn.EnqueueHi(openMsg) {
			conn.Close()
			return false
		}
	} else {
		ps := peer.NewPendingStream(streamID, session, cols, rows, hostID, opts.PeerMgr.LocalID(), hostID)
		opts.StreamReg.Register(token, ps)
		if !peerConn.EnqueueHi(openMsg) {
			return false
		}
		c, ok := ps.WaitResolved(peer.StreamSetupTimeout())
		if !ok {
			return false
		}
		conn = c
	}
	defer conn.Close()
	// Viewer writes (browser input) stay uncompressed for role clarity.
	conn.EnableWriteCompression(false)
	ws.SpliceConns(browserWS, conn, log)
	return true
}

// handleDaemonSession upgrades to WebSocket and bridges a session daemon
// (direct PTY with persistence) to the browser. Query params: name=<id>, cols=<>, rows=<>.
func handleDaemonSession(w http.ResponseWriter, r *http.Request, opts *Options) {
	if opts.DaemonReg == nil {
		http.Error(w, "daemon sessions not available", http.StatusServiceUnavailable)
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}

	cols, _ := strconv.ParseUint(r.URL.Query().Get("cols"), 10, 16)
	rows, _ := strconv.ParseUint(r.URL.Query().Get("rows"), 10, 16)
	if cols == 0 {
		cols = 120
	}
	if rows == 0 {
		rows = 40
	}

	replayGated := r.URL.Query().Get("replay") == "1"

	socketPath := opts.DaemonReg.SocketPath(name)
	sess, err := pty.NewDaemonSession(socketPath)
	if err != nil {
		http.Error(w, "daemon connect: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Resize to match browser.
	sess.Resize(uint16(cols), uint16(rows))

	upgrader := websocket.Upgrader{
		CheckOrigin:    ws.CheckSameOrigin,
		ReadBufferSize: 1024, WriteBufferSize: 1024 * 32,
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		sess.Close()
		return
	}
	defer conn.Close()

	log := logrus.WithFields(logrus.Fields{"session": name, "backend": "daemon"})
	paneID := name + ":0.0"
	var onOutput func()
	if opts.OnDaemonOutput != nil {
		onOutput = func() { opts.OnDaemonOutput(paneID) }
	}
	ws.BridgeDirectPTY(conn, sess, name, opts.ActivityTracker, log, replayGated, onOutput)

	// The bridge returned: either this tab disconnected (daemon still alive)
	// or the daemon/shell exited (Ctrl+D, kill, crash). Reconcile discovery now
	// instead of waiting up to 2s for the ticker, so a dead session disappears
	// from the sidebar and its terminal view unmounts promptly. A live daemon
	// simply stays in the list, so this is a no-op for tab disconnects.
	if opts.RefreshSessions != nil {
		opts.RefreshSessions()
	}
}

func enrichSessionsFromTracker(sessions []*model.Session, tracker *toolevents.Tracker, localHost string) {
	if tracker == nil {
		return
	}
	for _, session := range sessions {
		meta := tracker.SessionMetaFor(session.Host, session.Name)
		// Agent-derived fields (tool, prompt, last message) are only valid while
		// the agent still runs; suppress them once it exits. Checked lazily/once.
		aliveChecked := false
		aliveVal := false
		alive := func() bool {
			if !aliveChecked {
				aliveVal = shouldResurrectAgentMeta(session, localHost)
				aliveChecked = true
			}
			return aliveVal
		}
		if session.AgentType == "" && meta.Tool != "" && alive() {
			session.AgentType = string(meta.Tool)
		}
		if session.ProjectPath == "" && meta.CWD != "" {
			session.ProjectPath = meta.CWD
		}
		if session.PromptPreview == "" && meta.Message != "" {
			session.PromptPreview = meta.Message
		}
		if session.AgentSessionID == "" && meta.AgentSessionID != "" {
			session.AgentSessionID = meta.AgentSessionID
		}
		if session.UserPrompt == "" && meta.UserPrompt != "" && alive() {
			session.UserPrompt = meta.UserPrompt
		}
		if session.LastAgentMessage == "" && meta.LastAgentMessage != "" && alive() {
			session.LastAgentMessage = meta.LastAgentMessage
		}
	}
}

// shouldResurrectAgentMeta reports whether this host may repopulate a session's
// agent-derived fields from its own tool-event tracker. It is only allowed for
// LOCAL sessions whose agent process is still running: if the process is gone
// the identity is stale (agent exited, pane reverted to a shell or a command
// like a dev server) and must not be resurrected. REMOTE (peer) sessions are
// never resurrected here -- the origin host is authoritative and relays the
// correct state; this host's tracker is only a mirror and may hold a stale tool
// entry that would otherwise revive a tag the origin already cleared.
func shouldResurrectAgentMeta(session *model.Session, localHost string) bool {
	if session.Host != "" && session.Host != localHost {
		return false
	}
	for _, win := range session.Windows {
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
