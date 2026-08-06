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
	"github.com/anh-chu/termyard/pkg/config"
	"github.com/anh-chu/termyard/pkg/groupsync"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/namer"
	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/portforward"
	"github.com/anh-chu/termyard/pkg/preferences"
	"github.com/anh-chu/termyard/pkg/pty"
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

// Runtime assembles the server's long-lived dependencies and exposes explicit
// Start/Ready/Stop lifecycle control. It owns the goroutines that used to be
// launched inline in Execute, so startup order, readiness, and cancellation are
// all visible in one place.
type Runtime struct {
	ctx    context.Context
	cancel context.CancelFunc

	// v2Mode is resolved once in newRuntime (TERMYARD_V2_STATE=1) and never
	// changes for the lifetime of the runtime. It gates every legacy-only
	// background loop and write path so the two authorities never run
	// concurrently.
	v2Mode bool

	ready chan struct{}
	opts  *server.Options

	// Core stores / registries
	stateMgr   *state.Manager
	tracker    *toolevents.Tracker
	actTracker *activity.Tracker
	daemonReg  *pty.Registry
	adapter    *daemonAdapter

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

	// Dormant v2 store (Task 3). It is opened here but not wired into the
	// active session/workspace source of truth yet.
	v2store *state.Store

	// Task 5 v2 catalog and reconciler (shadow mode behind TERMYARD_V2_STATE).
	v2Catalog    *state.Catalog
	v2Reconciler *state.Reconciler

	// Task 7 v2 local command service.
	v2CommandSvc *state.SessionCommandService

	// Task 9 v2 remote create coordinator.
	v2RemoteCreate *state.RemoteCreateCoordinator

	// Task 10 durable v2 browser state stream.
	v2StateStream *ws.StateStreamHub

	// v2Enricher supplies runtime metadata (cwd, pid, prompt preview, activity)
	// for catalog projection. Its /proc-derived fields are refreshed on the
	// same cadence as the daemon adapter snapshot (see refreshDaemonState) so
	// catalog enrichment itself never performs blocking I/O.
	v2Enricher *v2RuntimeEnricher

	// Test hook: if set, overrides DetectAndCleanupCrashes in runDaemonRefresh.
	detectCrashesFn func() []pty.LifecycleRecord
}

