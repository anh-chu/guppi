package server

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/common"
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
	"github.com/anh-chu/termyard/pkg/sessionorder"
	"github.com/anh-chu/termyard/pkg/state"
	"github.com/anh-chu/termyard/pkg/toolevents"
	"github.com/anh-chu/termyard/pkg/webpush"
	"github.com/anh-chu/termyard/pkg/wikilite"
)

func Execute(ctx context.Context, c *cli.Command) error {
	stateMgr := state.NewManager()
	tracker := toolevents.NewTracker()
	tracker.EnablePersistence()
	actTracker := activity.NewTracker()

	// Session daemon registry -- the only session backend.
	daemonReg := pty.NewRegistry(defaultSessionDir())

	// Wire up durable lifecycle store for crash detection and recovery.
	if lcStore, err := pty.NewLifecycleStore(pty.DefaultStateDir()); err != nil {
		logrus.WithError(err).Warn("failed to create lifecycle store -- crash recovery disabled")
	} else {
		daemonReg.SetLifecycleStore(lcStore)
		// Detect any sessions that crashed while the server was down.
		if crashed := daemonReg.DetectAndCleanupCrashes(); len(crashed) > 0 {
			logrus.WithField("count", len(crashed)).Warn("detected crashed sessions from previous run")
		}
	}

	stateMgr.SetDaemonRegistry(&daemonRegAdapter{reg: daemonReg})

	// refreshSessions discovers daemon sessions and pushes state.
	refreshSessions := func() {
		var sessions []*model.Session
		for _, d := range daemonReg.List() {
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
						ID:          "daemon-" + d.ID + "-0",
						Active:      true,
						CurrentPath: d.Cwd,
					}},
				}},
			})
		}
		stateMgr.UpdateSessions(sessions)

		// Run crash detection on each refresh cycle.
		if lcStore := daemonReg.LifecycleStore(); lcStore != nil {
			if crashed := daemonReg.DetectAndCleanupCrashes(); len(crashed) > 0 {
				logrus.WithField("count", len(crashed)).Warn("detected newly crashed sessions")
			}
		}
	}

	// Poll daemon sessions every 2 seconds.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refreshSessions()
			}
		}
	}()

	reconciler := toolevents.NewReconciler(tracker, func(paneID string) toolevents.PaneState {
		if idx := strings.Index(paneID, ":0.0"); idx > 0 {
			name := paneID[:idx]
			for _, d := range daemonReg.List() {
				if d.ID == name {
					pid := d.ShellPid
					if pid == 0 {
						pid = d.Pid
					}
					return toolevents.PaneState{Exists: true, CurrentCommand: d.Shell, PID: pid}
				}
			}
		}
		return toolevents.PaneState{Exists: false}
	}, 3*time.Second)
	go reconciler.Run(ctx)

	detector := toolevents.NewDetector(tracker, func() []toolevents.PaneInfo {
		var infos []toolevents.PaneInfo
		for _, d := range daemonReg.List() {
			pid := d.ShellPid
			if pid == 0 {
				pid = d.Pid
			}
			infos = append(infos, toolevents.PaneInfo{
				PaneID:  d.ID + ":0.0",
				Session: d.ID,
				Window:  0,
				PID:     pid,
			})
		}
		return infos
	}, 5*time.Second)
	go detector.Run(ctx)

	captureClient := &daemonCaptureClient{daemon: daemonReg}
	silenceMonitor := toolevents.NewSilenceMonitor(tracker, detector, captureClient)
	go silenceMonitor.Run(ctx)

	go runShellNameWatcher(ctx, stateMgr, daemonReg)

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

	go tracker.RunInactivityPromoter(ctx, toolevents.DefaultInactivityTimeout)

	// Stuck monitor: flag agents that claim "active" but show no progress.
	checkPrompt := func(paneID string) (bool, bool) {
		if idx := strings.Index(paneID, ":0.0"); idx > 0 {
			name := paneID[:idx]
			if text, err := daemonReg.Capture(name); err == nil {
				return toolevents.DetectPrompt(text).IsPrompt, true
			}
		}
		return false, false
	}
	go tracker.RunStuckMonitor(ctx, toolevents.DefaultStuckTimeout, checkPrompt)

	prefStore, err := preferences.NewStore()
	if err != nil {
		logrus.WithError(err).Warn("failed to load preferences, using defaults")
		prefStore = nil
	}

	// applyNamerFromPrefs (re)builds the AI session namer from preferences,
	// falling back to env vars. Called at startup and whenever preferences are
	// updated via the API.
	applyNamerFromPrefs := func(p *preferences.Preferences) {
		cfg := namer.Configure(p.AINaming.Enabled, p.AINaming.Endpoint, p.AINaming.APIKey, p.AINaming.Model)
		n := namer.New(cfg)
		stateMgr.SetNamer(n)
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

	var pushKeys *webpush.VAPIDKeys
	var pushStore *webpush.Store
	vapidKeys, err := webpush.LoadOrCreateKeys()
	if err != nil {
		logrus.WithError(err).Warn("failed to load VAPID keys, push notifications will be unavailable")
	} else {
		pushKeys = vapidKeys
		pushStore = webpush.NewStore()
		pushSender := webpush.NewSender(pushKeys, pushStore, tracker)
		go pushSender.Run(ctx)
	}

	var (
		authEnabled   bool
		passwordStore *auth.PasswordStore
		sessionMgr    *auth.SessionManager
		authLimiter   *auth.Limiter
		notifyToken   string
	)
	if !c.Bool("no-auth") {
		passwordStore, err = auth.NewPasswordStore()
		if err != nil {
			return fmt.Errorf("failed to initialize auth: %w", err)
		}
		sessionMgr = auth.NewSessionManager(24 * time.Hour)
		authEnabled = true
		authLimiter = auth.NewLimiter()

		notifyToken, err = auth.LoadOrCreateNotifyToken()
		if err != nil {
			logrus.WithError(err).Warn("failed to load notify token; TCP notify fallback unavailable")
		}

		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					sessionMgr.Cleanup()
				}
			}
		}()

		if !passwordStore.HasPassword() {
			logrus.Info("no password set -- open the dashboard in your browser to complete setup")
		}
	}

	hostname, _ := os.Hostname()
	nodeIdentity, err := identity.LoadOrCreate(hostname)
	if err != nil {
		return fmt.Errorf("failed to load identity: %w", err)
	}
	logrus.WithField("name", nodeIdentity.Name).WithField("fingerprint", nodeIdentity.Fingerprint()).Info("node identity loaded")

	peerStore, err := identity.NewPeerStore()
	if err != nil {
		return fmt.Errorf("failed to load peer store: %w", err)
	}

	peerMgr := peer.NewManager(nodeIdentity, peerStore, stateMgr)
	go peerMgr.Run()

	detector.SetHost(peerMgr.LocalID(), peerMgr.LocalName())
	silenceMonitor.SetHost(peerMgr.LocalID(), peerMgr.LocalName())
	reconciler.SetHost(peerMgr.LocalID(), peerMgr.LocalName())

	streamReg := peer.NewStreamRegistry()
	captureReg := peer.NewCaptureRegistry()
	fileReadReg := peer.NewFileReadRegistry()

	deps := peer.SessionDeps{
		Manager:     peerMgr,
		LocalMgr:    stateMgr,
		Identity:    nodeIdentity,
		ActTracker:  actTracker,
		ToolTracker: tracker,
		PeerStore:   peerStore,
		DaemonReg:   &peerDaemonAdapter{reg: daemonReg},
		StreamReg:   streamReg,
		CaptureReg:  captureReg,
		FileReadReg: fileReadReg,
	}

	peerHandler := peer.NewHandler(deps, streamReg)

	supervisor := peer.NewLinkSupervisor(deps)
	supervisor.Start(ctx)

	wikiSup := wikilite.NewSupervisor()
	wikiSup.Start(ctx)

	opts := &server.Options{
		Port:             int(c.Int("port")),
		SocketPath:       c.String("socket"),
		TLSCert:          c.String("tls-cert"),
		TLSKey:           c.String("tls-key"),
		TLSAuto:          c.Bool("tls"),
		StateMgr:         stateMgr,
		Tracker:          tracker,
		ActivityTracker:  actTracker,
		PushKeys:         pushKeys,
		PushStore:        pushStore,
		PrefStore:        prefStore,
		AttrsStore:       attrsStore,
		OrderStore:       orderStore,
		GroupStore:       groupStore,
		AuthEnabled:      authEnabled,
		DebugPprof:       c.Bool("debug-pprof"),
		PasswordStore:    passwordStore,
		SessionMgr:       sessionMgr,
		AuthLimiter:      authLimiter,
		NotifyToken:      notifyToken,
		Identity:         nodeIdentity,
		PeerStore:        peerStore,
		PeerMgr:          peerMgr,
		PeerHandler:      peerHandler,
		StreamReg:        streamReg,
		CaptureReg:       captureReg,
		FileReadReg:      fileReadReg,
		LinkSupervisor:   supervisor,
		Detector:         detector,
		PortForwardStore: portforward.NewStore(),
		SchedulerStore:   schedulerStore,
		WikiLite:         wikiSup,
		DaemonReg:        daemonReg,
		CWDResolver:      &daemonCWDResolver{reg: daemonReg},
		OnDaemonOutput: func(paneID string) {
			silenceMonitor.RecordOutput(paneID)
		},
		RefreshSessions: refreshSessions,
		OnPrefsChanged:  applyNamerFromPrefs,
	}
	if schedulerStore != nil {
		runner := scheduler.NewRunner(schedulerStore, stateMgr, peerMgr, func(req scheduler.CreateSessionReq) error {
			// Remote sessions still go through peer path.
			if req.Host != "" && peerMgr != nil && !peerMgr.IsLocal(req.Host) {
				return server.CreateSession(opts, req)
			}
			// Local sessions use daemon backend.
			shell := req.Command
			if shell == "" || shell == "shell" {
				shell = ""
			}
			cwd := req.Path
			if cwd == "~" {
				cwd = ""
			}
			if err := daemonReg.Create(req.Name, shell, cwd, 120, 40); err != nil {
				return err
			}
			if req.AgentType != "" {
				stateMgr.SetSessionAgentType(req.Name, req.AgentType)
			}
			refreshSessions()
			return nil
		}, logrus.WithField("component", "scheduler"))
		runner.SetCapEnforcer(func(job scheduler.Job) {
			// Pre-spawn: leave room for the incoming run.
			server.EnforceScheduleCap(opts, job.ID, job.MaxConcurrency-1)
		})
		opts.SchedulerRunner = runner
		go func() {
			for opts.Hub == nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
			runner.Run(ctx)
		}()
	}

	return server.Run(ctx, opts)
}

