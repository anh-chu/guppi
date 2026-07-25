package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"
	"github.com/robfig/cron/v3"
	"github.com/sirupsen/logrus"

	wp "github.com/SherClockHolmes/webpush-go"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/agentcheck"
	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/common"
	"github.com/anh-chu/termyard/pkg/git"
	"github.com/anh-chu/termyard/pkg/groupsync"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/namer"
	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/portforward"
	"github.com/anh-chu/termyard/pkg/preferences"
	"github.com/anh-chu/termyard/pkg/pty"
	"github.com/anh-chu/termyard/pkg/scheduler"
	"github.com/anh-chu/termyard/pkg/sessionattrs"
	"github.com/anh-chu/termyard/pkg/sessionorder"
	"github.com/anh-chu/termyard/pkg/socket"
	"github.com/anh-chu/termyard/pkg/state"
	"github.com/anh-chu/termyard/pkg/stats"
	"github.com/anh-chu/termyard/pkg/toolevents"
	"github.com/anh-chu/termyard/pkg/webpush"
	"github.com/anh-chu/termyard/pkg/ws"
)

type Options struct {
	Port       int
	SocketPath string
	TLSCert    string
	TLSKey     string
	TLSAuto    bool

	StateMgr         *state.Manager
	Tracker          *toolevents.Tracker
	ActivityTracker  *activity.Tracker
	PushKeys         *webpush.VAPIDKeys
	PushStore        *webpush.Store
	PrefStore        *preferences.Store
	OnPrefsChanged   func(*preferences.Preferences)
	AttrsStore       *sessionattrs.Store
	OrderStore       *sessionorder.Store
	GroupStore       *groupsync.Store
	AuthEnabled      bool
	PasswordStore    *auth.PasswordStore
	SessionMgr       *auth.SessionManager
	Identity         *identity.Identity
	PeerStore        *identity.PeerStore
	PeerMgr          *peer.Manager
	PeerHandler      *peer.Handler
	StreamReg        *peer.StreamRegistry
	CaptureReg       *peer.CaptureRegistry
	FileReadReg      *peer.FileReadRegistry
	LinkSupervisor   *peer.LinkSupervisor
	Detector         *toolevents.Detector
	PortForwardStore *portforward.Store
	SchedulerStore   *scheduler.Store
	SchedulerRunner  *scheduler.Runner
	DaemonReg        *pty.Registry
	CWDResolver      toolevents.CWDResolver
	RefreshSessions  func()              // triggers daemon state refresh
	OnDaemonOutput   func(paneID string) // called on PTY output for daemon sessions (silence monitor)
	Hub              *ws.Hub
}

// attrsStoreAdapter bridges sessionattrs.Store to the narrow SessionAttrsSink
// interface the peer package consumes.
type attrsStoreAdapter struct {
	store *sessionattrs.Store
}

func (a attrsStoreAdapter) ApplyRemoteDelta(key string, background, hidden bool, scheduleID string, updatedAt time.Time) (bool, error) {
	_, accepted, err := a.store.ApplyRemote(key, sessionattrs.Attr{
		Background: background, Hidden: hidden, ScheduleID: scheduleID, UpdatedAt: updatedAt,
	})
	return accepted, err
}

func (a attrsStoreAdapter) ApplyRemoteSnapshot(attrs map[string]peer.SessionAttr) ([]string, error) {
	conv := make(map[string]sessionattrs.Attr, len(attrs))
	for k, v := range attrs {
		conv[k] = sessionattrs.Attr{Background: v.Background, Hidden: v.Hidden, ScheduleID: v.ScheduleID, UpdatedAt: v.UpdatedAt}
	}
	return a.store.ApplySnapshot(conv)
}

func (a attrsStoreAdapter) SnapshotAttrs() map[string]peer.SessionAttr {
	snap := a.store.Snapshot()
	out := make(map[string]peer.SessionAttr, len(snap))
	for k, v := range snap {
		out[k] = peer.SessionAttr{Background: v.Background, Hidden: v.Hidden, ScheduleID: v.ScheduleID, UpdatedAt: v.UpdatedAt}
	}
	return out
}

func (a attrsStoreAdapter) SetScheduleID(key, scheduleID string) error {
	_, err := a.store.SetScheduleID(key, scheduleID)
	return err
}

// sessionOrderStoreAdapter bridges sessionorder.Store to the peer narrow sink.
type sessionOrderStoreAdapter struct {
	store *sessionorder.Store
}

func (a sessionOrderStoreAdapter) ApplyRemoteDelta(key, rank string, updatedAt time.Time) (bool, error) {
	_, accepted, err := a.store.ApplyRemote(key, sessionorder.Order{Rank: rank, UpdatedAt: updatedAt})
	return accepted, err
}

func (a sessionOrderStoreAdapter) ApplyRemoteSnapshot(orders map[string]peer.SessionOrder) ([]string, error) {
	conv := make(map[string]sessionorder.Order, len(orders))
	for k, v := range orders {
		conv[k] = sessionorder.Order{Rank: v.Rank, UpdatedAt: v.UpdatedAt}
	}
	return a.store.ApplySnapshot(conv)
}

func (a sessionOrderStoreAdapter) SnapshotOrders() map[string]peer.SessionOrder {
	snap := a.store.Snapshot()
	out := make(map[string]peer.SessionOrder, len(snap))
	for k, v := range snap {
		out[k] = peer.SessionOrder{Rank: v.Rank, UpdatedAt: v.UpdatedAt}
	}
	return out
}

// groupStoreAdapter bridges groupsync.Store to the peer narrow sink.
type groupStoreAdapter struct {
	store *groupsync.Store
}

func (a groupStoreAdapter) ApplyRemoteDelta(id string, group peer.Group) (bool, error) {
	_, accepted, err := a.store.ApplyRemote(id, groupsync.Group{
		Tree:          append(json.RawMessage(nil), group.Tree...),
		TreeUpdatedAt: group.TreeUpdatedAt,
		Name:          group.Name,
		NameUpdatedAt: group.NameUpdatedAt,
		Rank:          group.Rank,
		RankUpdatedAt: group.RankUpdatedAt,
		DeletedAt:     group.DeletedAt,
	})
	return accepted, err
}

func (a groupStoreAdapter) ApplyRemoteSnapshot(groups map[string]peer.Group) ([]string, error) {
	conv := make(map[string]groupsync.Group, len(groups))
	for id, g := range groups {
		conv[id] = groupsync.Group{
			Tree:          append(json.RawMessage(nil), g.Tree...),
			TreeUpdatedAt: g.TreeUpdatedAt,
			Name:          g.Name,
			NameUpdatedAt: g.NameUpdatedAt,
			Rank:          g.Rank,
			RankUpdatedAt: g.RankUpdatedAt,
			DeletedAt:     g.DeletedAt,
		}
	}
	return a.store.ApplySnapshot(conv)
}

func (a groupStoreAdapter) SnapshotGroups() map[string]peer.Group {
	snap := a.store.Snapshot()
	out := make(map[string]peer.Group, len(snap))
	for id, g := range snap {
		out[id] = peer.Group{
			Tree:          append(json.RawMessage(nil), g.Tree...),
			TreeUpdatedAt: g.TreeUpdatedAt,
			Name:          g.Name,
			NameUpdatedAt: g.NameUpdatedAt,
			Rank:          g.Rank,
			RankUpdatedAt: g.RankUpdatedAt,
			DeletedAt:     g.DeletedAt,
		}
	}
	return out
}

