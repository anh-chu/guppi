package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/groupsync"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/namer"
	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/portforward"
	"github.com/anh-chu/termyard/pkg/preferences"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/recovery"
	"github.com/anh-chu/termyard/pkg/scheduler"
	"github.com/anh-chu/termyard/pkg/server"
	"github.com/anh-chu/termyard/pkg/sessionattrs"
	"github.com/anh-chu/termyard/pkg/sessionlaunch"
	"github.com/anh-chu/termyard/pkg/sessionorder"
	"github.com/anh-chu/termyard/pkg/state"
	"github.com/anh-chu/termyard/pkg/toolevents"
	"github.com/anh-chu/termyard/pkg/webpush"
	"github.com/anh-chu/termyard/pkg/wikilite"
	"github.com/anh-chu/termyard/pkg/ws"
)

// trackedSession holds a live daemon instance and its watcher stop func.
type trackedSession struct {
	inst pty.Instance
	stop func() // stop-and-wait for watcher goroutine
}

// Runtime assembles the server's long-lived dependencies and exposes explicit
// Start/Ready/Stop lifecycle control. It owns the goroutines that used to be
// launched inline in Execute, so startup order, readiness, and cancellation are
// all visible in one place.
type Runtime struct {
	ctx    context.Context
	cancel context.CancelFunc

	ready chan struct{}
	opts  *server.Options

	// Core stores / registries
	stateMgr   *state.Manager
	tracker    *toolevents.Tracker
	actTracker *activity.Tracker
	daemonReg  *pty.Registry
	adapter    *daemonAdapter

	// Session authority: in-memory map is the single authority for local membership.
	sessionsMu sync.Mutex
	tracked    map[string]trackedSession // indexed by session name

	// Tool-event monitors
	reconciler     *toolevents.Reconciler
	detector       *toolevents.Detector
	silenceMonitor *toolevents.SilenceMonitor

	// Identity / peers
	peerMgr    *peer.Manager
	supervisor *peer.LinkSupervisor
	identity   *identity.Identity

	// Optional subsystems
	wikiSup         *wikilite.Supervisor
	schedulerRunner *scheduler.Runner
	pushSender      *webpush.Sender
	sessionMgr      *auth.SessionManager

	// Helpers
	fgProvider ForegroundProvider
	hub        *ws.Hub
}