// newRuntime builds the dependency graph without starting any monitors. All
// construction-time errors are returned synchronously.
func newRuntime(c *cli.Command) (*Runtime, error) {
	// Resolve the runtime mode BEFORE constructing dependencies. In v2 mode,
	// legacy state stores are not constructed; in legacy mode, v2 is not constructed.
	// This ensures single authority: either v2 or legacy, never both.
	v2Mode := os.Getenv("TERMYARD_V2_STATE") == "1"

	rt := &Runtime{
		v2Mode:     v2Mode,
		tracker:    toolevents.NewTracker(),
		actTracker: activity.NewTracker(),
		ready:      make(chan struct{}),
		fgProvider: newForegroundProvider(),
	}
	// The legacy state.Manager is the single-authority boundary: it must not
	// be constructed at all in v2 mode, not merely constructed-and-gated. Every
	// consumer below (peer.Manager, ws.Hub, sessionlaunch.Service, server
	// options, SessionDeps) is either nil-safe for rt.stateMgr or takes a
	// narrower interface that a nil *state.Manager still satisfies safely via
	// explicit nil checks at the conversion boundary (see ws.AsStateSource).
	if !v2Mode {
		rt.stateMgr = state.NewManager()
	}
	rt.tracker.EnablePersistence()

	rt.daemonReg = pty.NewRegistry(defaultSessionDir())
	if lcStore, err := pty.NewLifecycleStore(pty.DefaultStateDir()); err != nil {
		logrus.WithError(err).Warn("failed to create lifecycle store -- crash recovery disabled")
	} else {
		rt.daemonReg.SetLifecycleStore(lcStore)
		if crashed := rt.daemonReg.DetectAndCleanupCrashes(); len(crashed) > 0 {
			logrus.WithField("count", len(crashed)).Warn("detected crashed sessions from previous run")
		}
	}

	rt.adapter = &daemonAdapter{reg: rt.daemonReg}
	if rt.stateMgr != nil {
		rt.stateMgr.SetDaemonRegistry(rt.adapter)
	}

	rt.reconciler = toolevents.NewReconciler(rt.tracker, rt.lookupPane, 3*time.Second)
	rt.detector = toolevents.NewDetector(rt.tracker, rt.listPanes, 5*time.Second)
	rt.silenceMonitor = toolevents.NewSilenceMonitor(rt.tracker, rt.detector, rt.adapter)

	// Legacy state stores: only constructed in legacy mode.
	var attrsStore *sessionattrs.Store
	var orderStore *sessionorder.Store
	var groupStore *groupsync.Store

	if !v2Mode {
		var err error
		attrsStore, err = sessionattrs.NewStore()
		if err != nil {
			logrus.WithError(err).Warn("failed to load session-attrs store, sync disabled")
			attrsStore = nil
		}

		orderStore, err = sessionorder.NewStore()
		if err != nil {
			logrus.WithError(err).Warn("failed to load session-order store, sync disabled")
			orderStore = nil
		}

		groupStore, err = groupsync.NewStore()
		if err != nil {
			logrus.WithError(err).Warn("failed to load groups store, sync disabled")
			groupStore = nil
		}
	}

	prefStore, err := preferences.NewStore()
	if err != nil {
		logrus.WithError(err).Warn("failed to load preferences, using defaults")
		prefStore = nil
	}

	applyNamerFromPrefs := func(p *preferences.Preferences) {
		if rt.stateMgr == nil {
			return
		}
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

	// v2 mode: initialize v2 store, catalog, and services. All failures are FATAL.
	if v2Mode {
		v2Dir, err := config.V2StateDir()
		if err != nil {
			return nil, fmt.Errorf("failed to determine v2 state directory: %w", err)
		}

		// This node's own v2 catalog Owner MUST be its own authenticated
		// identity, converted through the single canonical
		// state.OwnerIDFromFingerprint function -- the same function peer
		// validation (pkg/peer/manager.go) uses on the receiving end. Without
		// this, a fresh store would generate an unrelated random OwnerID that
		// no peer could ever authenticate against.
		selfOwner := state.OwnerIDFromFingerprint(nodeIdentity.Fingerprint())
		rt.v2store, err = state.OpenStore(v2Dir, nodeIdentity.Fingerprint(), state.StoreOptions{Owner: selfOwner})
		if err != nil {
			return nil, fmt.Errorf("failed to open v2 state store: %w", err)
		}

		rt.v2Catalog = state.NewCatalog(rt.v2store.Owner(), rt.v2store)
		if err := rt.v2Catalog.Load(); err != nil {
			return nil, fmt.Errorf("failed to load v2 catalog: %w", err)
		}

		enricher := &v2RuntimeEnricher{adapter: rt.adapter, actTracker: rt.actTracker}
		rt.v2Enricher = enricher
		rt.v2Reconciler = state.NewReconciler(rt.v2Catalog, rt.daemonReg, enricher, state.ReconcilerOptions{DisablePendingCreates: true})
		rt.v2CommandSvc = state.NewSessionCommandService(rt.v2Catalog, rt.daemonReg, enricher, state.SessionCommandServiceOptions{Owner: rt.v2Catalog.Owner()})
		rt.v2RemoteCreate = state.NewRemoteCreateCoordinator(rt.v2Catalog, rt.daemonReg, state.RemoteCreateCoordinatorOptions{Owner: rt.v2Catalog.Owner()})
		rt.v2StateStream = ws.NewStateStreamHub(rt.v2Catalog, nil)
		logrus.WithField("owner", rt.v2Catalog.Owner()).Info("v2 mode enabled (legacy stores disabled)")
	}

	peerStore, err := identity.NewPeerStore()
	if err != nil {
		return nil, fmt.Errorf("failed to load peer store: %w", err)
	}

	// peer.Manager takes the narrow LocalSessionSource interface, not a
	// concrete *state.Manager. In v2 mode rt.stateMgr is nil (no legacy
	// manager constructed at all), so localSrc must be assigned through this
	// explicit nil check -- passing rt.stateMgr directly would wrap a typed
	// nil pointer in a non-nil interface value, which peer.Manager's
	// `!= nil` checks would then fail to catch.
	var localSrc peer.LocalSessionSource
	if rt.stateMgr != nil {
		localSrc = rt.stateMgr
	}
	rt.peerMgr = peer.NewManager(nodeIdentity, peerStore, localSrc)
	if rt.v2Catalog != nil {
		// Wired explicitly and independently of rt.stateMgr's presence: v2 mode
		// never constructs a legacy manager to carry this.
		rt.peerMgr.SetV2Catalog(rt.v2Catalog)
	}
	if rt.v2RemoteCreate != nil {
		rt.peerMgr.SetRemoteCreateCoordinator(rt.v2RemoteCreate)
	}
	if rt.v2store != nil {
		rt.peerMgr.SetRemoteStore(rt.v2store)
		if err := rt.peerMgr.LoadRemoteCatalogCache(); err != nil {
			logrus.WithError(err).Warn("failed to load remote catalog cache")
		}
	}
	if rt.v2StateStream != nil {
		// Multi-node: stream cached remote-owner catalogs (peer.Manager's
		// already-validated remoteCatalogs cache) to the browser alongside
		// this node's own local catalog.
		rt.v2StateStream.AttachRemoteCatalogSource(rt.peerMgr)
	}
	rt.detector.SetHost(rt.peerMgr.LocalID(), rt.peerMgr.LocalName())
	rt.silenceMonitor.SetHost(rt.peerMgr.LocalID(), rt.peerMgr.LocalName())
	rt.reconciler.SetHost(rt.peerMgr.LocalID(), rt.peerMgr.LocalName())

	// Legacy fire-and-forget remote launcher: only constructed in legacy
	// mode. In v2 mode, remote creates must go through the reliable v2
	// remote-create coordinator (v2RemoteLauncher below); constructing this
	// path anyway would let v2-only nodes silently fall back to a launcher
	// that reports success once a frame is merely enqueued, not once the
	// remote session actually exists.
	var legacyRemoteLauncher sessionlaunch.RemoteLauncher
	if !v2Mode {
		legacyRemoteLauncher = func(ctx context.Context, req sessionlaunch.Request) (sessionlaunch.Result, error) {
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
		}
	}

	// v2 remote launcher: routes through RemoteCreateCoordinator on the
	// remote owner via the reliable command RPC (pkg/peer's
	// Manager.SendRemoteCreate), which blocks for a genuine ack/nack instead
	// of merely enqueuing a frame. Only constructed in v2 mode.
	var v2RemoteLauncher sessionlaunch.RemoteLauncher
	if v2Mode {
		v2RemoteLauncher = func(ctx context.Context, req sessionlaunch.Request) (sessionlaunch.Result, error) {
			cmdID := state.CommandID(req.CommandID)
			if cmdID == "" {
				cmdID = state.NewCommandID()
			}
			rreq := state.RemoteCreateRequest{
				IntentID:       cmdID,
				Requester:      rt.v2Catalog.Owner(),
				Name:           req.Name,
				Shell:          req.Command,
				Cwd:            req.Path,
				WorktreeBranch: req.WorktreeBranch,
				Cols:           req.Cols,
				Rows:           req.Rows,
				AgentType:      req.AgentType,
				ScheduleID:     req.ScheduleID,
			}
			res, err := rt.peerMgr.SendRemoteCreate(ctx, req.Host, rreq)
			if err != nil {
				return sessionlaunch.Result{}, err
			}
			return sessionlaunch.Result{Name: res.DisplayName, Host: req.Host, Path: res.Path, Remote: true}, nil
		}
	}

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
		Identity: nodeIdentity,
		Refresh:  rt.refreshSessionsFunc,
		Remote:   legacyRemoteLauncher,
		V2Remote: v2RemoteLauncher,
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
	}

	streamReg := peer.NewStreamRegistry()
	captureReg := peer.NewCaptureRegistry()
	fileReadReg := peer.NewFileReadRegistry()

	deps := peer.SessionDeps{
		Manager:                 rt.peerMgr,
		LocalMgr:                rt.stateMgr,
		V2Catalog:               rt.v2Catalog,
		Identity:                nodeIdentity,
		ActTracker:              rt.actTracker,
		ToolTracker:             rt.tracker,
		PeerStore:               peerStore,
		DaemonReg:               rt.adapter,
		Launch:                  launchSvc,
		StreamReg:               streamReg,
		CaptureReg:              captureReg,
		FileReadReg:             fileReadReg,
		V2CommandSvc:            rt.v2CommandSvc,
		RemoteCreateCoordinator: rt.v2RemoteCreate,
	}

	peerHandler := peer.NewHandler(deps, streamReg)

	rt.supervisor = peer.NewLinkSupervisor(deps)

	rt.wikiSup = wikilite.NewSupervisor()

	// ws.Hub takes the narrow StateSource interface. AsStateSource converts
	// a possibly-nil rt.stateMgr into a genuine nil interface value (v2 mode
	// constructs no legacy manager at all) instead of a non-nil interface
	// wrapping a nil pointer.
	rt.hub = ws.NewHub(ws.AsStateSource(rt.stateMgr), rt.tracker)
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
		DaemonReg:        rt.daemonReg,
		Launch:           launchSvc,
		CWDResolver:      rt.adapter,
		RefreshSessions:  rt.refreshSessionsFunc,
		OnDaemonOutput: func(paneID string) {
			rt.silenceMonitor.RecordOutput(paneID)
		},
		OnPrefsChanged: applyNamerFromPrefs,
		Hub:            rt.hub,
	}
	launchSvc.Hub = rt.hub
	if rt.v2CommandSvc != nil {
		rt.opts.V2CommandSvc = rt.v2CommandSvc
		rt.opts.V2Catalog = rt.v2Catalog
		rt.opts.V2StateStream = rt.v2StateStream
		launchSvc.V2Commander = rt.v2CommandSvc
	}

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
				CommandID:      req.CommandID,
			})
			if err != nil {
				return err
			}
			return nil
		}, logrus.WithField("component", "scheduler"))
		rt.schedulerRunner.SetCapEnforcer(func(job scheduler.Job) {
			server.EnforceScheduleCap(rt.opts, job.ID, job.MaxConcurrency-1)
		})
		if rt.v2Catalog != nil {
			rt.schedulerRunner.Owner = rt.v2Catalog.Owner().String()
		}
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
	if !rt.v2Mode {
		// AI session naming only writes to the legacy state.Manager; v2 has its
		// own naming path via SessionCommandService's create flow.
		go runShellNameWatcher(rt.ctx, rt.stateMgr, rt.adapter, rt.fgProvider)
	}

	go rt.runDaemonRefresh(rt.ctx)

	if rt.v2Reconciler != nil {
		go rt.v2Reconciler.Run(rt.ctx)
	}
	if rt.v2CommandSvc != nil {
		go rt.v2CommandSvc.Run(rt.ctx)
	}
	if rt.v2RemoteCreate != nil {
		go rt.v2RemoteCreate.Run(rt.ctx)
	}

	if rt.schedulerRunner != nil {
		go rt.schedulerRunner.Run(rt.ctx)
	}

	close(rt.ready)
	return nil
}