func init() {
	flags := []cli.Flag{
		&cli.IntFlag{
			Name:    "port",
			Aliases: []string{"p"},
			Usage:   "HTTP server port",
			Sources: cli.EnvVars("TERMYARD_PORT"),
			Value:   7654,
		},

		&cli.StringFlag{
			Name:    "socket",
			Usage:   "Unix socket path for local notify CLI (auto-detected if omitted)",
			Sources: cli.EnvVars("TERMYARD_SOCKET"),
		},
		&cli.BoolFlag{
			Name:    "no-auth",
			Usage:   "Disable authentication (not recommended for remote access)",
			Sources: cli.EnvVars("TERMYARD_NO_AUTH"),
		},

		&cli.BoolFlag{
			Name:    "tls",
			Usage:   "Serve HTTPS with a self-signed cert (enables secure-context browser features over LAN)",
			Sources: cli.EnvVars("TERMYARD_TLS"),
		},
		&cli.StringFlag{
			Name:    "tls-cert",
			Usage:   "Path to a TLS certificate file (PEM); pair with --tls-key for a real cert",
			Sources: cli.EnvVars("TERMYARD_TLS_CERT"),
		},
		&cli.StringFlag{
			Name:    "tls-key",
			Usage:   "Path to a TLS private key file (PEM); pair with --tls-cert",
			Sources: cli.EnvVars("TERMYARD_TLS_KEY"),
		},
		&cli.BoolFlag{
			Name:    "debug-pprof",
			Usage:   "Mount /debug/pprof behind auth and loopback (off by default)",
			Sources: cli.EnvVars("TERMYARD_DEBUG_PPROF"),
		},
	}

	cmd := &cli.Command{
		Name:        "server",
		Usage:       "start the termyard web server",
		Description: "starts the web dashboard for monitoring and interacting with coding agent sessions",
		Flags:       flags,
		Action:      Execute,
	}

	common.RegisterCommand(cmd)
}