// pruneSessionAttrs garbage-collects session-attribute keys whose owning host
// is online but whose session is gone from the authoritative mesh list, and
// drops expired tombstones. Genuinely-gone keys are tombstoned and the removal
// is fanned out to peers + browsers. No-op when peer mode is unavailable
// (without the host list we can't prove a session is gone, so we keep keys).
func pruneSessionAttrs(opts *Options, hub *ws.Hub) {
	if opts.AttrsStore == nil || opts.PeerMgr == nil {
		return
	}
	sessions := opts.PeerMgr.GetAllSessions()
	liveKeys := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		// Global key mirrors frontend sessionKey(): "<host>/<name>".
		if s.Host != "" {
			liveKeys[s.Host+"/"+s.Name] = true
		} else {
			liveKeys[s.Name] = true
		}
	}
	online := map[string]bool{}
	for _, h := range opts.PeerMgr.GetHosts() {
		if h.Online {
			online[h.ID] = true
		}
	}
	gone, changed, err := opts.AttrsStore.Prune(liveKeys, online)
	if err != nil || !changed {
		return
	}
	for _, key := range gone {
		fanoutAttrsDeltaToPeers(opts, key, opts.AttrsStore.Get(key))
	}
	if hub != nil {
		hub.BroadcastJSON(map[string]interface{}{"type": "session-attrs-updated"})
	}
}

// fanoutAttrsDeltaToPeers broadcasts a single-key session-attribute delta to
// every connected paired peer over the control WS. Best-effort: a full Send
// queue drops the frame; the peer reconciles from the next snapshot on link-up.
func fanoutAttrsDeltaToPeers(opts *Options, key string, a sessionattrs.Attr) {
	if opts.PeerMgr == nil || opts.Identity == nil {
		return
	}
	msg, err := peer.NewMessage(peer.MsgSessionAttrsDelta, peer.SessionAttrsDeltaPayload{
		Origin: opts.Identity.Fingerprint(),
		Key:    key,
		Attr:   peer.SessionAttr{Background: a.Background, Hidden: a.Hidden, ScheduleID: a.ScheduleID, UpdatedAt: a.UpdatedAt},
	})
	if err != nil {
		return
	}
	for _, pc := range opts.PeerMgr.ConnectedPeers() {
		pc.Enqueue(msg)
	}
}

func fanoutOrderDeltaToPeers(opts *Options, key string, o sessionorder.Order) {
	if opts.PeerMgr == nil || opts.Identity == nil {
		return
	}
	msg, err := peer.NewMessage(peer.MsgSessionOrderDelta, peer.SessionOrderDeltaPayload{
		Origin: opts.Identity.Fingerprint(),
		Key:    key,
		Order:  peer.SessionOrder{Rank: o.Rank, UpdatedAt: o.UpdatedAt},
	})
	if err != nil {
		return
	}
	for _, pc := range opts.PeerMgr.ConnectedPeers() {
		pc.Enqueue(msg)
	}
}

func fanoutGroupDeltaToPeers(opts *Options, id string, g groupsync.Group) {
	if opts.PeerMgr == nil || opts.Identity == nil {
		return
	}
	msg, err := peer.NewMessage(peer.MsgGroupDelta, peer.GroupDeltaPayload{
		Origin: opts.Identity.Fingerprint(),
		ID:     id,
		Group: peer.Group{
			Tree:          append(json.RawMessage(nil), g.Tree...),
			TreeUpdatedAt: g.TreeUpdatedAt,
			Name:          g.Name,
			NameUpdatedAt: g.NameUpdatedAt,
			Rank:          g.Rank,
			RankUpdatedAt: g.RankUpdatedAt,
			DeletedAt:     g.DeletedAt,
		},
	})
	if err != nil {
		return
	}
	for _, pc := range opts.PeerMgr.ConnectedPeers() {
		pc.Enqueue(msg)
	}
}

func sessionKey(host, name string) string {
	if host != "" {
		return host + "/" + name
	}
	return name
}

// EnforceScheduleCap prunes the sessions owned by scheduleID until at most
// keep remain, killing oldest first (by creation time). For a pre-spawn
// call pass max-1 to leave room for the incoming run; for an update-time prune
// pass max. A negative keep is treated as unlimited and is a no-op.
func EnforceScheduleCap(opts *Options, scheduleID string, keep int) {
	if opts == nil || opts.AttrsStore == nil || keep < 0 || scheduleID == "" {
		return
	}
	keys := map[string]bool{}
	for key, sid := range opts.AttrsStore.Sets().ScheduleIDs {
		if sid == scheduleID {
			keys[key] = true
		}
	}
	if len(keys) == 0 {
		return
	}

	// Collect tagged sessions from daemon registry.
	var tagged []*model.Session
	if opts.DaemonReg != nil {
		for _, info := range opts.DaemonReg.List() {
			if keys[sessionKey("", info.ID)] {
				created := time.Time{}
				if t, err := time.Parse(time.RFC3339, info.Created); err == nil {
					created = t
				}
				tagged = append(tagged, &model.Session{
					Name:    info.ID,
					Created: created,
					Backend: "daemon",
				})
			}
		}
	}

	sort.Slice(tagged, func(i, j int) bool {
		return tagged[i].Created.Before(tagged[j].Created)
	})
	for len(tagged) > keep {
		victim := tagged[0]
		tagged = tagged[1:]
		if err := opts.DaemonReg.Kill(victim.Name); err != nil {
			logrus.WithError(err).WithField("session", victim.Name).Warn("schedule cap: kill daemon failed")
		}
	}
}

// CreateSession centralizes spawn logic for HTTP and scheduler fires.
func CreateSession(opts *Options, req scheduler.CreateSessionReq) error {
	req.Name = strings.TrimSpace(req.Name)
	req.Host = strings.TrimSpace(req.Host)
	req.Path = strings.TrimSpace(req.Path)
	req.Command = strings.TrimSpace(req.Command)
	if req.Name == "" {
		return fmt.Errorf("name is required")
	}
	if err := model.ValidateSessionName(req.Name); err != nil {
		return err
	}

	// Remote host — forward via peer connection. The peer handles worktree
	// creation locally so the worktree lands on the peer's filesystem, not ours.
	if req.Host != "" && opts.PeerMgr != nil && !opts.PeerMgr.IsLocal(req.Host) {
		peerConn := opts.PeerMgr.GetPeerConnection(req.Host)
		if peerConn == nil {
			return fmt.Errorf("peer not connected")
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
			return fmt.Errorf("peer send queue full")
		}
		if opts.AttrsStore != nil && req.ScheduleID != "" {
			key := sessionKey(req.Host, req.Name)
			if attr, err := opts.AttrsStore.SetScheduleID(key, req.ScheduleID); err != nil {
				logrus.WithError(err).Warn("failed to store schedule id")
			} else {
				fanoutAttrsDeltaToPeers(opts, key, attr)
				if opts.Hub != nil {
					opts.Hub.BroadcastJSON(map[string]interface{}{"type": "session-attrs-updated", "key": key})
				}
			}
		}
		return nil
	}

	// If a worktree branch is requested, create the linked worktree first and
	// redirect the session path to it.
	if req.WorktreeBranch != "" && req.Path != "" {
		expanded := req.Path
		if strings.HasPrefix(expanded, "~") {
			if home, err := os.UserHomeDir(); err == nil && home != "" {
				expanded = home + expanded[1:]
			}
		}
		// Resolve bare relative paths (e.g. "termyard") against the home dir.
		if !filepath.IsAbs(expanded) {
			if home, err := os.UserHomeDir(); err == nil {
				expanded = filepath.Join(home, expanded)
			}
		}
		sanitized := strings.ReplaceAll(req.WorktreeBranch, "/", "-")
		worktreesDir := filepath.Join(expanded, ".worktrees")
		if err := os.MkdirAll(worktreesDir, 0755); err != nil {
			return fmt.Errorf("mkdir .worktrees: %w", err)
		}
		destPath := filepath.Join(worktreesDir, sanitized)
		if err := git.CreateWorktree(expanded, req.WorktreeBranch, destPath); err != nil {
			return fmt.Errorf("git worktree add: %w", err)
		}
		req.Path = destPath
	}

	shell := req.Command
	if shell == "" || shell == "shell" {
		shell = ""
	}
	cwd := req.Path
	if cwd == "~" {
		cwd = ""
	}
	if err := opts.DaemonReg.Create(req.Name, shell, cwd, 120, 40); err != nil {
		return err
	}
	// Store explicit agent type before refresh so it survives inference.
	if req.AgentType != "" && opts.StateMgr != nil {
		opts.StateMgr.SetSessionAgentType(req.Name, req.AgentType)
	}
	if opts.AttrsStore != nil && req.ScheduleID != "" {
		key := sessionKey(req.Host, req.Name)
		if attr, err := opts.AttrsStore.SetScheduleID(key, req.ScheduleID); err != nil {
			logrus.WithError(err).Warn("failed to store schedule id")
		} else {
			fanoutAttrsDeltaToPeers(opts, key, attr)
			if opts.Hub != nil {
				opts.Hub.BroadcastJSON(map[string]interface{}{"type": "session-attrs-updated", "key": key})
			}
		}
	}
	// Trigger state refresh so WebSocket clients get notified.
	if opts.RefreshSessions != nil {
		opts.RefreshSessions()
	}
	return nil
}