// Ready returns a channel that is closed once Start has finished launching the
// runtime's background goroutines. The HTTP server can safely begin using opts.
func (rt *Runtime) Ready() <-chan struct{} {
	return rt.ready
}

// Stop cancels the runtime context and stops all monitors started by Start.
func (rt *Runtime) Stop() {
	if rt.cancel != nil {
		rt.cancel()
	}
	if rt.v2StateStream != nil {
		rt.v2StateStream.Close()
	}
}

// Options returns the assembled server options after newRuntime has succeeded.
func (rt *Runtime) Options() *server.Options {
	return rt.opts
}

// refreshSessionsFunc builds a model.Session snapshot from daemon metadata and
// pushes it to the state manager. It is a narrow helper for the launch service
// and recovery callbacks.
func (rt *Runtime) refreshSessionsFunc() {
	// In v2 mode, writing session snapshots into the legacy state.Manager is a
	// shadow write: v2 mode has its own reconciler/catalog as the single
	// source of truth. Callers (sessionlaunch.Service.Refresh, WS teardown
	// paths, recover/rename handlers) invoke this unconditionally today, so
	// the no-op guard must live here, not just at the periodic-refresh call
	// site (refreshDaemonState).
	if rt.v2Mode {
		return
	}
	rt.refreshSessions(rt.adapter.List())
}

