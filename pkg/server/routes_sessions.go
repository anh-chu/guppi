package server

import (
	"context"
	"encoding/json"
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
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/preferences"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/state"
	"github.com/anh-chu/termyard/pkg/stats"
	"github.com/anh-chu/termyard/pkg/toolevents"
	"github.com/anh-chu/termyard/pkg/ws"
)

// registerSessionsRoutes mounts the protected session/activity/stats/sync
// endpoints under /api. Callers must apply auth middleware separately.
func registerSessionsRoutes(r chi.Router, opts *Options, hub *ws.Hub) {

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

	// /api/hosts was deleted -- hosts are now streamed via canonical bootstrap/state stream

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

		// Remote peer -- request capture over the control link. host may be a
		// v2 OwnerID (from a v2-routed pane's SessionRef.Owner) or a legacy
		// peer fingerprint; ResolveHostParam accepts either (see its doc).
		resolvedPeerID, hostIsLocal := "", true
		if opts.PeerMgr != nil {
			resolvedPeerID, hostIsLocal = opts.PeerMgr.ResolveHostParam(host)
		}
		if host != "" && opts.PeerMgr != nil && !hostIsLocal {
			if opts.CaptureReg == nil {
				http.Error(w, "capture unavailable", http.StatusInternalServerError)
				return
			}
			peerConn := opts.PeerMgr.GetPeerConnection(resolvedPeerID)
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

	// Crashed sessions recovery endpoints
	r.Get("/crashed-sessions", func(w http.ResponseWriter, r *http.Request) {
		var out []map[string]string
		for _, rec := range opts.Catalog.Sessions() {
			if rec.Phase != state.SessionPhaseCrashed {
				continue
			}
			name := rec.Name
			if name == "" {
				name = string(rec.ID)
			}
			out = append(out, map[string]string{
				"id":         name,
				"shell":      rec.Shell,
				"cwd":        rec.Cwd,
				"generation": rec.Generation,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	r.Post("/crashed-sessions/{id}/recover", func(w http.ResponseWriter, r *http.Request) {
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

		ref, ok := opts.CommandSvc.LookupRefByDisplayName(id)
		if !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		params, _ := json.Marshal(state.RecoverParams{Shell: body.Shell, Cwd: body.Cwd})
		if _, err := opts.CommandSvc.ExecuteSessionCommand(r.Context(), state.SessionCommand{
			ID:     state.NewCommandID(),
			Ref:    ref,
			Action: state.ActionRecover,
			Params: params,
		}); err != nil {
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
		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}

		ref, ok := opts.CommandSvc.LookupRefByDisplayName(id)
		if !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		if _, err := opts.CommandSvc.ExecuteSessionCommand(r.Context(), state.SessionCommand{
			ID:     state.NewCommandID(),
			Ref:    ref,
			Action: state.ActionDismiss,
		}); err != nil {
			http.Error(w, "dismiss failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	r.Delete("/crashed-sessions", func(w http.ResponseWriter, r *http.Request) {
		for _, rec := range opts.Catalog.Sessions() {
			if rec.Phase != state.SessionPhaseCrashed {
				continue
			}
			if _, err := opts.CommandSvc.ExecuteSessionCommand(r.Context(), state.SessionCommand{
				ID:     state.NewCommandID(),
				Ref:    rec.Ref,
				Action: state.ActionDismiss,
			}); err != nil {
				http.Error(w, "dismiss all failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
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
				// Stamp durable session identity (v2) when available
				if opts.CommandSvc != nil {
					if ref, ok := opts.CommandSvc.LookupRefByDisplayName(info.Session); ok {
						evt.SessionID = string(ref.Session)
					}
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
			Host      string `json:"host,omitempty"`
			Session   string `json:"session"`
			SessionID string `json:"session_id,omitempty"`
		}
		turns := opts.Tracker.ActiveTurns()
		out := make([]turn, 0, len(turns))
		for _, evt := range turns {
			out = append(out, turn{Host: evt.Host, Session: evt.Session, SessionID: evt.SessionID})
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

	// Stats endpoint -- system and process information
	r.Get("/stats", func(w http.ResponseWriter, r *http.Request) {
		result := map[string]interface{}{
			"processes": []interface{}{},
			"system":    stats.SystemStats(),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
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

	// Legacy session-attrs/session-order/groups sync routes do not exist in
	// the canonical architecture: there is no backing store to register them
	// against, and no reachable client calls them anymore.
}

func registerWSRoutes(r chi.Router, opts *Options, hub *ws.Hub) {
	ptyHandler := ws.NewPTYTerminalHandler(opts.ActivityTracker, opts.Tracker)

	daemonWS := func(w http.ResponseWriter, req *http.Request) {
		// Route remote sessions through PTY relay. `host` may be an OwnerID
		// (what a pane's SessionRef.Owner / terminalPool.ts identity actually
		// carries) or a peer transport fingerprint; ResolveHostParam accepts
		// either and returns the live peer connection's fingerprint to use
		// for routing, never conflating the two identity domains by
		// comparing the raw value against fingerprints unconditionally.
		hostID := req.URL.Query().Get("host")
		if opts.PeerMgr != nil && hostID != "" {
			resolvedPeerID, isLocal := opts.PeerMgr.ResolveHostParam(hostID)
			if !isLocal {
				handleRemoteSession(w, req, opts, resolvedPeerID)
				return
			}
		}
		handleDaemonSession(w, req, opts)
	}

	if opts.AuthEnabled {
		authMw := auth.Middleware(opts.SessionMgr)
		r.With(authMw).Get("/ws/events", hub.HandleEvents)
		r.With(authMw).Get("/ws/session", daemonWS)
		r.With(authMw).Get("/ws/direct-session", ptyHandler.HandleDirectSession)
		r.With(authMw).Get("/ws/daemon-session", daemonWS)
		if opts.StateStream != nil {
			r.With(authMw).Get("/ws/state", opts.StateStream.HandleState)
		}
	} else {
		r.Get("/ws/events", hub.HandleEvents)
		r.Get("/ws/session", daemonWS)
		r.Get("/ws/direct-session", ptyHandler.HandleDirectSession)
		r.Get("/ws/daemon-session", daemonWS)
		if opts.StateStream != nil {
			r.Get("/ws/state", opts.StateStream.HandleState)
		}
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
	// Forward the stable identity (immutable SessionID + generation) when the
	// browser supplied it. The receiving peer prefers the SessionID as its
	// daemon socket key and only falls back to the plain `name` when no
	// SessionID was carried.
	sessionID := r.URL.Query().Get("sessionID")
	generation := r.URL.Query().Get("generation")
	ok := serveViewerPerStream(browserWS, peerConn, opts, hostID, sessionName, sessionID, generation, cols, rows)
	if !ok {
		// Write a close frame so the browser knows this is a terminal failure,
		// not a normal closure. Use application-level close code 4000 + reason.
		_ = browserWS.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(4000, "per-stream setup failed"))
	}
}

func serveViewerPerStream(browserWS *websocket.Conn, peerConn *peer.PeerConnection, opts *Options, hostID, session, sessionID, generation string, cols, rows uint16) bool {
	if opts == nil || opts.PeerMgr == nil || opts.Identity == nil || opts.StreamReg == nil {
		return false
	}
	streamID := peer.GenerateStreamID()
	token := peer.NewToken()
	log := logrus.WithFields(logrus.Fields{"stream": streamID, "session": session, "host": hostID})
	openMsg, _ := peer.NewMessage(peer.MsgOpenTerminal, peer.OpenTerminalPayload{
		StreamID:     streamID,
		Session:      session,
		SessionID:    sessionID,
		Generation:   generation,
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
//
// Stable re-keying: when `sessionID` (immutable SessionID) and `generation`
// are supplied, the attach is routed by the immutable SessionID and validated
// against the current generation (from the v2 catalog, when present) BEFORE
// any PTY bytes stream. A mismatched or superseded generation returns a typed
// JSON error so an open terminal can never attach to a stale daemon generation.
func handleDaemonSession(w http.ResponseWriter, r *http.Request, opts *Options) {
	if opts.DaemonReg == nil {
		http.Error(w, "daemon sessions not available", http.StatusServiceUnavailable)
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

	// v2 stable-attach path: key the daemon lookup by immutable SessionID and
	// gate on generation. Legacy clients keep routing by `name`.
	if idParam := r.URL.Query().Get("sessionID"); idParam != "" || r.URL.Query().Get("generation") != "" {
		handleDaemonSessionStable(w, r, opts, idParam, uint16(cols), uint16(rows), replayGated)
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}

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

// daemonAttachCode is a stable, typed reject reason for generation-gated
// daemon attach. It mirrors the v2 wire error vocabulary where possible.
type daemonAttachCode string

const (
	daemonAttachNotFound          daemonAttachCode = "not_found"
	daemonAttachGenerationChanged daemonAttachCode = "generation_changed"
	daemonAttachNotReady          daemonAttachCode = "not_ready"
)

// handleDaemonSessionStable validates a stable-identity attach request against
// the current daemon generation and, when it matches, bridges the daemon PTY
// exactly like the legacy name-keyed path. The immutable SessionID is the
// daemon key (DaemonKey defaults to SessionID), so rename never changes the
// route; only a generation change (recovery/restart) or a missing/starting
// session can reject the attach.
func handleDaemonSessionStable(w http.ResponseWriter, r *http.Request, opts *Options, sessionID string, cols, rows uint16, replayGated bool) {
	sid := state.SessionID(sessionID)
	if !validSessionIDQuery(sid) {
		writeDaemonAttachError(w, daemonAttachNotFound, http.StatusBadRequest, "missing or invalid sessionID")
		return
	}

	requestedGen := r.URL.Query().Get("generation")

	// Resolve the current generation. Prefer the v2 catalog when it is
	// configured (it is authoritative for created sessions); otherwise ask the
	// daemon registry via its lifecycle record.
	currentGen := ""
	phaseOK := true
	if opts.Catalog != nil {
		if rec, ok := opts.Catalog.Session(sid); ok {
			currentGen = rec.Generation
			phaseOK = rec.Phase == state.SessionPhaseActive || rec.Phase == state.SessionPhaseStarting
		}
	}
	if currentGen == "" && opts.DaemonReg != nil {
		currentGen = opts.DaemonReg.GenerationFor(string(sid))
	}

	// A requested generation that differs from the live one is a stale attach:
	// the browser must reconnect with the current generation before any bytes
	// stream. This is what lets recovery change generation and re-key the open
	// terminal instead of silently splicing onto an old daemon.
	if requestedGen != "" && currentGen != "" && requestedGen != currentGen {
		writeDaemonAttachError(w, daemonAttachGenerationChanged, http.StatusConflict, "session generation changed; reconnect with current generation")
		return
	}

	// Session record exists but is not in an attachable phase (crashed/
	// stopping/cleanly ended/dismissed). Reject unconditionally: these phases
	// commonly retain a stale generation from before the session died, so
	// gating this reject on an empty currentGen let dead sessions with a
	// recorded generation fall through to socket attach. "starting" is
	// already treated as phaseOK above, so it is unaffected here.
	if !phaseOK {
		writeDaemonAttachError(w, daemonAttachNotReady, http.StatusServiceUnavailable, "session is not ready for attach")
		return
	}

	socketPath := opts.DaemonReg.SocketPath(string(sid))
	sess, err := pty.NewDaemonSession(socketPath)
	if err != nil {
		http.Error(w, "daemon connect: "+err.Error(), http.StatusInternalServerError)
		return
	}

	sess.Resize(cols, rows)

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

	log := logrus.WithFields(logrus.Fields{"session_id": string(sid), "generation": currentGen, "backend": "daemon"})
	paneID := string(sid) + ":0.0"
	var onOutput func()
	if opts.OnDaemonOutput != nil {
		onOutput = func() { opts.OnDaemonOutput(paneID) }
	}
	ws.BridgeDirectPTY(conn, sess, string(sid), opts.ActivityTracker, log, replayGated, onOutput)

	if opts.RefreshSessions != nil {
		opts.RefreshSessions()
	}
}

// validSessionIDQuery reports whether sid is a canonical SessionID (lowercase
// base32 per pkg/state/ids.go). Anything else -- including path separators,
// ".." sequences, control characters, or over-long values -- is rejected
// before it can be used as a daemon socket key.
func validSessionIDQuery(sid state.SessionID) bool {
	return sid.Validate() == nil
}

func writeDaemonAttachError(w http.ResponseWriter, code daemonAttachCode, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": string(code), "message": msg})
}