// handleRemoteSession handles a terminal session request for a remote peer.
// It tells the peer to spawn a PTY, then bridges the browser WS to the peer's PTY WS.
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
		http.Error(w, "peer does not support per-stream terminal connections — upgrade the peer first", http.StatusUpgradeRequired)
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

// absPathRe matches HTML attribute values that begin with a single /
// (absolute paths), excluding protocol-relative URLs (//) and fragments.
// Group 1: the attribute prefix up through the opening quote.
// Group 2: the slash plus the first non-slash character of the path.
//
// This is compiled once at package init so handleProxy doesn't pay the cost
// of regexp compilation on every request.
var absPathRe = regexp.MustCompile(`((?:href|src|action|srcset|data-src|data-href)=")(/[^/])`)

// handleProxy reverse-proxies a request to a locally-bound port on the termyard
// host. WebSocket upgrade requests are tunnelled over raw TCP so that
// localhost-only dev servers remain accessible through the termyard URL.
//
// Route pattern: /proxy/{port}/{rest...}
func handleProxy(w http.ResponseWriter, r *http.Request, termyardPort int) {
	// Extract port from chi URL params
	portStr := chi.URLParam(r, "port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}
	if port == termyardPort {
		http.Error(w, "cannot proxy termyard's own port", http.StatusForbidden)
		return
	}

	// Strip "/proxy/{port}" prefix to get the downstream path
	rest := chi.URLParam(r, "*")
	if !strings.HasPrefix(rest, "/") {
		rest = "/" + rest
	}

	isWebSocket := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	if isWebSocket {
		proxyWebSocket(w, r, port, rest)
		return
	}

	target := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("127.0.0.1:%d", port),
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	// Override director to rewrite the path correctly
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = rest
		if r.URL.RawQuery != "" {
			req.URL.RawQuery = r.URL.RawQuery
		}
		req.Host = fmt.Sprintf("127.0.0.1:%d", port)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logrus.WithError(err).WithField("port", port).Debug("port forward proxy error")
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
	}
	proxy.ModifyResponse = makeHTMLRewriter(port)
	proxy.ServeHTTP(w, r)
}

// makeHTMLRewriter returns a ModifyResponse function that rewrites absolute
// paths in HTML responses so browsers route asset requests back through the
// termyard proxy rather than directly to the host root.
//
// For example, a Next.js app served at /proxy/8377/ generates:
//
//	<script src="/_next/static/chunks/main.js">
//
// which the browser resolves to devvm:7654/_next/... (a termyard 404). The
// rewriter turns it into src="/proxy/8377/_next/...", which routes correctly.
//
// It also patches the assetPrefix/basePath fields in Next.js __NEXT_DATA__ so
// that client-side navigation and code-splitting also use the proxy prefix.
func makeHTMLRewriter(port int) func(*http.Response) error {
	prefix := fmt.Sprintf("/proxy/%d", port)
	// Replacement: group1 stays, prefix is inserted before the leading slash of group2.
	// Example: href="/foo" → href="/proxy/8377/foo"
	// Example: href="/"   → href="/proxy/8377/"
	repl := []byte("${1}" + prefix + "${2}")

	// Next.js embeds {"assetPrefix":"","basePath":""} in __NEXT_DATA__.
	// Rewriting these makes the React runtime also use the proxy for all
	// dynamically loaded chunks and API calls.
	nextAssetReplace := []byte(`"assetPrefix":""`)
	nextAssetWith := []byte(`"assetPrefix":"` + prefix + `"`)
	nextBaseReplace := []byte(`"basePath":""`)
	nextBaseWith := []byte(`"basePath":"` + prefix + `"`)

	return func(resp *http.Response) error {
		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "text/html") {
			return nil
		}

		// Decompress gzip if needed so we can inspect the body bytes.
		var reader io.Reader = resp.Body
		encoded := strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip")
		if encoded {
			gr, err := gzip.NewReader(resp.Body)
			if err != nil {
				return nil // can't decompress — leave response untouched
			}
			defer gr.Close()
			reader = gr
		}

		body, err := io.ReadAll(reader)
		resp.Body.Close()
		if err != nil {
			return err
		}

		// Rewrite absolute-path attribute values.
		body = absPathRe.ReplaceAll(body, repl)

		// Patch Next.js runtime metadata.
		body = bytes.Replace(body, nextAssetReplace, nextAssetWith, -1)
		body = bytes.Replace(body, nextBaseReplace, nextBaseWith, -1)

		if encoded {
			// We decoded gzip; remove the header so the browser doesn't try to decode.
			resp.Header.Del("Content-Encoding")
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		return nil
	}
}

// proxyWebSocket tunnels a WebSocket upgrade through a raw TCP connection to
// the downstream port, allowing WebSocket-based dev servers to work through
// the termyard port-forward proxy.
func proxyWebSocket(w http.ResponseWriter, r *http.Request, port int, path string) {
	backend, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	if err != nil {
		logrus.WithError(err).WithField("port", port).Debug("ws port forward: dial failed")
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer backend.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		http.Error(w, "hijack failed", http.StatusInternalServerError)
		return
	}
	defer conn.Close()

	// Forward the original WS upgrade request to the backend with the rewritten path
	upgradeReq := r.Clone(r.Context())
	upgradeReq.URL.Path = path
	upgradeReq.URL.Host = fmt.Sprintf("127.0.0.1:%d", port)
	upgradeReq.Host = fmt.Sprintf("127.0.0.1:%d", port)
	if err := upgradeReq.Write(backend); err != nil {
		return
	}

	// Flush any buffered data from the hijacked connection to the backend
	if buf.Reader.Buffered() > 0 {
		buffered := make([]byte, buf.Reader.Buffered())
		_, _ = buf.Read(buffered)
		_, _ = backend.Write(buffered)
	}

	// Tunnel bidirectionally
	done := make(chan struct{}, 2)
	go func() { io.Copy(backend, conn); done <- struct{}{} }() //nolint:errcheck
	go func() { io.Copy(conn, backend); done <- struct{}{} }() //nolint:errcheck
	<-done
}

func Run(ctx context.Context, opts *Options) error {
	logger := logrus.WithField("component", "server")

	hub := setupHub(opts)
	wireSessionAttrsSync(opts, hub)

	r := chi.NewRouter()
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.StripSlashes)
	r.Use(chimiddleware.RequestID)
	// Diagnostic: live goroutine/heap profiles at /debug/pprof. Read-only, no
	// behavior change; used to pin where a wedged peer link's goroutines park.
	r.Mount("/debug", chimiddleware.Profiler())
	registerAPIRoutes(r, opts, hub)

	// WebSocket routes (protected by auth if enabled)
	go hub.Run()
	go runUpdateChecker(opts)

	registerWSRoutes(r, opts, hub)
	registerProxyRoutes(r, opts)
	if err := registerFrontendRoutes(r); err != nil {
		return err
	}

	return serveAndWait(ctx, opts, logger, r)
}

func setupHub(opts *Options) *ws.Hub {
	// Build the events hub up front so routes can broadcast layout changes.
	hub := ws.NewHub(opts.StateMgr, opts.Tracker)
	opts.Hub = hub
	var peerActivity ws.ActivitySource
	localHostID := ""
	if opts.PeerMgr != nil {
		peerActivity = opts.PeerMgr
		localHostID = opts.PeerMgr.LocalID()
	}
	if opts.ActivityTracker != nil || peerActivity != nil {
		hub.SetActivityTracker(opts.ActivityTracker, peerActivity, localHostID, false)
	}
	return hub
}

