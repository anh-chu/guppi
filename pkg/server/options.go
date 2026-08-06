package server

import (
	"errors"
	"fmt"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/portforward"
	"github.com/anh-chu/termyard/pkg/preferences"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/scheduler"
	"github.com/anh-chu/termyard/pkg/state"
	"github.com/anh-chu/termyard/pkg/toolevents"
	"github.com/anh-chu/termyard/pkg/webpush"
	"github.com/anh-chu/termyard/pkg/wikilite"
	"github.com/anh-chu/termyard/pkg/ws"
)

// Options configures the termyard HTTP/WebSocket server.
//
// The struct is intentionally flat so existing callers using keyed field
// literals keep working. Fields are documented below in cohesive dependency
// groups.
//
// There is exactly one state authority: Catalog/CommandSvc/StateStream. There
// is no alternate session store or runtime mode switch -- the canonical
// graph is the only one that exists.
type Options struct {
	// Network/transport
	Port       int
	SocketPath string
	TLSCert    string
	TLSKey     string
	TLSAuto    bool

	// Auth
	AuthEnabled   bool
	DebugPprof    bool
	PasswordStore *auth.PasswordStore
	SessionMgr    *auth.SessionManager
	AuthLimiter   *auth.Limiter
	NotifyToken   string

	// State / activity
	Tracker         *toolevents.Tracker
	ActivityTracker *activity.Tracker
	Detector        *toolevents.Detector
	RefreshSessions func()              // triggers daemon state refresh
	OnDaemonOutput  func(paneID string) // called on PTY output for daemon sessions (silence monitor)
	CWDResolver     toolevents.CWDResolver

	// Peer
	Identity       *identity.Identity
	PeerStore      *identity.PeerStore
	PeerMgr        *peer.Manager
	PeerHandler    *peer.Handler
	StreamReg      *peer.StreamRegistry
	CaptureReg     *peer.CaptureRegistry
	FileReadReg    *peer.FileReadRegistry
	LinkSupervisor *peer.LinkSupervisor

	// Registry / canonical state
	DaemonReg   *pty.Registry
	Hub         *ws.Hub
	Catalog     *state.Catalog
	CommandSvc  *state.SessionCommandService
	StateStream *ws.StateStreamHub

	// Push notifications / media
	PushKeys  *webpush.VAPIDKeys
	PushStore *webpush.Store

	// Preferences / callbacks
	PrefStore      *preferences.Store
	OnPrefsChanged func(*preferences.Preferences)

	// Scheduler
	SchedulerStore  *scheduler.Store
	SchedulerRunner *scheduler.Runner

	// Wiki
	WikiLite *wikilite.Supervisor

	// Port forwarding
	PortForwardStore *portforward.Store
}

// Validate checks that mutually-required option groups are consistent.
// It returns an aggregate error describing all missing dependencies.
func (o *Options) Validate() error {
	if o == nil {
		return errors.New("server options are nil")
	}

	var errs []error

	if o.AuthEnabled {
		if o.PasswordStore == nil {
			errs = append(errs, errors.New("auth enabled but PasswordStore is nil"))
		}
		if o.SessionMgr == nil {
			errs = append(errs, errors.New("auth enabled but SessionMgr is nil"))
		}
	}

	if o.PeerMgr != nil {
		if o.Identity == nil {
			errs = append(errs, errors.New("PeerMgr configured but Identity is nil"))
		}
		if o.PeerStore == nil {
			errs = append(errs, errors.New("PeerMgr configured but PeerStore is nil"))
		}
		if o.LinkSupervisor == nil {
			errs = append(errs, errors.New("PeerMgr configured but LinkSupervisor is nil"))
		}
	}

	if o.SchedulerStore != nil && o.SchedulerRunner == nil {
		errs = append(errs, errors.New("SchedulerStore configured but SchedulerRunner is nil"))
	}

	if o.SchedulerRunner != nil && o.SchedulerStore == nil {
		errs = append(errs, errors.New("SchedulerRunner configured but SchedulerStore is nil"))
	}

	if o.PortForwardStore != nil && (o.Port < 1 || o.Port > 65535) {
		errs = append(errs, fmt.Errorf("PortForwardStore configured but Port %d is invalid", o.Port))
	}

	// Canonical state graph is required: there is no legacy fallback to run
	// the server without it.
	if o.Catalog == nil {
		errs = append(errs, errors.New("Catalog is required (no legacy state fallback exists)"))
	}
	if o.CommandSvc == nil {
		errs = append(errs, errors.New("CommandSvc is required (no legacy state fallback exists)"))
	}
	if o.StateStream == nil {
		errs = append(errs, errors.New("StateStream is required (no legacy state fallback exists)"))
	}

	if len(errs) == 1 {
		return errs[0]
	}
	if len(errs) > 1 {
		out := "server options validation failed:"
		for _, e := range errs {
			out += "\n- " + e.Error()
		}
		return errors.New(out)
	}
	return nil
}