// newRuntime builds the dependency graph without starting any monitors. All
// construction-time errors are returned synchronously.
func newRuntime(c *cli.Command) (*Runtime, error) {
	rt := &Runtime{
		stateMgr:   state.NewManager(),
		tracker:    toolevents.NewTracker(),
		actTracker: activity.NewTracker(),
		ready:      make(chan struct{}),
		fgProvider: newForegroundProvider(),
		tracked:    make(map[string]trackedSession),
	}
	rt.tracker.EnablePersistence()

	rt.daemonReg = pty.NewRegistry(defaultSessionDir())

	rt.adapter = &daemonAdapter{reg: rt.daemonReg}
	rt.stateMgr.SetDaemonRegistry(rt.adapter)

	rt.reconciler = toolevents.NewReconciler(rt.tracker, rt.lookupPane, 3*time.Second)
	rt.detector = toolevents.NewDetector(rt.tracker, rt.listPanes, 5*time.Second)
	rt.silenceMonitor = toolevents.NewSilenceMonitor(rt.tracker, rt.detector, rt.adapter)

	attrsStore, err := sessionattrs.NewStore()
	if err != nil {
		logrus.WithError(err).Warn("failed to load session-attrs store, sync disabled")
		attrsStore = nil
	}

	orderStore, err := sessionorder.NewStore()
	if err != nil {
		logrus.WithError(err).Warn("failed to load session-order store, sync disabled")
		orderStore = nil
	}

	groupStore, err := groupsync.NewStore()
	if err != nil {
		logrus.WithError(err).Warn("failed to load groups store, sync disabled")
		groupStore = nil
	}

	prefStore, err := preferences.NewStore()
	if err != nil {
		logrus.WithError(err).Warn("failed to load preferences, using defaults")
		prefStore = nil
	}

	applyNamerFromPrefs := func(p *preferences.Preferences) {
		cfg := namer.Configure(p.AINaming.Enabled, p.AINaming.Endpoint, p.AINaming.APIKey, p.AINaming.Model)
		n := namer.New(cfg)
		rt.stateMgr.SetNamer(n)
		if n.Enabled() {
			logrus.Info("AI session namer enabled")
		} else {
			logrus.Debug("AI session namer disabled")
		}
	}
	if prefStore != nil {
		applyNamerFromPrefs(prefStore.Get())
	}

	schedulerStore, err := scheduler.NewStore()
	if err != nil {
		logrus.WithError(err).Warn("failed to load scheduler store, schedules disabled")
		schedulerStore = nil
	}

	var (
		authEnabled   bool
		passwordStore *auth.PasswordStore
		authLimiter   *auth.Limiter
		notifyToken   string
	)
	if !c.Bool("no-auth") {
		var err error
		passwordStore, err = auth.NewPasswordStore()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize auth: %w", err)
		}
		rt.sessionMgr = auth.NewSessionManager(24 * time.Hour)
		authEnabled = true
		authLimiter = auth.NewLimiter()

		notifyToken, err = auth.LoadOrCreateNotifyToken()
		if err != nil {
			logrus.WithError(err).Warn("failed to load notify token; TCP notify fallback unavailable")
		}

		if !passwordStore.HasPassword() {
			logrus.Info("no password set -- open the dashboard in your browser to complete setup")
		}
	}

	hostname, _ := os.Hostname()
	nodeIdentity, err := identity.LoadOrCreate(hostname)
	if err != nil {
		return nil, fmt.Errorf("failed to load identity: %w", err)
	}
	logrus.WithField("name", nodeIdentity.Name).WithField("fingerprint", nodeIdentity.Fingerprint()).Info("node identity loaded")
	rt.identity = nodeIdentity

	peerStore, err := identity.NewPeerStore()
	if err != nil {
		return nil, fmt.Errorf("failed to load peer store: %w", err)
	}

	rt.peerMgr = peer.NewManager(nodeIdentity, peerStore, rt.stateMgr)
	rt.detector.SetHost(rt.peerMgr.LocalID(), rt.peerMgr.LocalName())
	rt.silenceMonitor.SetHost(rt.peerMgr.LocalID(), rt.peerMgr.LocalName())
	rt.reconciler.SetHost(rt.peerMgr.LocalID(), rt.peerMgr.LocalName())

	launchSvc := &sessionlaunch.Service{
		DaemonReg: rt.daemonReg,
		StateMgr:  rt.stateMgr,
		Attrs: sessionlaunch.AttrStoreFunc(func(key, scheduleID string) (sessionlaunch.ScheduleAttr, error) {
			if attrsStore == nil {
				return sessionlaunch.ScheduleAttr{}, fmt.Errorf("session attrs store not available")
			}
			attr, err := attrsStore.SetScheduleID(key, scheduleID)
			if err != nil {
				return sessionlaunch.ScheduleAttr{}, err
			}
			return sessionlaunch.ScheduleAttr{
				Background: attr.Background,
				Hidden:     attr.Hidden,
				ScheduleID: attr.ScheduleID,
				UpdatedAt:  attr.UpdatedAt,
			}, nil
		}),
		Identity:   nodeIdentity,
		OnLaunched: rt.onLaunched,
		Remote: func(ctx context.Context, req sessionlaunch.Request) (sessionlaunch.Result, error) {
			peerConn := rt.peerMgr.GetPeerConnection(req.Host)
			if peerConn == nil {
				return sessionlaunch.Result{}, sessionlaunch.ErrPeerUnavailable
			}
			params, _ := json.Marshal(map[string]string{
				"name":            req.Name,
				"path":            req.Path,
				"command":         req.Command,
				"worktree_branch": req.WorktreeBranch,
				"schedule_id":     req.ScheduleID,
			})
			msg, _ := peer.NewMessage(peer.MsgSessionAction, peer.SessionActionPayload{
				Action: "new",
				Params: params,
			})
			if !peerConn.Enqueue(msg) {
				return sessionlaunch.Result{}, sessionlaunch.ErrPeerQueueFull
			}
			return sessionlaunch.Result{Name: req.Name, Host: req.Host}, nil
		},
		Fanout: func(key string, attr sessionlaunch.ScheduleAttr) {
			if rt.peerMgr == nil || nodeIdentity == nil {
				return
			}
			msg, err := peer.NewMessage(peer.MsgSessionAttrsDelta, peer.SessionAttrsDeltaPayload{
				Origin: nodeIdentity.Fingerprint(),
				Key:    key,
				Attr: peer.SessionAttr{
					Background: attr.Background,
					Hidden:     attr.Hidden,
					ScheduleID: attr.ScheduleID,
					UpdatedAt:  attr.UpdatedAt,
				},
			})
			if err != nil {
				return
			}
			for _, pc := range rt.peerMgr.ConnectedPeers() {
				pc.Enqueue(msg)
			}
		},
		Names: func(host string) []string {
			if host != "" && rt.peerMgr != nil && !rt.peerMgr.IsLocal(host) {
				sessions := rt.peerMgr.GetAllSessions()
				names := make([]string, 0, len(sessions))
				for _, s := range sessions {
					if s != nil && s.Host == host {
						names = append(names, s.Name)
					}
				}
				return names
			}
			if rt.stateMgr != nil {
				sessions := rt.stateMgr.GetSessions()
				names := make([]string, 0, len(sessions))
				for _, s := range sessions {
					if s != nil {
						names = append(names, s.Name)
					}
				}
				return names
			}
			return nil
		},
		Forget: recovery.ForgetSession,
	}

	streamReg := peer.NewStreamRegistry()
	captureReg := peer.NewCaptureRegistry()
	fileReadReg := peer.NewFileReadRegistry()

	deps := peer.SessionDeps{
		Manager:     rt.peerMgr,
		LocalMgr:    rt.stateMgr,
		Identity:    nodeIdentity,
		ActTracker:  rt.actTracker,
		ToolTracker: rt.tracker,
		PeerStore:   peerStore,
		DaemonReg:   rt.adapter,
		Launch:      launchSvc,
		StreamReg:   streamReg,
		CaptureReg:  captureReg,
		FileReadReg: fileReadReg,
	}

	peerHandler := peer.NewHandler(deps, streamReg)

	rt.supervisor = peer.NewLinkSupervisor(deps)

	rt.wikiSup = wikilite.NewSupervisor()

	rt.hub = ws.NewHub(rt.stateMgr, rt.tracker)
	rt.hub.SetActivityTracker(rt.actTracker, rt.peerMgr, rt.peerMgr.LocalID(), false)

	var (
		pushKeys  *webpush.VAPIDKeys
		pushStore *webpush.Store
	)
	vapidKeys, err := webpush.LoadOrCreateKeys()
	if err != nil {
		logrus.WithError(err).Warn("failed to load VAPID keys, push notifications will be unavailable")
	} else {
		pushKeys = vapidKeys
		pushStore = webpush.NewStore()
		rt.pushSender = webpush.NewSender(pushKeys, pushStore, rt.tracker)
	}

	rt.opts = &server.Options{
		Port:             int(c.Int("port")),
		SocketPath:       c.String("socket"),
		TLSCert:          c.String("tls-cert"),
		TLSKey:           c.String("tls-key"),
		TLSAuto:          c.Bool("tls"),
		StateMgr:         rt.stateMgr,
		Tracker:          rt.tracker,
		ActivityTracker:  rt.actTracker,
		PushKeys:         pushKeys,
		PushStore:        pushStore,
		PrefStore:        prefStore,
		AttrsStore:       attrsStore,
		OrderStore:       orderStore,
		GroupStore:       groupStore,
		AuthEnabled:      authEnabled,
		DebugPprof:       c.Bool("debug-pprof"),
		PasswordStore:    passwordStore,
		SessionMgr:       rt.sessionMgr,
		AuthLimiter:      authLimiter,
		NotifyToken:      notifyToken,
		Identity:         nodeIdentity,
		PeerStore:        peerStore,
		PeerMgr:          rt.peerMgr,
		PeerHandler:      peerHandler,
		StreamReg:        streamReg,
		CaptureReg:       captureReg,
		FileReadReg:      fileReadReg,
		LinkSupervisor:   rt.supervisor,
		Detector:         rt.detector,
		PortForwardStore: portforward.NewStore(),
		SchedulerStore:   schedulerStore,
		WikiLite:         rt.wikiSup,
		DaemonReg:        rt.adapter,
		Launch:           launchSvc,
		CWDResolver:      rt.adapter,
		OnDaemonOutput: func(paneID string) {
			rt.silenceMonitor.RecordOutput(paneID)
		},
		OnPrefsChanged: applyNamerFromPrefs,
		Hub:            rt.hub,
	}
	launchSvc.Hub = rt.hub

	if schedulerStore != nil {
		rt.schedulerRunner = scheduler.NewRunner(schedulerStore, rt.stateMgr, rt.peerMgr, func(req scheduler.CreateSessionReq) error {
			_, err := launchSvc.Create(rt.ctx, sessionlaunch.Request{
				Name:           req.Name,
				Host:           req.Host,
				Path:           req.Path,
				Command:        req.Command,
				AgentType:      req.AgentType,
				WorktreeBranch: req.WorktreeBranch,
				ScheduleID:     req.ScheduleID,
			})
			if err != nil {
				return err
			}
			return nil
		}, logrus.WithField("component", "scheduler"))
		rt.schedulerRunner.SetCapEnforcer(func(job scheduler.Job) {
			server.EnforceScheduleCap(rt.opts, job.ID, job.MaxConcurrency-1)
		})
		rt.opts.SchedulerRunner = rt.schedulerRunner
	}

	return rt, nil
}