func wireSessionAttrsSync(opts *Options, hub *ws.Hub) {
	// Wire cross-machine sync. Peer subsystem applies inbound snapshots/deltas to
	// server-authoritative stores and bounces browser events through hub.
	if opts.StateMgr != nil {
		localHost := ""
		if opts.Identity != nil {
			localHost = opts.Identity.Fingerprint()
		}
		opts.StateMgr.SetRenameHook(func(oldName, newName string) {
			if opts.AttrsStore != nil {
				migrated, err := opts.AttrsStore.MigrateKey(localHost, oldName, newName)
				if err != nil {
					logrus.WithError(err).WithField("session", newName).Warn("failed to persist migrated session attrs")
				}
				for _, key := range migrated {
					attr := opts.AttrsStore.Get(key)
					fanoutAttrsDeltaToPeers(opts, key, attr)
					if hub != nil {
						hub.BroadcastJSON(map[string]interface{}{"type": "session-attrs-updated", "key": key})
					}
				}
			}
			if opts.OrderStore != nil {
				migrated, err := opts.OrderStore.MigrateKey(localHost, oldName, newName)
				if err != nil {
					logrus.WithError(err).WithField("session", newName).Warn("failed to persist migrated session order")
				}
				for _, key := range migrated {
					order := opts.OrderStore.Get(key)
					fanoutOrderDeltaToPeers(opts, key, order)
					if hub != nil {
						hub.BroadcastJSON(map[string]interface{}{"type": "session-order-updated", "key": key})
					}
				}
			}
			if opts.GroupStore != nil {
				changed, err := opts.GroupStore.MigrateKey(localHost, oldName, newName)
				if err != nil {
					logrus.WithError(err).WithField("session", newName).Warn("failed to persist migrated groups")
				}
				for _, id := range changed {
					group, ok := opts.GroupStore.Get(id)
					if !ok {
						continue
					}
					fanoutGroupDeltaToPeers(opts, id, group)
					if hub != nil {
						hub.BroadcastJSON(map[string]interface{}{"type": "groups-updated", "id": id})
					}
				}
			}
		})
	}
	if opts.AttrsStore != nil {
		sink := attrsStoreAdapter{store: opts.AttrsStore}
		if opts.LinkSupervisor != nil {
			opts.LinkSupervisor.SetAttrsSink(sink)
		}
		if opts.PeerHandler != nil {
			opts.PeerHandler.SetAttrsSink(sink)
		}
	}
	if opts.LinkSupervisor != nil {
		opts.LinkSupervisor.SetBrowserHub(hub)
	}
	if opts.PeerHandler != nil {
		opts.PeerHandler.SetBrowserHub(hub)
	}
	if opts.OrderStore != nil {
		sink := sessionOrderStoreAdapter{store: opts.OrderStore}
		if opts.LinkSupervisor != nil {
			opts.LinkSupervisor.SetOrderSink(sink)
		}
		if opts.PeerHandler != nil {
			opts.PeerHandler.SetOrderSink(sink)
		}
	}
	if opts.GroupStore != nil {
		sink := groupStoreAdapter{store: opts.GroupStore}
		if opts.LinkSupervisor != nil {
			opts.LinkSupervisor.SetGroupSink(sink)
		}
		if opts.PeerHandler != nil {
			opts.PeerHandler.SetGroupSink(sink)
		}
	}
}