func (rt *Runtime) refreshSessions(infos []pty.SessionInfo) {
	sessions := make([]*model.Session, 0, len(infos))
	for _, d := range infos {
		var created time.Time
		if t, err := time.Parse(time.RFC3339, d.Created); err == nil {
			created = t
		}
		sessions = append(sessions, &model.Session{
			Name:        d.ID,
			Created:     created,
			Backend:     "daemon",
			ProjectPath: d.Cwd,
			Windows: []*model.Window{{
				ID:     "daemon-" + d.ID,
				Name:   "shell",
				Active: true,
				Panes: []*model.Pane{{
					ID:          model.SessionRef{Session: d.ID, Window: 0, Pane: 0}.PaneID(),
					Active:      true,
					CurrentPath: d.Cwd,
				}},
			}},
		})
	}
	rt.stateMgr.UpdateSessions(sessions)
}

// classifyAndCleanupCrashes is the crash-detection half of the refresh cycle.
// It uses the test hook if present, otherwise the real lifecycle store.
func (rt *Runtime) classifyAndCleanupCrashes() {
	var crashed []pty.LifecycleRecord
	if rt.detectCrashesFn != nil {
		crashed = rt.detectCrashesFn()
	} else if lcStore := rt.daemonReg.LifecycleStore(); lcStore != nil {
		crashed = rt.daemonReg.DetectAndCleanupCrashes()
	}
	if len(crashed) > 0 {
		logrus.WithField("count", len(crashed)).Warn("detected newly crashed sessions")
	}
}