// Start launches all background monitors. It returns immediately; callers wait
// on Ready() before treating the runtime as up.
func (rt *Runtime) Start(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	rt.ctx, rt.cancel = context.WithCancel(parent)

	// Note: the WebSocket hub is constructed above but intentionally not
	// started here. server.Run owns starting hub.Run after Ready() so that
	// there is a single hub goroutine and a single path to cancel it.
	go rt.peerMgr.Run(rt.ctx)
	rt.supervisor.Start(rt.ctx)
	rt.wikiSup.Start(rt.ctx)

	if rt.pushSender != nil {
		go rt.pushSender.Run(rt.ctx)
	}

	if rt.sessionMgr != nil {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-rt.ctx.Done():
					return
				case <-ticker.C:
					rt.sessionMgr.Cleanup()
				}
			}
		}()
	}

	go rt.tracker.RunInactivityPromoter(rt.ctx, toolevents.DefaultInactivityTimeout)
	go rt.tracker.RunStuckMonitor(rt.ctx, toolevents.DefaultStuckTimeout, rt.checkPrompt)

	go rt.reconciler.Run(rt.ctx)
	go rt.detector.Run(rt.ctx)
	go rt.silenceMonitor.Run(rt.ctx)
	go runShellNameWatcher(rt.ctx, rt.stateMgr, rt.adapter, rt.fgProvider)

	// Enrichment tick: update session fields without changing membership
	go rt.runEnrichmentTick(rt.ctx)

	if rt.schedulerRunner != nil {
		go rt.schedulerRunner.Run(rt.ctx)
	}

	// Atomic boot adoption: acquire flock, clean lifecycle records, adopt all
	// live daemons, and start watchers synchronously before Ready() closes.
	rt.adoptSessionsAtBoot()

	close(rt.ready)
	return nil
}