func registerAPIRoutes(r chi.Router, opts *Options, hub *ws.Hub) {
	secureCookies := false

	r.Route("/api", func(r chi.Router) {
		// Public auth endpoints (no middleware)
		r.Get("/auth/status", auth.StatusHandler(opts.AuthEnabled, opts.PasswordStore))
		if opts.AuthEnabled {
			r.Post("/auth/setup", auth.SetupHandler(opts.PasswordStore, opts.SessionMgr, secureCookies))
			r.Post("/auth/login", auth.LoginHandler(opts.PasswordStore, opts.SessionMgr, secureCookies))
			r.Post("/auth/logout", auth.LogoutHandler(opts.SessionMgr))
			r.Get("/auth/check", auth.CheckHandler(opts.SessionMgr))
		}

		// Peer bootstrap endpoint — password-authenticated (no session cookie).
		// Lets two nodes establish mutual trust via the dashboard password.
		r.Post("/peers/bootstrap", func(w http.ResponseWriter, r *http.Request) {
			handlePeersBootstrap(w, r, opts)
		})

		// Version endpoint — public, no auth required
		r.Get("/version", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"version": common.VERSION,
				"commit":  common.COMMIT,
			})
		})
		// Tool event ingest — no auth required (used by local CLI via unix socket)
		r.Post("/tool-event", func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(io.LimitReader(r.Body, 16384))
			if err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}

			logrus.WithField("raw_body", string(body)).Trace("tool-event API: received request")

			var evt toolevents.Event
			if err := json.Unmarshal(body, &evt); err != nil {
				logrus.WithError(err).WithField("raw_body", string(body)).Trace("tool-event API: JSON parse failed")
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}

			if evt.Tool == "" || evt.Status == "" || evt.Session == "" {
				logrus.WithFields(logrus.Fields{
					"tool": evt.Tool, "status": evt.Status, "session": evt.Session,
				}).Trace("tool-event API: missing required fields")
				http.Error(w, "tool, status, and session are required", http.StatusBadRequest)
				return
			}

			// Stamp local host identity when running in multi-host mode
			if opts.PeerMgr != nil && evt.Host == "" {
				evt.Host = opts.PeerMgr.LocalID()
				evt.HostName = opts.PeerMgr.LocalName()
			}
			if len(evt.Files) > 0 {
				cwd := toolevents.ResolveSessionCWD(opts.CWDResolver, evt.Session)
				evt.Artifacts = toolevents.EnrichArtifacts(evt.Files, cwd, evt.Tool, "hook")
			}

			logrus.WithFields(logrus.Fields{
				"tool":    evt.Tool,
				"status":  evt.Status,
				"session": evt.Session,
				"window":  evt.Window,
				"pane":    evt.Pane,
				"message": evt.Message,
				"host":    evt.Host,
			}).Debug("received tool event via API")

			opts.Tracker.Record(&evt)
			if opts.StateMgr != nil {
				opts.StateMgr.UpdateSessionMetadataFromEvent(&evt)
			}
			w.WriteHeader(http.StatusNoContent)
		})

		// Protected API routes
		r.Group(func(r chi.Router) {
			if opts.AuthEnabled {
				r.Use(auth.Middleware(opts.SessionMgr))
			}

			// Agent status — check which agents are installed/configured
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

				// Remote peer — request capture over the control link.
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

				// Local session — capture from daemon registry.
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
				req.Name = strings.TrimSpace(req.Name)
				req.Path = strings.TrimSpace(req.Path)
				req.Command = strings.TrimSpace(req.Command)

				// Remote host — always forward via peer.
				if req.Host != "" && opts.PeerMgr != nil && !opts.PeerMgr.IsLocal(req.Host) {
					req.Name = resolveNewSessionName(opts, req.Host, req.Name, req.Command, req.Path)
					if req.Name == "" {
						http.Error(w, "name or path is required", http.StatusBadRequest)
						return
					}
					if err := CreateSession(opts, scheduler.CreateSessionReq{
						Name:           req.Name,
						Host:           req.Host,
						Path:           req.Path,
						Command:        req.Command,
						AgentType:      req.AgentType,
						WorktreeBranch: req.WorktreeBranch,
						ScheduleID:     req.ScheduleID,
					}); err != nil {
						switch err.Error() {
						case "peer not connected", "peer send queue full":
							http.Error(w, err.Error(), http.StatusBadGateway)
						default:
							http.Error(w, err.Error(), http.StatusInternalServerError)
						}
						return
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]string{"name": req.Name})
					return
				}

				// Daemon backend (default for all local sessions).
				// Always deduplicate — even when the caller supplies a name
				// (e.g. drag-to-split sends "shell" every time).
				req.Name = resolveNewSessionName(opts, "", req.Name, req.Command, req.Path)
				if req.Name == "" {
					req.Name = fmt.Sprintf("shell-%d", time.Now().UnixMilli())
				}
				shell := req.Command
				if shell == "" || shell == "shell" {
					shell = "" // let daemon default to $SHELL
				}
				cwd := req.Path
				if cwd == "~" {
					cwd = ""
				}
				// If a worktree branch is requested, create the linked worktree
				// first and redirect the session path to it.
				if req.WorktreeBranch != "" && cwd != "" {
					expanded := cwd
					if strings.HasPrefix(expanded, "~") {
						if home, err := os.UserHomeDir(); err == nil && home != "" {
							expanded = home + expanded[1:]
						}
					}
					if !filepath.IsAbs(expanded) {
						if home, err := os.UserHomeDir(); err == nil {
							expanded = filepath.Join(home, expanded)
						}
					}
					sanitized := strings.ReplaceAll(req.WorktreeBranch, "/", "-")
					worktreesDir := filepath.Join(expanded, ".worktrees")
					if err := os.MkdirAll(worktreesDir, 0755); err != nil {
						http.Error(w, fmt.Sprintf("mkdir .worktrees: %v", err), http.StatusInternalServerError)
						return
					}
					destPath := filepath.Join(worktreesDir, sanitized)
					if err := git.CreateWorktree(expanded, req.WorktreeBranch, destPath); err != nil {
						http.Error(w, fmt.Sprintf("git worktree add: %v", err), http.StatusInternalServerError)
						return
					}
					cwd = destPath
				}
				if err := opts.DaemonReg.Create(req.Name, shell, cwd, 120, 40); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				// Store agent type and schedule ID so they persist in state.
				if req.AgentType != "" && opts.StateMgr != nil {
					opts.StateMgr.SetSessionAgentType(req.Name, req.AgentType)
				}
				if opts.AttrsStore != nil && req.ScheduleID != "" {
					localHost := ""
					if opts.PeerMgr != nil {
						localHost = opts.PeerMgr.LocalID()
					}
					key := sessionKey(localHost, req.Name)
					if attr, err := opts.AttrsStore.SetScheduleID(key, req.ScheduleID); err != nil {
						logrus.WithError(err).Warn("failed to store schedule id")
					} else {
						fanoutAttrsDeltaToPeers(opts, key, attr)
						if opts.Hub != nil {
							opts.Hub.BroadcastJSON(map[string]interface{}{"type": "session-attrs-updated", "key": key})
						}
					}
				}
				// Trigger state refresh so WebSocket clients get notified.
				if opts.StateMgr != nil && opts.RefreshSessions != nil {
					opts.RefreshSessions()
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"name": req.Name})
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

				// Remote host — forward via peer connection. The new label propagates
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

				// Remote host — name it here (the peer process may have no namer
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
					Members []namer.GroupMember `json:"members"`
					Current string              `json:"current,omitempty"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Members) == 0 {
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

				// Remote host — forward via peer connection
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

				// Remote host — forward via peer connection
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

				// Remote host — forward via peer connection
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

				// Transition lifecycle state before killing so the daemon
				// records this as an intentional termination, not a crash.
				if opts.DaemonReg != nil && opts.DaemonReg.LifecycleStore() != nil {
					_ = opts.DaemonReg.LifecycleStore().Transition(req.Name, "active", "termination_requested")
				}
				// Kill daemon session.
				if opts.DaemonReg != nil {
					if err := opts.DaemonReg.Kill(req.Name); err != nil {
						logrus.WithError(err).WithField("session", req.Name).Warn("daemon kill failed")
					}
				}
				if opts.StateMgr != nil {
					opts.StateMgr.RemoveSession(req.Name)
				}

				// Remove the linked worktree if requested. Non-fatal — session is
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

			// Dedicated file upload — streams a browser-supplied file into
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

			// Stats endpoint — aggregate overview data
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


			// Wiki integration: read the API key generated by wiki-viewer at
			// ~/.wiki-viewer/api-key so the frontend can auth iframe embeds
			// without WIKI_NO_AUTH=1.
			r.Get("/wiki/key", func(w http.ResponseWriter, r *http.Request) {
				home, err := os.UserHomeDir()
				if err != nil {
					http.Error(w, "cannot resolve home dir", http.StatusInternalServerError)
					return
				}
				keyPath := filepath.Join(home, ".wiki-viewer", "api-key")
				data, err := os.ReadFile(keyPath)
				if err != nil {
					if os.IsNotExist(err) {
						http.Error(w, "wiki-viewer api key not found", http.StatusNotFound)
					} else {
						http.Error(w, "cannot read api key", http.StatusInternalServerError)
					}
					return
				}
				key := strings.TrimSpace(string(data))
				if key == "" {
					http.Error(w, "wiki-viewer api key is empty", http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"key": key})
			})

			// Session-attribute endpoints — server-authoritative, mesh-wide shared
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

			// Session-order endpoints — server-authoritative, per-session rank map.
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

			// Group endpoints — server-authoritative, durable field-LWW records.
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
					group, err = opts.GroupStore.SetTree(body.ID, body.Tree)
				case "name":
					group, err = opts.GroupStore.SetName(body.ID, body.Name)
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

			// Schedule registry
			r.Get("/schedules", func(w http.ResponseWriter, r *http.Request) {
				if opts.SchedulerStore == nil {
					http.Error(w, "scheduler not available", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(opts.SchedulerStore.List())
			})
			r.Post("/schedules", func(w http.ResponseWriter, r *http.Request) {
				if opts.SchedulerStore == nil {
					http.Error(w, "scheduler not available", http.StatusServiceUnavailable)
					return
				}
				var job scheduler.Job
				if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
					http.Error(w, "invalid JSON", http.StatusBadRequest)
					return
				}
				created, err := opts.SchedulerStore.Add(job)
				if err != nil {
					if strings.Contains(err.Error(), "invalid cron spec") || strings.Contains(err.Error(), "cron spec is required") {
						http.Error(w, err.Error(), http.StatusBadRequest)
					} else {
						http.Error(w, err.Error(), http.StatusInternalServerError)
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(created)
			})
			r.Put("/schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
				if opts.SchedulerStore == nil {
					http.Error(w, "scheduler not available", http.StatusServiceUnavailable)
					return
				}
				id := chi.URLParam(r, "id")
				cur, ok := opts.SchedulerStore.Get(id)
				if !ok {
					http.Error(w, "job not found", http.StatusNotFound)
					return
				}
				var job scheduler.Job
				if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
					http.Error(w, "invalid JSON", http.StatusBadRequest)
					return
				}
				if job.ID != "" && job.ID != id {
					http.Error(w, "id mismatch", http.StatusBadRequest)
					return
				}
				job.ID = id
				job.CreatedAt = cur.CreatedAt
				job.LastRun = cur.LastRun
				job.RunCount = cur.RunCount
				if job.SessionNamePrefix == "" {
					job.SessionNamePrefix = cur.SessionNamePrefix
				}
				if job.Enabled {
					schedule, err := cron.ParseStandard(job.CronSpec)
					if err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					job.NextRun = schedule.Next(time.Now())
				} else {
					job.NextRun = time.Time{}
				}
				updated, err := opts.SchedulerStore.Update(job)
				if err != nil {
					if strings.Contains(err.Error(), "invalid cron spec") || strings.Contains(err.Error(), "cron spec is required") {
						http.Error(w, err.Error(), http.StatusBadRequest)
					} else {
						http.Error(w, err.Error(), http.StatusInternalServerError)
					}
					return
				}
				// Lowering the cap prunes existing over-limit runs immediately
				// instead of waiting for the next fire. Leave exactly max alive.
				if updated.MaxConcurrency > 0 {
					EnforceScheduleCap(opts, updated.ID, updated.MaxConcurrency)
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(updated)
			})
			r.Delete("/schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
				if opts.SchedulerStore == nil {
					http.Error(w, "scheduler not available", http.StatusServiceUnavailable)
					return
				}
				if err := opts.SchedulerStore.Remove(chi.URLParam(r, "id")); err != nil {
					if strings.Contains(err.Error(), "not found") {
						http.Error(w, err.Error(), http.StatusNotFound)
					} else {
						http.Error(w, err.Error(), http.StatusInternalServerError)
					}
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})
			r.Post("/schedules/{id}/run", func(w http.ResponseWriter, r *http.Request) {
				if opts.SchedulerStore == nil || opts.SchedulerRunner == nil {
					http.Error(w, "scheduler not available", http.StatusServiceUnavailable)
					return
				}
				id := chi.URLParam(r, "id")
				job, ok := opts.SchedulerStore.Get(id)
				if !ok {
					http.Error(w, "job not found", http.StatusNotFound)
					return
				}
				req := scheduler.CreateSessionReq{
					Name:           job.SessionNamePrefix,
					Host:           job.Host,
					Path:           job.Path,
					Command:        job.Command,
					AgentType:      job.AgentType,
					WorktreeBranch: job.WorktreeBranch,
					ScheduleID:     job.ID,
				}
				if req.Name == "" {
					req.Name = job.Name
				}
				if req.Name == "" {
					req.Name = "schedule"
				}
				req.Name = fmt.Sprintf("%s-%d", req.Name, time.Now().Unix())
				if job.MaxConcurrency > 0 {
					EnforceScheduleCap(opts, job.ID, job.MaxConcurrency-1)
				}
				if err := CreateSession(opts, req); err != nil {
					if err.Error() == "peer not connected" {
						http.Error(w, err.Error(), http.StatusBadGateway)
					} else {
						http.Error(w, err.Error(), http.StatusInternalServerError)
					}
					return
				}
				nextRun := job.NextRun
				if _, err := opts.SchedulerStore.MarkRan(job.ID, time.Now(), nextRun); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				updated, _ := opts.SchedulerStore.Get(job.ID)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(updated)
			})

			// Peers management endpoints (auth-protected)
			r.Get("/peers", func(w http.ResponseWriter, r *http.Request) {
				handleGetPeers(w, r, opts)
			})
			r.Post("/peers", func(w http.ResponseWriter, r *http.Request) {
				handlePostPeers(w, r, opts)
			})
			r.Patch("/peers/{fp}", func(w http.ResponseWriter, r *http.Request) {
				handlePatchPeer(w, r, opts)
			})
			r.Post("/peers/{fp}/reconnect", func(w http.ResponseWriter, r *http.Request) {
				handleReconnectPeer(w, r, opts)
			})
			r.Delete("/peers/{fp}", func(w http.ResponseWriter, r *http.Request) {
				handleDeletePeer(w, r, opts)
			})
		})

		// Port forward registry (local single-host).
		r.Group(func(r chi.Router) {
			if opts.AuthEnabled {
				r.Use(auth.Middleware(opts.SessionMgr))
			}
			r.Get("/portforwards", func(w http.ResponseWriter, r *http.Request) {
				if opts.PortForwardStore == nil {
					http.Error(w, "port forwarding not available", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(opts.PortForwardStore.List())
			})
			r.Post("/portforwards", func(w http.ResponseWriter, r *http.Request) {
				if opts.PortForwardStore == nil {
					http.Error(w, "port forwarding not available", http.StatusServiceUnavailable)
					return
				}
				var req struct {
					Port         int              `json:"port"`
					Label        string           `json:"label"`
					Mode         portforward.Mode `json:"mode"`
					ExternalPort int              `json:"external_port"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Port < 1 || req.Port > 65535 {
					http.Error(w, "port (1-65535) required", http.StatusBadRequest)
					return
				}
				if req.Mode == "" {
					req.Mode = portforward.ModeProxy
				}
				if err := opts.PortForwardStore.Add(req.Port, req.Label, req.Mode, req.ExternalPort); err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(opts.PortForwardStore.List())
			})
			r.Delete("/portforward/{port}", func(w http.ResponseWriter, r *http.Request) {
				if opts.PortForwardStore == nil {
					http.Error(w, "port forwarding not available", http.StatusServiceUnavailable)
					return
				}
				port, err := strconv.Atoi(chi.URLParam(r, "port"))
				if err != nil {
					http.Error(w, "invalid port", http.StatusBadRequest)
					return
				}
				opts.PortForwardStore.Remove(port)
				w.WriteHeader(http.StatusNoContent)
			})
		})

		// PTY benchmark: compare direct PTY throughput and latency
		r.Get("/pty-benchmark", func(w http.ResponseWriter, r *http.Request) {
			handlePTYBenchmark(w, r, opts)
		})
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

	// Peer WebSocket routes (no browser auth — peers use their own challenge-response)
	if opts.PeerHandler != nil {
		r.Get("/ws/peer", opts.PeerHandler.HandlePeer)
		r.Get("/ws/peer-stream", opts.PeerHandler.HandlePeerStream)
	}
}