// shellNames lists foreground commands treated as "no meaningful process".
var shellNames = map[string]bool{
	"bash": true, "zsh": true, "sh": true, "fish": true, "tmux": true,
	"-bash": true, "-zsh": true, "-sh": true, "login": true,
}

// trivialCmds are short-lived navigation/inspection commands that should never
// drive a session rename on their own -- they say nothing durable about the
// session's purpose.
var trivialCmds = map[string]bool{
	"ls": true, "cd": true, "pwd": true, "cat": true, "less": true,
	"more": true, "man": true, "clear": true, "echo": true, "which": true,
	"sleep": true, "watch": true, "top": true, "htop": true, "ps": true,
	"history": true, "env": true, "export": true, "head": true, "tail": true,
	"touch": true, "mkdir": true, "rm": true, "cp": true, "mv": true,
}

// runShellNameWatcher polls active-pane foreground commands and, when a
// non-agent session starts a new meaningful process, asks the AI namer to
// refresh that session's display name. The actual eligibility gate (namer
// enabled, no agent, not user-set) lives in TriggerShellNaming.
func runShellNameWatcher(ctx context.Context, mgr *state.Manager, daemonReg *pty.Registry) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	lastCmd := make(map[string]string)
	lastFire := make(map[string]time.Time)
	named := make(map[string]bool)
	// First name is assigned quickly; after a session already has a name we back
	// off hard so a meaningful label is not churned by later transient commands.
	const firstInterval = 20 * time.Second
	const renameInterval = 3 * time.Minute

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var fgs []model.SessionForeground
			if daemonReg != nil {
				for _, d := range daemonReg.List() {
					pid := d.ShellPid
					if pid == 0 {
						pid = d.Pid
					}
					if cmd := foregroundCommand(pid); cmd != "" {
						fgs = append(fgs, model.SessionForeground{
							Session: d.ID,
							Command: cmd,
							PID:     pid,
						})
					}
				}
			}
			for _, fg := range fgs {
				prev := lastCmd[fg.Session]
				lastCmd[fg.Session] = fg.Command
				cmd := strings.TrimSpace(fg.Command)
				if cmd == "" || shellNames[cmd] || trivialCmds[cmd] || cmd == prev {
					continue
				}
				// New meaningful foreground process detected. Use a long cooldown
				// once the session already has a name so we don't churn it.
				interval := firstInterval
				if named[fg.Session] {
					interval = renameInterval
				}
				if t, ok := lastFire[fg.Session]; ok && time.Since(t) < interval {
					continue
				}
				lastFire[fg.Session] = time.Now()
				named[fg.Session] = true

				cmds := []string{cmd}
				if daemonReg != nil {
					if content, err := daemonReg.Capture(fg.Session); err == nil {
						cmds = recentCommands(content, cmd)
					}
				}
				go mgr.TriggerShellNaming(fg.Session, cmds)
			}
		}
	}
}