// Ready returns a channel that is closed once Start has finished launching the
// runtime's background goroutines. The HTTP server can safely begin using opts.
func (rt *Runtime) Ready() <-chan struct{} {
	return rt.ready
}

// Stop cancels the runtime context and synchronously stops all watchers.
func (rt *Runtime) Stop() {
	// Detach and stop all watchers synchronously before canceling context.
	// This ensures callback suppression covers in-flight callbacks.
	rt.sessionsMu.Lock()
	toStop := make([]func(), 0, len(rt.tracked))
	for name := range rt.tracked {
		if stop := rt.tracked[name].stop; stop != nil {
			toStop = append(toStop, stop)
		}
	}
	rt.tracked = make(map[string]trackedSession)
	rt.sessionsMu.Unlock()

	// Call stop() for each watcher outside mutex (each waits for goroutine).
	for _, stop := range toStop {
		stop()
	}

	// Cancel context to shut down other monitors.
	if rt.cancel != nil {
		rt.cancel()
	}
}

// Options returns the assembled server options after newRuntime has succeeded.
func (rt *Runtime) Options() *server.Options {
	return rt.opts
}

// adoptSessionsAtBoot performs atomic boot adoption: acquire flock on the socket
// directory, clean lifecycle records, adopt all live daemons, and start watchers
// synchronously. The lock is held until adoption completes, preventing CLI direct-spawn
// races. This runs before Ready() closes, ensuring the first API response contains
// all adopted sessions.
func (rt *Runtime) adoptSessionsAtBoot() {
	// Acquire exclusive lock on socket directory for the duration of adoption.
	// CLI direct-spawn fallback checks this lock; if held, it waits/retries API path.
	release, err := rt.daemonReg.AcquireAdoptionLock()
	if err != nil {
		logrus.WithError(err).Warn("failed to acquire adoption lock (continuing anyway)")
		release = func() {}
	} else {
		defer release()
	}

	// One-time cleanup of lifecycle records left by pre-watch versions (R11).
	pty.CleanupLegacyStateDir()

	// Adopt all live daemons synchronously.
	// Adopt() performs PID-dead stale-file cleanup as specified in the design.
	for _, info := range rt.daemonReg.Adopt() {
		rt.onLaunched(info)
	}
}