// refreshDaemonState runs one classify-then-publish cycle.  Crash detection
// happens first so a crashed session is never broadcast as live in the same
// cycle.
//
// In v2 mode, crash detection still runs (the v2 reconciler and daemon
// registry need it), but the legacy publish step (refreshSessions, which
// writes into the legacy state.Manager) is skipped: v2 mode has its own
// classify-before-publish reconciler (pkg/state.Reconciler) driven off the
// same daemon registry, so publishing through both would be a dual-write.
func (rt *Runtime) refreshDaemonState() {
	rt.classifyAndCleanupCrashes()
	infos := rt.adapter.refresh()
	if !rt.v2Mode {
		rt.refreshSessions(infos)
	}
	// Refresh the v2 enricher's runtime metadata cache (cwd, pid, command) on
	// the same cadence as the daemon adapter snapshot. This is the only place
	// that performs the blocking /proc/<pid>/cwd read; Enrich itself only does
	// an in-memory map lookup, keeping catalog projection off the I/O path.
	if rt.v2Enricher != nil {
		rt.v2Enricher.refreshRuntimeCache()
	}
}

// runDaemonRefresh keeps an up-to-date snapshot of daemon sessions and runs
// crash detection on the same cadence as state discovery.
func (rt *Runtime) runDaemonRefresh(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// One immediate refresh so tests see state without waiting for the tick.
	// Classify first so the initial snapshot cannot publish crash state as live.
	rt.refreshDaemonState()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rt.refreshDaemonState()
		}
	}
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
	Create(name, shell, cwd string, cols, rows uint16) error
	Kill(name string) error
	Capture(name string) (string, error)
	CaptureTail(name string, maxBytes int) (string, error)
	SocketPath(name string) string
	List() []pty.SessionInfo
	IsSessionDead(name string) bool
	CrashedSessions() []pty.LifecycleRecord
	GenerationFor(name string) string
}

// daemonAdapter wraps a registryView and presents one immutable snapshot to
// both the state and peer packages. It collapses the previous peerDaemonAdapter
// and daemonRegAdapter into a single conversion.
type daemonAdapter struct {
	reg  registryView
	mu   sync.RWMutex
	snap []pty.SessionInfo
}

// refresh captures a new snapshot from the registry and stores it locally.
func (a *daemonAdapter) refresh() []pty.SessionInfo {
	infos := a.reg.List()
	a.mu.Lock()
	a.snap = infos
	a.mu.Unlock()
	return infos
}

