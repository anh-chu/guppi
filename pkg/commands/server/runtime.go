package server

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/config"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/portforward"
	"github.com/anh-chu/termyard/pkg/preferences"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/scheduler"
	"github.com/anh-chu/termyard/pkg/server"
	"github.com/anh-chu/termyard/pkg/sessionlaunch"
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
//
// There is exactly one state authority: the canonical store/catalog/command
// service graph below is always constructed, unconditionally. There is no
// runtime mode switch and no environment variable that selects an alternate
// state path.
type Runtime struct {
	ctx    context.Context
	cancel context.CancelFunc
	ready  chan struct{}
	opts   *server.Options

	tracker    *toolevents.Tracker
	actTracker *activity.Tracker
	daemonReg  *pty.Registry
	adapter    *daemonAdapter

	// Tool-event monitors
	reconciler     *toolevents.Reconciler
	detector       *toolevents.Detector
	silenceMonitor *toolevents.SilenceMonitor

	// Canonical state graph -- the single source of truth for sessions.
	store           *state.Store
	catalog         *state.Catalog
	stateReconciler *state.Reconciler
	commandSvc      *state.SessionCommandService
	remoteCreate    *state.RemoteCreateCoordinator
	stateStream     *ws.StateStreamHub

	// enricher supplies runtime metadata (cwd, pid, prompt preview, activity)
	// for catalog projection. Its /proc-derived fields are refreshed on the
	// same cadence as the daemon adapter snapshot (see refreshDaemonState) so
	// catalog enrichment itself never performs blocking I/O.
	enricher *runtimeEnricher

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
	hub *ws.Hub

	// Test hook: if set, overrides DetectAndCleanupCrashes in runDaemonRefresh.
	detectCrashesFn func() []pty.LifecycleRecord
}