// onLaunched handles a newly launched or adopted session.
// Called under no lock; acquires sessionsMu to manage tracked map and watchers.
// Performs atomic swap: delete old, call Watch, store new entry all under sessionsMu.
func (rt *Runtime) onLaunched(info pty.SessionInfo) {
	// Build model.Session from info (before lock to avoid holding lock during enrichment).
	var created time.Time
	if t, err := time.Parse(time.RFC3339, info.Created); err == nil {
		created = t
	}

	// Determine shell PID: use ShellPid if available, else fall back to daemon PID
	shellPid := info.ShellPid
	if shellPid == 0 {
		shellPid = info.Pid
	}

	// Determine shell cwd: try to read from /proc, else use info.Cwd
	shellCwd := info.Cwd
	if shellPid > 0 {
		if liveCwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", shellPid)); err == nil && liveCwd != "" {
			shellCwd = liveCwd
		}
	}

	session := &model.Session{
		Name:        info.Name,
		Created:     created,
		Backend:     "daemon",
		ProjectPath: shellCwd,
		Windows: []*model.Window{{
			ID:     "daemon-" + info.Name,
			Name:   "shell",
			Active: true,
			Panes: []*model.Pane{{
				ID:             model.SessionRef{Session: info.Name, Window: 0, Pane: 0}.PaneID(),
				Active:         true,
				CurrentPath:    shellCwd,
				CurrentCommand: info.Shell,
				PID:            shellPid,
			}},
		}},
	}

	// Atomic operation under sessionsMu: detach old watcher, start new watcher,
	// install the tracked entry, and add the session to state. Performing the
	// state mutation inside the same critical section guarantees a stale onExit
	// (which also takes sessionsMu before removing state) can never remove a
	// replacement session. The state manager only takes its own internal lock
	// and broadcasts; it never calls back into the runtime, so the sessionsMu >
	// state.mu lock order is acyclic.
	inst := info.Instance
	var oldStop func()
	rt.sessionsMu.Lock()

	// If duplicate name exists, capture old watcher for cleanup outside lock.
	if old, exists := rt.tracked[info.Name]; exists {
		oldStop = old.stop
	}

	// Start new watcher for this instance while holding lock (atomic with map
	// update). Watch returns promptly; dialing happens in its goroutine.
	stop, err := rt.daemonReg.Watch(inst, rt.onExit, rt.onUnreachable)
	if err != nil {
		logrus.WithError(err).WithField("session", info.Name).Warn("failed to start watcher")
		rt.sessionsMu.Unlock()
		// Stop old watcher if it exists (watch() failed, so can't track new one).
		if oldStop != nil {
			oldStop()
		}
		return
	}

	// Install new entry and add to state (all under lock).
	rt.tracked[info.Name] = trackedSession{inst: inst, stop: stop}
	rt.stateMgr.AddSession(session)
	rt.sessionsMu.Unlock()

	// Stop old watcher after unlock (outside critical section).
	if oldStop != nil {
		oldStop()
	}

	// Apply full enrichment (metadata, preview, worktree detection, stale-agent
	// cleanup). Field-only; safe outside sessionsMu.
	rt.stateMgr.EnrichSessionInPlaceWithMetaCallback(info.Name, &info)

	// Update adapter snapshot for consumers.
	snapshot := rt.buildAdapterSnapshot()
	rt.adapter.refresh(snapshot)
}