// activePaneCwd returns the CurrentPath of the active pane among panes, or ""
// if none — no fallback to inactive panes, so a relative file open resolves
// against exactly the pane shown in the terminal.

// fileGrantTTL bounds how long an opened file stays fetchable. A grant is
// minted per explicit "Open file" click; the browser has this long to fetch
// (and to reload, e.g. re-render a PDF) before the token dies.
// ponytail: fixed 5m window; make configurable if reload-after-expiry annoys.
const fileGrantTTL = 5 * time.Minute

// fileGrants is an in-memory capability store: a token maps to one absolute
// path with an expiry. It replaces open-ended whole-FS read — the serve
// endpoint can only return paths that were explicitly granted and not expired.
// Eviction is lazy (on access); no background goroutine.
type fileGrants struct {
	mu    sync.Mutex
	byTok map[string]fileGrant
}

type fileGrant struct {
	path    string
	expires time.Time
}

func newFileGrants() *fileGrants { return &fileGrants{byTok: map[string]fileGrant{}} }

func (g *fileGrants) grant(path string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	tok := hex.EncodeToString(b[:])
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, v := range g.byTok { // lazy eviction
		if now.After(v.expires) {
			delete(g.byTok, k)
		}
	}
	g.byTok[tok] = fileGrant{path: path, expires: now.Add(fileGrantTTL)}
	return tok
}

func (g *fileGrants) resolve(tok string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.byTok[tok]
	if !ok || time.Now().After(v.expires) {
		delete(g.byTok, tok)
		return "", false
	}
	return v.path, true
}

// resolveFilePath turns a user-selected path into an absolute, existing file
// path. Relative paths resolve against the active pane's cwd (see
// toolevents.ResolveSessionCWD) — no fallback to other panes. Returns an HTTP
// status + message on failure.
func resolveFilePath(p string, opts *Options, r *http.Request) (string, int, string) {
	if p == "" {
		return "", http.StatusBadRequest, "path required"
	}
	if !filepath.IsAbs(p) {
		base := ""
		// ListPanes(session) targets the session's current window; pick its
		// active pane. ListWindows does not populate panes, so we query panes.
		if session := r.URL.Query().Get("session"); session != "" && opts.CWDResolver != nil {
			base = toolevents.ResolveSessionCWD(opts.CWDResolver, session)
		}
		if base == "" {
			return "", http.StatusBadRequest, "cannot resolve relative path: no active pane cwd"
		}
		p = filepath.Clean(filepath.Join(base, p))
	} else {
		p = filepath.Clean(p)
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", http.StatusNotFound, "not found"
	}
	if info.IsDir() {
		return "", http.StatusBadRequest, "path is a directory"
	}
	return p, 0, ""
}