// recentCommands extracts up to a handful of recent input lines from captured
// pane content as a hint for naming. Falls back to [foreground] if nothing
// useful is found.
func defaultSessionDir() string {
	if dir := os.Getenv("TERMYARD_SESSION_DIR"); dir != "" {
		return dir
	}
	uid := fmt.Sprintf("%d", os.Getuid())
	return fmt.Sprintf("/tmp/termyard-sessions-%s", uid)
}

func recentCommands(content, foreground string) []string {
	lines := strings.Split(content, "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < 6; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		// Strip common prompt prefixes ($, #, %, >).
		l = strings.TrimLeft(l, "$#%> ")
		if l == "" {
			continue
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return []string{foreground}
	}
	// reverse to chronological order
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// daemonCaptureClient routes pane capture to the daemon registry.
type daemonCaptureClient struct {
	daemon *pty.Registry
}

func (c *daemonCaptureClient) CapturePaneContent(paneID string) (string, error) {
	if idx := strings.Index(paneID, ":0.0"); idx > 0 {
		return c.daemon.Capture(paneID[:idx])
	}
	return c.daemon.Capture(paneID)
}

// peerDaemonAdapter wraps *pty.Registry to satisfy peer.DaemonRegistry.
type peerDaemonAdapter struct {
	reg *pty.Registry
}

func (a *peerDaemonAdapter) Create(name, shell, cwd string, cols, rows uint16) error {
	return a.reg.Create(name, shell, cwd, cols, rows)
}
func (a *peerDaemonAdapter) Kill(name string) error              { return a.reg.Kill(name) }
func (a *peerDaemonAdapter) Capture(name string) (string, error) { return a.reg.Capture(name) }
func (a *peerDaemonAdapter) SocketPath(name string) string       { return a.reg.SocketPath(name) }
func (a *peerDaemonAdapter) List() []peer.DaemonSessionInfo {
	infos := a.reg.List()
	out := make([]peer.DaemonSessionInfo, len(infos))
	for i, info := range infos {
		out[i] = peer.DaemonSessionInfo{
			ID:       info.ID,
			Pid:      info.Pid,
			ShellPid: info.ShellPid,
			Shell:    info.Shell,
			Cwd:      info.Cwd,
			Created:  info.Created,
		}
	}
	return out
}

// daemonCWDResolver satisfies toolevents.CWDResolver for daemon sessions.
type daemonCWDResolver struct {
	reg *pty.Registry
}

func (r *daemonCWDResolver) SessionCWD(session string) string {
	for _, d := range r.reg.List() {
		if d.ID == session {
			return d.Cwd
		}
	}
	return ""
}

// foregroundCommand returns the name of the foreground process running under
// the given shell PID, or "" if the shell has no children / is idle.
func foregroundCommand(shellPid int) string {
	childrenPath := fmt.Sprintf("/proc/%d/task/%d/children", shellPid, shellPid)
	data, err := os.ReadFile(childrenPath)
	if err != nil {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) == 0 {
		return "" // shell is idle, no foreground process
	}
	// Use the first child (most likely the foreground process).
	childPid := fields[0]
	commPath := fmt.Sprintf("/proc/%s/comm", childPid)
	comm, err := os.ReadFile(commPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(comm))
}

// daemonRegAdapter wraps pty.Registry to satisfy state.DaemonRegistry.
type daemonRegAdapter struct {
	reg *pty.Registry
}

func (a *daemonRegAdapter) List() []state.DaemonSessionInfo {
	infos := a.reg.List()
	out := make([]state.DaemonSessionInfo, len(infos))
	for i, info := range infos {
		out[i] = state.DaemonSessionInfo{
			ID:       info.ID,
			Pid:      info.Pid,
			ShellPid: info.ShellPid,
			Shell:    info.Shell,
			Cwd:      info.Cwd,
			Created:  info.Created,
		}
	}
	return out
}

func (a *daemonRegAdapter) Capture(name string) (string, error) {
	return a.reg.Capture(name)
}

func (a *daemonRegAdapter) CaptureTail(name string, maxBytes int) (string, error) {
	return a.reg.CaptureTail(name, maxBytes)
}

func (a *daemonRegAdapter) IsSessionDead(name string) bool {
	return a.reg.IsSessionDead(name)
}

func (a *daemonRegAdapter) CrashedSessions() []state.CrashedSessionInfo {
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