// onExit handles daemon exit (PID confirmed dead).
// Called by watcher; instance-matched removal only.
// Uses generation guard to prevent removal of replacement sessions added before RemoveSession is called.
func (rt *Runtime) onExit(inst pty.Instance) {
	rt.sessionsMu.Lock()
	tracked, exists := rt.tracked[inst.Name]
	if !exists || !rt.instanceMatch(tracked.inst, inst) {
		// Instance mismatch or already removed; skip.
		rt.sessionsMu.Unlock()
		return
	}
	// Delete from map and remove from state in one critical section: a
	// replacement launch (which installs its entry and AddSession under the
	// same mutex) can never interleave between the decision and the removal.
	delete(rt.tracked, inst.Name)
	rt.stateMgr.RemoveSession(inst.Name)
	rt.sessionsMu.Unlock()

	// Best-effort cleanup after removal: stop systemd scope and remove stale
	// files (identity re-verified inside removeStaleFiles).
	if inst.SystemdUnit != "" {
		rt.daemonReg.StopSystemdUnit(inst.SystemdUnit)
	}
	rt.removeStaleFiles(inst)

	// Update adapter snapshot for consumers.
	snapshot := rt.buildAdapterSnapshot()
	rt.adapter.refresh(snapshot)
}

// onUnreachable handles transient connection loss (PID alive).
// Called by watcher with bad=true (connection lost) or false (recovered).
func (rt *Runtime) onUnreachable(inst pty.Instance, bad bool) {
	rt.sessionsMu.Lock()
	tracked, exists := rt.tracked[inst.Name]
	if !exists || !rt.instanceMatch(tracked.inst, inst) {
		// Instance mismatch; skip.
		rt.sessionsMu.Unlock()
		return
	}
	rt.sessionsMu.Unlock()

	// Mark session unreachable in state.
	rt.stateMgr.Unreachable(inst.Name, bad)
}

// instanceMatch checks if two instances match by identity.
func (rt *Runtime) instanceMatch(a, b pty.Instance) bool {
	if a.Pid != b.Pid {
		return false
	}
	if a.Nonce != "" || b.Nonce != "" {
		return a.Nonce == b.Nonce
	}
	return a.ProcStartTime == b.ProcStartTime
}