// handleFileGrant resolves and validates a user-selected path, then mints a
// short-lived token the browser exchanges at GET /file?token=... . This is the
// only place a path enters the capability store.
//
// Route: POST /file/grant?path=<abs-or-rel>&session=<name>[&host=<id>]
func handleFileGrant(w http.ResponseWriter, r *http.Request, opts *Options, grants *fileGrants) {
	hostID := r.URL.Query().Get("host")

	// Remote peer — relay file read through the control link.
	if hostID != "" && opts.PeerMgr != nil && !opts.PeerMgr.IsLocal(hostID) {
		handleRemoteFileGrant(w, r, opts, grants, hostID)
		return
	}

	p, status, msg := resolveFilePath(r.URL.Query().Get("path"), opts, r)
	if status != 0 {
		http.Error(w, msg, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": grants.grant(p)})
}

// handleRemoteFileGrant fetches a file from a remote peer, writes it to a
// local temp file, and grants a token for it.
func handleRemoteFileGrant(w http.ResponseWriter, r *http.Request, opts *Options, grants *fileGrants, hostID string) {
	if opts.FileReadReg == nil {
		http.Error(w, "file read unavailable", http.StatusInternalServerError)
		return
	}
	peerConn := opts.PeerMgr.GetPeerConnection(hostID)
	if peerConn == nil {
		http.Error(w, "peer not connected", http.StatusBadGateway)
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	token := peer.NewToken()
	msg, err := peer.NewMessage(peer.MsgFileRead, peer.FileReadPayload{
		Token:   token,
		Path:    filePath,
		Session: r.URL.Query().Get("session"),
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	ch, cancel := opts.FileReadReg.Register(token)
	defer cancel()
	if !peerConn.Enqueue(msg) {
		http.Error(w, "peer send queue full", http.StatusBadGateway)
		return
	}

	select {
	case res := <-ch:
		if res.Error != "" {
			http.Error(w, "remote file: "+res.Error, http.StatusNotFound)
			return
		}
		data, err := base64.StdEncoding.DecodeString(res.Data)
		if err != nil {
			http.Error(w, "decode error", http.StatusInternalServerError)
			return
		}
		// Write to temp file so handleFile can serve it.
		ext := filepath.Ext(res.FileName)
		tmp, err := os.CreateTemp("", "guppi-remote-*"+ext)
		if err != nil {
			http.Error(w, "temp file error", http.StatusInternalServerError)
			return
		}
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			http.Error(w, "write error", http.StatusInternalServerError)
			return
		}
		tmp.Close()
		// Schedule cleanup after grant TTL.
		go func() {
			time.Sleep(fileGrantTTL + time.Minute)
			os.Remove(tmp.Name())
		}()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": grants.grant(tmp.Name())})
	case <-time.After(10 * time.Second):
		http.Error(w, "peer file read timed out", http.StatusGatewayTimeout)
	}
}

// handleFile serves a previously granted, non-expired file to the browser,
// which renders it by content-type. It can ONLY serve paths minted by
// handleFileGrant — there is no arbitrary-path read.
//
// Route: GET /file?token=<token>
func handleFile(w http.ResponseWriter, r *http.Request, grants *fileGrants) {
	p, ok := grants.resolve(r.URL.Query().Get("token"))
	if !ok {
		http.Error(w, "invalid or expired file token", http.StatusForbidden)
		return
	}
	info, err := os.Stat(p)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, "cannot open", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	// Inline so the browser renders instead of downloading. ServeContent does
	// content-type sniffing, range requests and caching for free.
	w.Header().Set("Content-Disposition", "inline; filename=\""+filepath.Base(p)+"\"")
	http.ServeContent(w, r, filepath.Base(p), info.ModTime(), f)
}

// handleUpload streams a browser-supplied file into private temp storage on
// the session's host and returns {"path","quotedPath"}. No product size cap.
// Route: POST /api/upload?session=<name>&host=<id>&filename=<name>
func handleUpload(w http.ResponseWriter, r *http.Request, opts *Options) {
	filename := r.URL.Query().Get("filename")
	if filename == "" {
		http.Error(w, "filename required", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("session") == "" {
		http.Error(w, "session required", http.StatusBadRequest)
		return
	}
	hostID := r.URL.Query().Get("host")
	if hostID != "" && opts.PeerMgr != nil && !opts.PeerMgr.IsLocal(hostID) {
		handleRemoteUpload(w, r, opts, hostID, filename)
		return
	}
	path, err := model.StoreUploadedFile(r.Body, filename)
	if err != nil {
		if r.Context().Err() != nil {
			return // client gone, nothing to write
		}
		if errors.Is(err, model.ErrEmptyUpload) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "store upload: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"path": path, "quotedPath": model.ShellQuote(path),
	})
}

// handleRemoteUpload relays a browser file upload to a peer host over a
// dedicated /ws/peer-stream data connection.
func handleRemoteUpload(w http.ResponseWriter, r *http.Request, opts *Options, hostID, filename string) {
	if opts == nil || opts.PeerMgr == nil {
		http.Error(w, "peer routing unavailable", http.StatusInternalServerError)
		return
	}
	peerConn := opts.PeerMgr.GetPeerConnection(hostID)
	if peerConn == nil {
		http.Error(w, "peer not connected", http.StatusBadGateway)
		return
	}
	if !peerConn.HasCapability(peer.CapUpload) {
		http.Error(w, "peer does not support uploads — upgrade the peer first", http.StatusUpgradeRequired)
		return
	}
	if opts.Identity == nil || opts.StreamReg == nil {
		http.Error(w, "peer routing unavailable", http.StatusInternalServerError)
		return
	}

	streamID := peer.GenerateStreamID()
	token := peer.NewToken()
	log := logrus.WithFields(logrus.Fields{"stream": streamID, "file": filename, "host": hostID})
	openMsg, _ := peer.NewMessage(peer.MsgOpenUpload, peer.OpenUploadPayload{
		StreamID:     streamID,
		Token:        token,
		Filename:     filename,
		ViewerHostID: opts.PeerMgr.LocalID(),
	})

	dial := peerConn.Role == peer.RoleDialer
	var conn *websocket.Conn
	if dial {
		addr := opts.PeerMgr.GetPeerAddress(hostID)
		c, err := peer.DialPeerStream(context.Background(), addr, opts.Identity, token)
		if err != nil {
			log.WithError(err).Debug("upload stream dial failed")
			http.Error(w, "upload stream setup failed", http.StatusBadGateway)
			return
		}
		conn = c
		if !peerConn.EnqueueHi(openMsg) {
			conn.Close()
			http.Error(w, "peer send queue full", http.StatusBadGateway)
			return
		}
	} else {
		ps := peer.NewPendingStream(streamID, "", 0, 0, hostID, opts.PeerMgr.LocalID(), hostID)
		opts.StreamReg.Register(token, ps)
		if !peerConn.EnqueueHi(openMsg) {
			http.Error(w, "peer send queue full", http.StatusBadGateway)
			return
		}
		// Context-aware setup wait — honour browser cancellation (xhr.abort).
		resolvedCh := make(chan struct {
			conn *websocket.Conn
			ok   bool
		}, 1)
		go func() {
			c, ok := ps.WaitResolved(peer.StreamSetupTimeout())
			resolvedCh <- struct {
				conn *websocket.Conn
				ok   bool
			}{c, ok}
		}()
		select {
		case <-r.Context().Done():
			return
		case rc := <-resolvedCh:
			if !rc.ok {
				http.Error(w, "upload stream setup failed", http.StatusBadGateway)
				return
			}
			conn = rc.conn
		}
	}
	defer conn.Close()
	conn.EnableWriteCompression(false)

	// Pump body to peer as binary frames (256 KiB chunks).
	buf := make([]byte, 256*1024)
	for {
		n, readErr := r.Body.Read(buf)
		if n > 0 {
			if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				log.WithError(err).Debug("upload relay write failed")
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"upload-abort"}`))
				return
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			// Body read error (client disconnected)
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"upload-abort"}`))
			return
		}
	}
	// Send EOF frame.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"upload-eof"}`)); err != nil {
		log.WithError(err).Debug("upload relay eof failed")
		return
	}

	// Read result from peer with context awareness so browser cancellation
	// (e.g. xhr.abort) is honoured even after the body has been fully sent.
	type wsReadResult struct {
		data []byte
		err  error
	}
	readCh := make(chan wsReadResult, 1)
	go func() {
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, data, err := conn.ReadMessage()
		readCh <- wsReadResult{data, err}
	}()
	var result []byte
	select {
	case <-r.Context().Done():
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"upload-abort"}`))
		conn.Close()
		return
	case rr := <-readCh:
		if rr.err != nil {
			log.WithError(rr.err).Debug("upload relay result failed")
			http.Error(w, "peer upload timed out", http.StatusGatewayTimeout)
			return
		}
		result = rr.data
	}

	var res struct {
		Path       string `json:"path"`
		QuotedPath string `json:"quotedPath"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(result, &res); err != nil {
		log.WithError(err).Debug("upload relay bad result")
		http.Error(w, "invalid peer response", http.StatusBadGateway)
		return
	}
	if res.Error != "" {
		code := http.StatusInternalServerError
		if model.IsEmptyUploadMessage(res.Error) {
			code = http.StatusBadRequest
		}
		http.Error(w, "remote upload: "+res.Error, code)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"path": res.Path, "quotedPath": res.QuotedPath,
	})
}