// List returns the last refreshed snapshot. Consumers receive a shallow copy so
// one caller cannot mutate the shared slice.
func (a *daemonAdapter) List() []pty.SessionInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]pty.SessionInfo, len(a.snap))
	copy(out, a.snap)
	return out
}

func (a *daemonAdapter) Create(name, shell, cwd string, cols, rows uint16) error {
	return a.reg.Create(name, shell, cwd, cols, rows)
}

func (a *daemonAdapter) Kill(name string) error { return a.reg.Kill(name) }

func (a *daemonAdapter) Capture(name string) (string, error) { return a.reg.Capture(name) }

func (a *daemonAdapter) CaptureTail(name string, maxBytes int) (string, error) {
	return a.reg.CaptureTail(name, maxBytes)
}

func (a *daemonAdapter) SocketPath(name string) string { return a.reg.SocketPath(name) }

func (a *daemonAdapter) GenerationFor(name string) string { return a.reg.GenerationFor(name) }

func (a *daemonAdapter) IsSessionDead(name string) bool { return a.reg.IsSessionDead(name) }

func (a *daemonAdapter) CrashedSessions() []state.CrashedSessionInfo {
	recs := a.reg.CrashedSessions()
	out := make([]state.CrashedSessionInfo, len(recs))
	for i, rec := range recs {
		out[i] = state.CrashedSessionInfo{
			ID:         rec.ID,
			Shell:      rec.Shell,
			Cwd:        rec.Cwd,
			Cols:       rec.Cols,
			Rows:       rec.Rows,
			CreatedAt:  rec.CreatedAt.Format(time.RFC3339),
			DaemonPID:  rec.DaemonPID,
			Generation: rec.Generation,
		}
	}
	return out
}

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

// v2RuntimeEnricher supplies live runtime fields for v2 catalog records
// without mutating persisted state.
type v2RuntimeEnricher struct {
	adapter    *daemonAdapter
	actTracker *activity.Tracker

	previewMu    sync.Mutex
	previewCache map[string]*v2PreviewCacheEntry

	runtimeMu    sync.RWMutex
	runtimeCache map[string]v2RuntimeCacheEntry
}

// v2RuntimeCacheEntry holds the process-derived fields for one session
// (daemon adapter snapshot fields plus the live /proc/<pid>/cwd read) as of
// the last background refresh. Consumers of this cache may see values that
// are up to v2RuntimeCacheInterval stale; this mirrors the accepted staleness
// of the throttled prompt-preview cache above and is a deliberate trade for
// keeping catalog projection free of blocking I/O.
type v2RuntimeCacheEntry struct {
	daemonPID      int
	shellPID       int
	currentCommand string
	currentPath    string
}

// readProcCwd reads the live working directory of a process from /proc. It is
// a package-level var (not a direct os.Readlink call) so tests can substitute
// a fake implementation without touching the real filesystem.
var readProcCwd = func(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
}

// v2PreviewCacheEntry holds a throttled prompt-preview snapshot for a single
// session so the (up to) 64KiB PTY capture only runs periodically instead of
// on every catalog enrichment call. This mirrors the throttle pattern used by
// state.Manager's legacy preview cache (see pkg/state/preview.go).
type v2PreviewCacheEntry struct {
	preview     string
	lastAttempt time.Time
	inFlight    bool
}

// v2PromptPreviewInterval throttles prompt-preview PTY captures during v2
// catalog enrichment. Matches the legacy manager's promptPreviewInterval.
const v2PromptPreviewInterval = 30 * time.Second

// previewFor returns the cached prompt preview for a session immediately,
// without blocking the catalog projection path. When the cached preview is
// stale (older than v2PromptPreviewInterval) and no refresh is already in
// flight for this session, it kicks off exactly one asynchronous PTY capture
// to refresh the cache for subsequent calls. This keeps catalog snapshot
// publication off the PTY I/O path entirely.
func (e *v2RuntimeEnricher) previewFor(sessionID string) string {
	e.previewMu.Lock()
	if e.previewCache == nil {
		e.previewCache = make(map[string]*v2PreviewCacheEntry)
	}
	entry := e.previewCache[sessionID]
	if entry == nil {
		entry = &v2PreviewCacheEntry{}
		e.previewCache[sessionID] = entry
	}
	cached := entry.preview
	due := time.Since(entry.lastAttempt) >= v2PromptPreviewInterval
	shouldRefresh := due && !entry.inFlight
	if shouldRefresh {
		entry.inFlight = true
		entry.lastAttempt = time.Now()
	}
	e.previewMu.Unlock()

	if shouldRefresh {
		go e.refreshPreview(sessionID)
	}

	return cached
}