// removeStaleFiles deletes .sock and .json files after re-verifying sidecar identity.
// Per design.md: stale-file removal must verify nonce/start-time matches the exiting
// instance before removing .sock/.json, to prevent a stale callback from deleting
// replacement files when a new instance takes the same name.
func (rt *Runtime) removeStaleFiles(inst pty.Instance) {
	sockPath := rt.daemonReg.SocketPath(inst.Name)
	jsonPath := strings.TrimSuffix(sockPath, ".sock") + ".json"

	// Re-read sidecar to verify it still matches the exiting instance.
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		// File already gone or unreadable; nothing to clean.
		return
	}

	var meta struct {
		Pid           int    `json:"pid"`
		Nonce         string `json:"nonce"`
		ProcStartTime int64  `json:"procStartTime"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		// Can't parse sidecar; skip deletion (unsafe).
		return
	}

	// Verify identity matches the exiting instance.
	if meta.Pid != inst.Pid {
		// PID changed; don't delete (new daemon).
		return
	}

	if inst.Nonce != "" {
		// Nonce-based identity: must match exactly.
		if meta.Nonce != inst.Nonce {
			// Nonce changed; don't delete (new daemon).
			return
		}
	} else if inst.ProcStartTime > 0 {
		// Legacy: verify /proc start time matches.
		if meta.ProcStartTime > 0 && meta.ProcStartTime != inst.ProcStartTime {
			// Start time changed; don't delete (new daemon).
			return
		}
	}

	// Identity matches; safe to delete.
	_ = os.Remove(sockPath)
	_ = os.Remove(jsonPath)
}

// runEnrichmentTick updates session fields (cwd, preview, worktree) every 2s
// without changing membership. It reads current daemon info and enriches each
// session with live data, then updates the adapter snapshot for consumers.
func (rt *Runtime) runEnrichmentTick(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Enrich sessions from the last snapshot; missing sessions no-op
			// inside UpdateSessionFields.
			for _, info := range rt.adapter.List() {
				rt.stateMgr.EnrichSessionInPlaceWithMetaCallback(info.ID, &info)
			}

			rt.adapter.refresh(rt.buildAdapterSnapshot())
		}
	}
}

// buildAdapterSnapshot builds SessionInfo from the tracked sessions and enriched state.
func (rt *Runtime) buildAdapterSnapshot() []pty.SessionInfo {
	rt.sessionsMu.Lock()
	defer rt.sessionsMu.Unlock()

	sessions := rt.stateMgr.GetSessions()
	var out []pty.SessionInfo
	for name, tracked := range rt.tracked {
		var sess *model.Session
		for _, s := range sessions {
			if s.Name == name {
				sess = s
				break
			}
		}

		// Build SessionInfo from instance + session data.
		info := pty.SessionInfo{
			Instance: tracked.inst,
			ID:       name,
			Pid:      tracked.inst.Pid,
			Socket:   rt.daemonReg.SocketPath(name),
		}

		if sess != nil {
			if len(sess.Windows) > 0 && len(sess.Windows[0].Panes) > 0 {
				pane := sess.Windows[0].Panes[0]
				info.Shell = pane.CurrentCommand
				info.Cwd = pane.CurrentPath
				info.ShellPid = pane.PID
			}
			info.Created = sess.Created.Format(time.RFC3339)
		}

		out = append(out, info)
	}

	return out
}

// listPanes returns the current daemon panes for the agent detector, using the
// shared snapshot rather than re-scanning the registry.
func (rt *Runtime) listPanes() []toolevents.PaneInfo {
	infos := rt.adapter.List()
	out := make([]toolevents.PaneInfo, 0, len(infos))
	for _, d := range infos {
		pid := d.ShellPid
		if pid == 0 {
			pid = d.Pid
		}
		out = append(out, toolevents.PaneInfo{
			PaneID:  model.SessionRef{Session: d.ID, Window: 0, Pane: 0}.PaneID(),
			Session: d.ID,
			Window:  0,
			PID:     pid,
		})
	}
	return out
}

// lookupPane resolves a pane identifier to live pane state for the reconciler.
func (rt *Runtime) lookupPane(paneID string) toolevents.PaneState {
	ref, err := model.ParseSessionRef(paneID)
	if err != nil {
		return toolevents.PaneState{Exists: false}
	}
	for _, d := range rt.adapter.List() {
		if d.ID == ref.Session {
			pid := d.ShellPid
			if pid == 0 {
				pid = d.Pid
			}
			return toolevents.PaneState{Exists: true, CurrentCommand: d.Shell, PID: pid}
		}
	}
	return toolevents.PaneState{Exists: false}
}

// checkPrompt guards the stuck monitor: it reports whether a pane is sitting at
// an input prompt so we do not flag it stuck.
func (rt *Runtime) checkPrompt(paneID string) (bool, bool) {
	ref, err := model.ParseSessionRef(paneID)
	if err != nil {
		return false, false
	}
	if text, err := rt.adapter.Capture(ref.Session); err == nil {
		return toolevents.DetectPrompt(text).IsPrompt, true
	}
	return false, false
}

// registryView is the subset of pty.Registry used by the adapter, kept narrow
// so tests can substitute a fake without implementing the full API.
type registryView interface {
	Create(name, shell, cwd string, cols, rows uint16) (pty.SessionInfo, error)
	Kill(name string) error
	Capture(name string) (string, error)
	CaptureTail(name string, maxBytes int) (string, error)
	SocketPath(name string) string
}

// daemonAdapter wraps a registryView and presents one immutable snapshot to
// both the state and peer packages. It collapses the previous peerDaemonAdapter
// and daemonRegAdapter into a single conversion.
type daemonAdapter struct {
	reg  registryView
	mu   sync.RWMutex
	snap []pty.SessionInfo // snapshot updated by runtime from tracked map, not from directory scans
}

// refresh updates the snapshot from the authoritative tracked map and enriched state.
// Called by runtime after enrichment tick; does NOT scan the directory.
func (a *daemonAdapter) refresh(infos []pty.SessionInfo) {
	a.mu.Lock()
	a.snap = infos
	a.mu.Unlock()
}

// List returns the last refreshed snapshot. Consumers receive a shallow copy so
// one caller cannot mutate the shared slice. This is the authoritative source for
// session membership, derived from the runtime's tracked map, not directory scans.
func (a *daemonAdapter) List() []pty.SessionInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]pty.SessionInfo, len(a.snap))
	copy(out, a.snap)
	return out
}

func (a *daemonAdapter) Create(name, shell, cwd string, cols, rows uint16) (pty.SessionInfo, error) {
	return a.reg.Create(name, shell, cwd, cols, rows)
}

func (a *daemonAdapter) Kill(name string) error { return a.reg.Kill(name) }

func (a *daemonAdapter) Capture(name string) (string, error) { return a.reg.Capture(name) }

func (a *daemonAdapter) CaptureTail(name string, maxBytes int) (string, error) {
	return a.reg.CaptureTail(name, maxBytes)
}

func (a *daemonAdapter) SocketPath(name string) string { return a.reg.SocketPath(name) }

func (a *daemonAdapter) SessionCWD(session string) string {
	for _, d := range a.List() {
		if d.ID == session {
			return d.Cwd
		}
	}
	return ""
}

// CapturePaneContent implements toolevents.CaptureClient using the shared
// snapshot adapter and a SessionRef parser.
func (a *daemonAdapter) CapturePaneContent(paneID string) (string, error) {
	ref, err := model.ParseSessionRef(paneID)
	if err != nil {
		return "", err
	}
	return a.Capture(ref.Session)
}

// runShellNameWatcher polls active-pane foreground commands and triggers AI
// naming for meaningful new processes.
func runShellNameWatcher(ctx context.Context, mgr *state.Manager, adapter *daemonAdapter, provider ForegroundProvider) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	lastCmd := make(map[string]string)
	lastFire := make(map[string]time.Time)
	named := make(map[string]bool)
	const firstInterval = 20 * time.Second
	const renameInterval = 3 * time.Minute

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, d := range adapter.List() {
				pid := d.ShellPid
				if pid == 0 {
					pid = d.Pid
				}
				cmd, ok := provider.Foreground(pid)
				if !ok {
					continue
				}
				cmd = strings.TrimSpace(cmd)
				if cmd == "" || shellNames[cmd] || trivialCmds[cmd] || cmd == lastCmd[d.ID] {
					continue
				}
				lastCmd[d.ID] = cmd

				interval := firstInterval
				if named[d.ID] {
					interval = renameInterval
				}
				if t, ok := lastFire[d.ID]; ok && time.Since(t) < interval {
					continue
				}
				lastFire[d.ID] = time.Now()
				named[d.ID] = true

				cmds := []string{cmd}
				if content, err := adapter.Capture(d.ID); err == nil {
					cmds = recentCommands(content, cmd)
				}
				go mgr.TriggerShellNaming(d.ID, cmds)
			}
		}
	}
}

// recentCommands extracts up to a handful of recent input lines from captured
// pane content as a hint for naming.
func recentCommands(content, foreground string) []string {
	lines := strings.Split(content, "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < 6; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		l = strings.TrimLeft(l, "$#%> ")
		if l == "" {
			continue
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return []string{foreground}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func defaultSessionDir() string {
	if dir := os.Getenv("TERMYARD_SESSION_DIR"); dir != "" {
		return dir
	}
	uid := fmt.Sprintf("%d", os.Getuid())
	return fmt.Sprintf("/tmp/termyard-sessions-%s", uid)
}
