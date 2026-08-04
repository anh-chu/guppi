package server

import (
	"errors"
	"fmt"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/groupsync"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/portforward"
	"github.com/anh-chu/termyard/pkg/preferences"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/scheduler"
	"github.com/anh-chu/termyard/pkg/sessionattrs"
	"github.com/anh-chu/termyard/pkg/sessionlaunch"
	"github.com/anh-chu/termyard/pkg/sessionorder"
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
	StateMgr        *state.Manager
	Tracker         *toolevents.Tracker
	ActivityTracker *activity.Tracker
	Detector        *toolevents.Detector
	RefreshSessions func()              // triggers daemon state refresh
	OnDaemonOutput  func(paneID string) // called on PTY output for daemon sessions (silence monitor)
	CWDResolver     toolevents.CWDResolver

	// Session attributes / ordering / grouping
	AttrsStore *sessionattrs.Store
	OrderStore *sessionorder.Store
	GroupStore *groupsync.Store

	// Peer
	Identity       *identity.Identity
	PeerStore      *identity.PeerStore
	PeerMgr        *peer.Manager
	PeerHandler    *peer.Handler
	StreamReg      *peer.StreamRegistry
	CaptureReg     *peer.CaptureRegistry
	FileReadReg    *peer.FileReadRegistry
	LinkSupervisor *peer.LinkSupervisor

	// Launch / registry
	Launch        *sessionlaunch.Service
	DaemonReg     *pty.Registry
	Hub           *ws.Hub
	V2CommandSvc  *state.SessionCommandService
	V2Catalog     *state.Catalog
	V2StateStream *ws.StateStreamHub

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

	if o.SchedulerStore != nil && o.Launch == nil {
		errs = append(errs, errors.New("SchedulerStore configured but Launch is nil"))
	}

	if o.SchedulerRunner != nil && o.SchedulerStore == nil {
		errs = append(errs, errors.New("SchedulerRunner configured but SchedulerStore is nil"))
	}

	if o.PortForwardStore != nil && (o.Port < 1 || o.Port > 65535) {
		errs = append(errs, fmt.Errorf("PortForwardStore configured but Port %d is invalid", o.Port))
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