// refreshPreview performs the actual (potentially slow) PTY capture off the
// synchronous enrichment path and stores the result for the next previewFor
// call to observe.
func (e *v2RuntimeEnricher) refreshPreview(sessionID string) {
	defer func() {
		e.previewMu.Lock()
		if entry := e.previewCache[sessionID]; entry != nil {
			entry.inFlight = false
		}
		e.previewMu.Unlock()
	}()

	content, err := e.adapter.CaptureTail(sessionID, 64*1024)
	if err != nil {
		return
	}
	preview := model.ExtractPromptPreview(content)

	e.previewMu.Lock()
	if entry := e.previewCache[sessionID]; entry != nil {
		entry.preview = preview
	}
	e.previewMu.Unlock()
}

// refreshRuntimeCache rebuilds the process-derived runtime metadata cache
// (daemon PID, shell PID, current command, cwd) from the daemon adapter's
// snapshot. This is the only place that performs the (up to N, one per
// session) blocking /proc/<pid>/cwd reads; it must only be called from a
// periodic background loop (see Runtime.refreshDaemonState), never from
// Enrich / catalog projection. Building a whole new map and swapping it in
// under the lock keeps readers lock-free of any in-progress refresh.
func (e *v2RuntimeEnricher) refreshRuntimeCache() {
	infos := e.adapter.List()
	next := make(map[string]v2RuntimeCacheEntry, len(infos))
	for _, d := range infos {
		entry := v2RuntimeCacheEntry{
			daemonPID:      d.Pid,
			shellPID:       d.ShellPid,
			currentCommand: d.Shell,
			currentPath:    d.Cwd,
		}

		pid := d.ShellPid
		if pid == 0 {
			pid = d.Pid
		}
		if pid > 0 {
			if liveCwd, err := readProcCwd(pid); err == nil && liveCwd != "" {
				entry.currentPath = liveCwd
			}
		}
		next[d.ID] = entry
	}

	e.runtimeMu.Lock()
	e.runtimeCache = next
	e.runtimeMu.Unlock()
}

// cachedRuntime returns the last background-refreshed metadata for a session
// ID. It is a pure in-memory map lookup: no /proc reads, no daemon adapter
// calls.
func (e *v2RuntimeEnricher) cachedRuntime(id string) (v2RuntimeCacheEntry, bool) {
	e.runtimeMu.RLock()
	defer e.runtimeMu.RUnlock()
	entry, ok := e.runtimeCache[id]
	return entry, ok
}

// Enrich returns runtime fields for a session from in-memory caches only. It
// performs zero /proc reads and zero daemon-adapter list/snapshot calls on
// this path: process metadata comes from the background-refreshed
// runtimeCache (see refreshRuntimeCache), the prompt preview comes from the
// throttled previewFor cache, and activity comes from actTracker's own O(1)
// lookup. This keeps catalog projection (called once per session per
// snapshot, potentially hundreds of times per call) off the blocking I/O
// path entirely; enriched fields may lag reality by up to one background
// refresh interval, which mirrors the accepted staleness of the prompt
// preview cache.
func (e *v2RuntimeEnricher) Enrich(ref state.SessionRef, rec state.LocalSessionRecord) state.SessionRuntime {
	var rt state.SessionRuntime
	id := string(ref.Session)

	if cached, ok := e.cachedRuntime(id); ok {
		rt.DaemonPID = cached.daemonPID
		rt.ShellPID = cached.shellPID
		rt.CurrentCommand = cached.currentCommand
		rt.CurrentPath = cached.currentPath
	}

	rt.PromptPreview = e.previewFor(id)
	if e.actTracker != nil {
		if snap := e.actTracker.Get(id); snap != nil && snap.IdleSeconds >= 0 {
			rt.LastActivity = snap.LastActive
		}
	}
	return rt
}