func registerProxyRoutes(r chi.Router, opts *Options) {
	// Port-forward proxy — exposes localhost-bound services through termyard's URL.
	// Requires auth (same rule as other protected routes) so remote users can't
	// reach internal services without a valid session.
	if opts.PortForwardStore != nil {
		proxyHandler := func(w http.ResponseWriter, r *http.Request) {
			handleProxy(w, r, opts.Port)
		}
		if opts.AuthEnabled {
			authMw := auth.Middleware(opts.SessionMgr)
			r.With(authMw).Get("/proxy/{port}", proxyHandler)
			r.With(authMw).Get("/proxy/{port}/*", proxyHandler)
		} else {
			r.Get("/proxy/{port}", proxyHandler)
			r.Get("/proxy/{port}/*", proxyHandler)
		}
	}
	// File open — capability-based: POST /file/grant mints a short-lived token
	// for one explicitly-opened path; GET /file?token=... serves it. Same auth as
	// the proxy above; not gated on PortForwardStore since it needs no port config.
	grants := newFileGrants()
	grantHandler := func(w http.ResponseWriter, r *http.Request) { handleFileGrant(w, r, opts, grants) }
	fileHandler := func(w http.ResponseWriter, r *http.Request) { handleFile(w, r, grants) }
	if opts.AuthEnabled {
		authMw := auth.Middleware(opts.SessionMgr)
		r.With(authMw).Post("/file/grant", grantHandler)
		r.With(authMw).Get("/file", fileHandler)
	} else {
		r.Post("/file/grant", grantHandler)
		r.Get("/file", fileHandler)
	}
}

func registerFrontendRoutes(r chi.Router) error {
	// Serve embedded frontend
	sub, err := fs.Sub(frontendFS, "dist")
	if err != nil {
		return fmt.Errorf("frontend fs: %w", err)
	}
	fileServer := http.FileServer(http.FS(sub))
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		f, err := sub.Open(r.URL.Path[1:])
		if err != nil {
			r.URL.Path = "/"
		} else {
			f.Close()
		}
		// Content-hashed assets are immutable; everything else (index.html, SPA
		// fallback) must revalidate so a rebuilt binary's new asset hashes are
		// picked up instead of a stale cached index.html 404ing the old ones.
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
	return nil
}

func serveAndWait(ctx context.Context, opts *Options, logger *logrus.Entry, handler http.Handler) error {
	srv := &http.Server{
		Handler:           handler,
		ErrorLog:          log.New(logger.WriterLevel(logrus.WarnLevel), "", 0),
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		// Note: ReadTimeout and WriteTimeout are intentionally omitted.
		// They apply to the underlying net.Conn and would kill long-lived
		// WebSocket connections after the timeout period.
	}

	serverErr := make(chan error, 2)

	tlsCfg, err := tlsConfig(opts)
	if err != nil {
		return err
	}
	srv.TLSConfig = tlsCfg

	// Start TCP listener for browser connections
	tcpAddr := fmt.Sprintf(":%d", opts.Port)
	tcpListener, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		return fmt.Errorf("tcp listen: %w", err)
	}

	scheme := "http"
	go func() {
		var serveErr error
		if tlsCfg != nil {
			serveErr = srv.ServeTLS(tcpListener, "", "") // certs come from srv.TLSConfig
		} else {
			serveErr = srv.Serve(tcpListener)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.WithError(serveErr).Error("tcp listen error")
			serverErr <- serveErr
		}
	}()
	if tlsCfg != nil {
		scheme = "https"
	}

	logger.WithField("port", opts.Port).Info("starting termyard server")
	logger.Infof("open %s://localhost:%d in your browser", scheme, opts.Port)
	if opts.AuthEnabled {
		logger.Info("authentication is enabled")
	}

	// Start Unix socket listener for local notify CLI
	var unixListener net.Listener
	socketPath := opts.SocketPath
	if socketPath == "" {
		socketPath = socket.DefaultPath()
	}
	if err := socket.EnsureDir(socketPath); err != nil {
		logger.WithError(err).Warn("failed to create socket directory, notify via socket will be unavailable")
	} else {
		// Remove stale socket file from a previous run
		_ = socket.Cleanup(socketPath)

		unixListener, err = net.Listen("unix", socketPath)
		if err != nil {
			logger.WithError(err).Warn("failed to listen on unix socket, notify via socket will be unavailable")
		} else {
			go func() {
				if err := srv.Serve(unixListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.WithError(err).Error("unix socket listen error")
					serverErr <- err
				}
			}()
			logger.WithField("socket", socketPath).Info("listening on unix socket")
		}
	}

	select {
	case <-ctx.Done():
	case err := <-serverErr:
		return fmt.Errorf("server error: %w", err)
	}

	logger.Info("shutting down server")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.WithError(err).Error("unable to shutdown gracefully")
		return err
	}

	// Clean up socket file
	if unixListener != nil {
		_ = socket.Cleanup(socketPath)
	}

	// Stop any socat processes
	if opts.PortForwardStore != nil {
		opts.PortForwardStore.StopAll()
	}

	return nil
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
// never resurrected here — the origin host is authoritative and relays the
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

func defaultSessionName(command, projectPath string) string {
	base := strings.TrimSpace(command)
	if idx := strings.IndexByte(base, ' '); idx >= 0 {
		base = base[:idx]
	}
	base = strings.Trim(base, `"'`)
	base = strings.TrimSpace(base)
	if base == "" {
		base = "session"
	}
	if projectPath == "" {
		return ""
	}

	projectBase := strings.TrimSpace(projectPath)
	projectBase = strings.TrimRight(projectBase, "/")
	if idx := strings.LastIndex(projectBase, "/"); idx >= 0 {
		projectBase = projectBase[idx+1:]
	}
	projectBase = sanitizeSessionSegment(projectBase)
	if projectBase == "" {
		projectBase = "workspace"
	}

	return sanitizeSessionSegment(base) + "-" + projectBase
}

func sanitizeSessionSegment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-', r == '_', r == '.', r == '/', r == ' ':
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func resolveNewSessionName(opts *Options, host, name, command, projectPath string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultSessionName(command, projectPath)
	}
	if name == "" {
		return ""
	}
	return ensureUniqueSessionName(name, existingSessionNames(opts, host))
}

func existingSessionNames(opts *Options, host string) []string {
	if opts == nil {
		return nil
	}

	if host != "" && opts.PeerMgr != nil && !opts.PeerMgr.IsLocal(host) {
		sessions := opts.PeerMgr.GetAllSessions()
		names := make([]string, 0, len(sessions))
		for _, session := range sessions {
			if session != nil && session.Host == host {
				names = append(names, session.Name)
			}
		}
		return names
	}

	if opts.StateMgr != nil {
		sessions := opts.StateMgr.GetSessions()
		names := make([]string, 0, len(sessions))
		for _, session := range sessions {
			if session != nil {
				names = append(names, session.Name)
			}
		}
		return names
	}

	return nil
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

func ensureUniqueSessionName(name string, existing []string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	used := make(map[string]struct{}, len(existing))
	for _, candidate := range existing {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			used[candidate] = struct{}{}
		}
	}

	if _, exists := used[name]; !exists {
		return name
	}

	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}