// newRuntime builds the dependency graph without starting any monitors. All
// construction-time errors are returned synchronously.
func newRuntime(c *cli.Command) (*Runtime, error) {
	rt := &Runtime{
		tracker:    toolevents.NewTracker(),
		actTracker: activity.NewTracker(),
		ready:      make(chan struct{}),
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

	rt.reconciler = toolevents.NewReconciler(rt.tracker, rt.lookupPane, 3*time.Second)
	rt.detector = toolevents.NewDetector(rt.tracker, rt.listPanes, 5*time.Second)
	rt.silenceMonitor = toolevents.NewSilenceMonitor(rt.tracker, rt.detector, rt.adapter)

	prefStore, err := preferences.NewStore()
	if err != nil {
		logrus.WithError(err).Warn("failed to load preferences, using defaults")
		prefStore = nil
	}

	// applyNamerFromPrefs is retained as an OnPrefsChanged hook but is
	// currently a no-op: AI naming against the canonical catalog is not yet
	// wired (see docs -- deferred, tracked separately from this cutover).
	applyNamerFromPrefs := func(p *preferences.Preferences) {}
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

	// Canonical state graph: store, catalog, and services are always
	// constructed. There is no environment variable or flag that selects an
	// alternate state path -- all failures here are FATAL to startup.
	stateDir, err := config.StateDir()
	if err != nil {
		return nil, fmt.Errorf("failed to determine state directory: %w", err)
	}

	// This node's own catalog Owner MUST be its own authenticated identity,
	// converted through the single canonical state.OwnerIDFromFingerprint
	// function -- the same function peer validation (pkg/peer/manager.go)
	// uses on the receiving end. Without this, a fresh store would generate
	// an unrelated random OwnerID that no peer could ever authenticate
	// against.
	selfOwner := state.OwnerIDFromFingerprint(nodeIdentity.Fingerprint())
	rt.store, err = state.OpenStore(stateDir, nodeIdentity.Fingerprint(), state.StoreOptions{Owner: selfOwner})
	if err != nil {
		return nil, fmt.Errorf("failed to open state store: %w", err)
	}

	rt.catalog = state.NewCatalog(rt.store.Owner(), rt.store)
	if err := rt.catalog.Load(); err != nil {
		return nil, fmt.Errorf("failed to load catalog: %w", err)
	}

	enricher := &runtimeEnricher{adapter: rt.adapter, actTracker: rt.actTracker}
	rt.enricher = enricher
	rt.stateReconciler = state.NewReconciler(rt.catalog, rt.daemonReg, enricher, state.ReconcilerOptions{DisablePendingCreates: true})
	rt.commandSvc = state.NewSessionCommandService(rt.catalog, rt.daemonReg, enricher, state.SessionCommandServiceOptions{Owner: rt.catalog.Owner()})
	rt.remoteCreate = state.NewRemoteCreateCoordinator(rt.catalog, rt.daemonReg, state.RemoteCreateCoordinatorOptions{Owner: rt.catalog.Owner()})
	rt.stateStream = ws.NewStateStreamHub(rt.catalog, nil)
	logrus.WithField("owner", rt.catalog.Owner()).Info("canonical state store opened")

	peerStore, err := identity.NewPeerStore()
	if err != nil {
		return nil, fmt.Errorf("failed to load peer store: %w", err)
	}

	rt.peerMgr = peer.NewManager(nodeIdentity, peerStore)
	rt.peerMgr.SetCatalog(rt.catalog)
	rt.peerMgr.SetRemoteCreateCoordinator(rt.remoteCreate)
	rt.peerMgr.SetRemoteStore(rt.store)
	if err := rt.peerMgr.LoadRemoteCatalogCache(); err != nil {
		logrus.WithError(err).Warn("failed to load remote catalog cache")
	}
	// Multi-node: stream cached remote-owner catalogs (peer.Manager's
	// already-validated remoteCatalogs cache) to the browser alongside this
	// node's own local catalog.
	rt.stateStream.AttachRemoteCatalogSource(rt.peerMgr)
	rt.detector.SetHost(rt.peerMgr.LocalID(), rt.peerMgr.LocalName())
	rt.silenceMonitor.SetHost(rt.peerMgr.LocalID(), rt.peerMgr.LocalName())
	rt.reconciler.SetHost(rt.peerMgr.LocalID(), rt.peerMgr.LocalName())

	// Remote launcher: routes through RemoteCreateCoordinator on the remote
	// owner via the reliable command RPC (pkg/peer's
	// Manager.SendRemoteCreate), which blocks for a genuine ack/nack instead
	// of merely enqueuing a frame. This is the only remote-create path.
	remoteLauncher := func(ctx context.Context, req sessionlaunch.Request) (sessionlaunch.Result, error) {
		cmdID := state.CommandID(req.CommandID)
		if cmdID == "" {
			cmdID = state.NewCommandID()
		}
		rreq := state.RemoteCreateRequest{
			IntentID:       cmdID,
			Requester:      rt.catalog.Owner(),
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

	launchSvc := &sessionlaunch.Service{
		DaemonReg:      rt.daemonReg,
		Identity:       nodeIdentity,
		Refresh:        rt.refreshSessionsFunc,
		ReliableRemote: remoteLauncher,
		Names: func(host string) []string {
			if host != "" && rt.peerMgr != nil && !rt.peerMgr.IsLocal(host) {
				snap, ok := rt.peerMgr.RemoteCatalogSnapshot(state.OwnerIDFromFingerprint(host))
				if !ok {
					return nil
				}
				names := make([]string, 0, len(snap.Sessions))
				for _, rec := range snap.Sessions {
					if rec.Name != "" {
						names = append(names, rec.Name)
					}
				}
				return names
			}
			recs := rt.catalog.Sessions()
			names := make([]string, 0, len(recs))
			for _, rec := range recs {
				if rec.Name != "" {
					names = append(names, rec.Name)
				}
			}
			return names
		},
	}

	streamReg := peer.NewStreamRegistry()
	captureReg := peer.NewCaptureRegistry()
	fileReadReg := peer.NewFileReadRegistry()

	deps := peer.SessionDeps{
		Manager:                 rt.peerMgr,
		Catalog:                 rt.catalog,
		Identity:                nodeIdentity,
		ActTracker:              rt.actTracker,
		ToolTracker:             rt.tracker,
		PeerStore:               peerStore,
		DaemonReg:               rt.adapter,
		Launch:                  launchSvc,
		StreamReg:               streamReg,
		CaptureReg:              captureReg,
		FileReadReg:             fileReadReg,
		CommandSvc:              rt.commandSvc,
		RemoteCreateCoordinator: rt.remoteCreate,
	}

	peerHandler := peer.NewHandler(deps, streamReg)

	rt.supervisor = peer.NewLinkSupervisor(deps)

	rt.wikiSup = wikilite.NewSupervisor()

	// ws.Hub has no separate state source to wire in: the canonical catalog
	// is the only state authority.
	rt.hub = ws.NewHub(rt.tracker)
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
		Catalog:          rt.catalog,
		CommandSvc:       rt.commandSvc,
		StateStream:      rt.stateStream,
		Tracker:          rt.tracker,
		ActivityTracker:  rt.actTracker,
		PushKeys:         pushKeys,
		PushStore:        pushStore,
		PrefStore:        prefStore,
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
	launchSvc.Commander = rt.commandSvc

	if schedulerStore != nil {
		rt.schedulerRunner = scheduler.NewRunner(schedulerStore, rt.peerMgr, func(req scheduler.CreateSessionReq) error {
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
		rt.schedulerRunner.Owner = rt.catalog.Owner()
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
	// AI session naming flows through SessionCommandService's create path.

	go rt.runDaemonRefresh(rt.ctx)

	go rt.stateReconciler.Run(rt.ctx)
	go rt.commandSvc.Run(rt.ctx)
	go rt.remoteCreate.Run(rt.ctx)

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
	if rt.stateStream != nil {
		rt.stateStream.Close()
	}
}

// Options returns the assembled server options after newRuntime has succeeded.
func (rt *Runtime) Options() *server.Options {
	return rt.opts
}

// refreshSessionsFunc is a narrow hook for the launch service and
// recover/rename/WS-teardown callers to request a prompt session-state
// refresh. The canonical catalog/reconciler is the single source of truth
// and already republishes on its own cadence (see runDaemonRefresh), so this
// is currently a no-op; it is kept as a call site so callers do not need to
// change if an explicit fast-path refresh is added later.
func (rt *Runtime) refreshSessionsFunc() {}

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

// refreshDaemonState runs one classify-then-publish cycle. Crash detection
// happens first so a crashed session is never broadcast as live in the same
// cycle. Publishing itself is owned entirely by the canonical reconciler
// (pkg/state.Reconciler), driven off the same daemon registry.
func (rt *Runtime) refreshDaemonState() {
	rt.classifyAndCleanupCrashes()
	rt.adapter.refresh()
	// Refresh the enricher's runtime metadata cache (cwd, pid, command) on the
	// same cadence as the daemon adapter snapshot. This is the only place
	// that performs the blocking /proc/<pid>/cwd read; Enrich itself only does
	// an in-memory map lookup, keeping catalog projection off the I/O path.
	rt.enricher.refreshRuntimeCache()
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

func defaultSessionDir() string {
	if dir := os.Getenv("TERMYARD_SESSION_DIR"); dir != "" {
		return dir
	}
	uid := fmt.Sprintf("%d", os.Getuid())
	return fmt.Sprintf("/tmp/termyard-sessions-%s", uid)
}

// runtimeEnricher supplies live runtime fields for catalog records
// without mutating persisted state.
type runtimeEnricher struct {
	adapter    *daemonAdapter
	actTracker *activity.Tracker

	previewMu    sync.Mutex
	previewCache map[string]*previewCacheEntry

	runtimeMu    sync.RWMutex
	runtimeCache map[string]runtimeCacheEntry
}

// runtimeCacheEntry holds the process-derived fields for one session
// (daemon adapter snapshot fields plus the live /proc/<pid>/cwd read) as of
// the last background refresh. Consumers of this cache may see values that
// are up to runtimeCacheInterval stale; this mirrors the accepted staleness
// of the throttled prompt-preview cache above and is a deliberate trade for
// keeping catalog projection free of blocking I/O.
type runtimeCacheEntry struct {
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

// previewCacheEntry holds a throttled prompt-preview snapshot for a single
// session so the (up to) 64KiB PTY capture only runs periodically instead of
// on every catalog enrichment call.
type previewCacheEntry struct {
	preview     string
	lastAttempt time.Time
	inFlight    bool
}

// promptPreviewInterval throttles prompt-preview PTY captures during
// catalog enrichment.
const promptPreviewInterval = 30 * time.Second

// previewFor returns the cached prompt preview for a session immediately,
// without blocking the catalog projection path. When the cached preview is
// stale (older than promptPreviewInterval) and no refresh is already in
// flight for this session, it kicks off exactly one asynchronous PTY capture
// to refresh the cache for subsequent calls. This keeps catalog snapshot
// publication off the PTY I/O path entirely.
func (e *runtimeEnricher) previewFor(sessionID string) string {
	e.previewMu.Lock()
	if e.previewCache == nil {
		e.previewCache = make(map[string]*previewCacheEntry)
	}
	entry := e.previewCache[sessionID]
	if entry == nil {
		entry = &previewCacheEntry{}
		e.previewCache[sessionID] = entry
	}
	cached := entry.preview
	due := time.Since(entry.lastAttempt) >= promptPreviewInterval
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
func (e *runtimeEnricher) refreshPreview(sessionID string) {
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
func (e *runtimeEnricher) refreshRuntimeCache() {
	infos := e.adapter.List()
	next := make(map[string]runtimeCacheEntry, len(infos))
	for _, d := range infos {
		entry := runtimeCacheEntry{
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
func (e *runtimeEnricher) cachedRuntime(id string) (runtimeCacheEntry, bool) {
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
func (e *runtimeEnricher) Enrich(ref state.SessionRef, rec state.LocalSessionRecord) state.SessionRuntime {
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
