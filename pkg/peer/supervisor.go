package peer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/identity"
)

// LinkStatus reports the state of a per-peer link.
type LinkStatus string

const (
	StatusIdle      LinkStatus = "idle" // disabled
	StatusDialing   LinkStatus = "dialing"
	StatusConnected LinkStatus = "connected"
	StatusBackoff   LinkStatus = "backoff"
	StatusListener  LinkStatus = "listener" // we are not the dialer; passive
)

// LinkSnapshot is the JSON-friendly view of a peer link's current state.
type LinkSnapshot struct {
	PublicKey   string     `json:"public_key"`
	Fingerprint string     `json:"fingerprint"`
	Name        string     `json:"name"`
	Address     string     `json:"address"`
	Enabled     bool       `json:"enabled"`
	Status      LinkStatus `json:"status"`
	LastError   string     `json:"last_error,omitempty"`
	NextRetry   time.Time  `json:"next_retry,omitempty"`
	LastSeen    time.Time  `json:"last_seen,omitempty"`
	PairedAt    time.Time  `json:"paired_at"`
	IsDialer    bool       `json:"is_dialer"`
}

type peerLink struct {
	peer      identity.Peer
	cancel    context.CancelFunc
	statusMu  sync.RWMutex
	status    LinkStatus
	lastErr   string
	nextRetry time.Time
}

func (l *peerLink) setStatus(s LinkStatus, errStr string, next time.Time) {
	l.statusMu.Lock()
	l.status = s
	l.lastErr = errStr
	l.nextRetry = next
	l.statusMu.Unlock()
}

func (l *peerLink) snapshot() (LinkStatus, string, time.Time) {
	l.statusMu.RLock()
	defer l.statusMu.RUnlock()
	return l.status, l.lastErr, l.nextRetry
}

// LinkSupervisor owns per-peer reconnector goroutines.
type LinkSupervisor struct {
	mu    sync.Mutex
	links map[string]*peerLink // keyed by peer public key

	deps    SessionDeps
	rootCtx context.Context
}

// NewLinkSupervisor builds a supervisor wrapping shared session deps.
func NewLinkSupervisor(deps SessionDeps) *LinkSupervisor {
	return &LinkSupervisor{
		links: make(map[string]*peerLink),
		deps:  deps,
	}
}

// SetAttrsSink wires the session-attrs store after construction. Safe to call
// once during Run() bootstrap before any sessions land.
func (s *LinkSupervisor) SetAttrsSink(sink SessionAttrsSink) {
	s.deps.AttrsSink = sink
}

// SetOrderSink wires the session-order store after construction.
func (s *LinkSupervisor) SetOrderSink(sink SessionOrderSink) {
	s.deps.OrderSink = sink
}

// SetGroupSink wires the group store after construction.
func (s *LinkSupervisor) SetGroupSink(sink GroupSink) {
	s.deps.GroupSink = sink
}

// SetBrowserHub wires the browser-events hub after construction.
func (s *LinkSupervisor) SetBrowserHub(hub BrowserBroadcaster) {
	s.deps.BrowserHub = hub

}

// Start begins supervision. Spawns reconnectors for enabled dialer-side peers
// and registers passive entries for listeners.
func (s *LinkSupervisor) Start(ctx context.Context) {
	s.rootCtx = ctx
	for _, p := range s.deps.PeerStore.List() {
		s.spawnLink(p)
	}
}

// AddPeer registers a peer + spawns its reconnector if appropriate.
func (s *LinkSupervisor) AddPeer(p identity.Peer) error {
	if err := s.deps.PeerStore.Add(p); err != nil {
		return err
	}
	s.spawnLink(p)
	return nil
}

// RemovePeer disconnects + removes a peer. Sends MsgForget if connected.
func (s *LinkSupervisor) RemovePeer(publicKey string) error {
	p := s.deps.PeerStore.GetByPublicKey(publicKey)
	if p == nil {
		return fmt.Errorf("peer not found")
	}

	// Best-effort forget message over live link (if any).
	if conn := s.deps.Manager.GetPeerConnection(p.Fingerprint()); conn != nil {
		if msg, err := NewMessage(MsgForget, struct{}{}); err == nil {
			conn.Enqueue(msg)
		}
	}

	s.mu.Lock()
	if l, ok := s.links[publicKey]; ok {
		if l.cancel != nil {
			l.cancel()
		}
		delete(s.links, publicKey)
	}
	s.mu.Unlock()

	s.deps.Manager.RemoveHost(p.Fingerprint())
	return s.deps.PeerStore.RemoveByPublicKey(publicKey)
}

// SetEnabled toggles auto-reconnect for a peer.
func (s *LinkSupervisor) SetEnabled(publicKey string, enabled bool) error {
	if err := s.deps.PeerStore.SetEnabled(publicKey, enabled); err != nil {
		return err
	}
	p := s.deps.PeerStore.GetByPublicKey(publicKey)
	if p == nil {
		return fmt.Errorf("peer not found")
	}
	if enabled {
		s.spawnLink(*p)
	} else {
		s.mu.Lock()
		if l, ok := s.links[publicKey]; ok {
			if l.cancel != nil {
				l.cancel()
			}
			delete(s.links, publicKey)
		}
		s.links[publicKey] = &peerLink{peer: *p, status: StatusIdle}
		s.mu.Unlock()
		s.deps.Manager.UnregisterPeer(p.Fingerprint())
	}
	return nil
}

// ReconnectNow forces an immediate dial attempt (cancels + respawns link).
// Tears down any active link so the new dial cycle doesn't trip the
// simultaneous-initiate role-flip on its own stale connection.
func (s *LinkSupervisor) ReconnectNow(publicKey string) {
	p := s.deps.PeerStore.GetByPublicKey(publicKey)
	if p == nil {
		return
	}
	s.mu.Lock()
	if l, ok := s.links[publicKey]; ok && l.cancel != nil {
		l.cancel()
	}
	s.mu.Unlock()
	s.deps.Manager.UnregisterPeer(p.Fingerprint())
	// Give the old goroutine a moment to drain (best-effort).
	time.Sleep(50 * time.Millisecond)
	s.spawnLink(*p)
}

// Status returns current per-peer status snapshots.
func (s *LinkSupervisor) Status() []LinkSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LinkSnapshot, 0, len(s.links))
	for _, l := range s.links {
		st, errStr, next := l.snapshot()
		out = append(out, LinkSnapshot{
			PublicKey:   l.peer.PublicKey,
			Fingerprint: l.peer.Fingerprint(),
			Name:        l.peer.Name,
			Address:     l.peer.Address,
			Enabled:     l.peer.Enabled,
			Status:      st,
			LastError:   errStr,
			NextRetry:   next,
			LastSeen:    l.peer.LastSeen,
			PairedAt:    l.peer.PairedAt,
			IsDialer:    l.peer.InitiatedByUs,
		})
	}
	return out
}

// spawnLink creates/refreshes a link entry. Spawns a reconnector goroutine
// if and only if we are the dialer and the peer is enabled.
func (s *LinkSupervisor) spawnLink(p identity.Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if l, ok := s.links[p.PublicKey]; ok {
		if l.cancel != nil {
			l.cancel()
		}
		delete(s.links, p.PublicKey)
	}

	switch {
	case !p.Enabled:
		s.links[p.PublicKey] = &peerLink{peer: p, status: StatusIdle}
		return
	case !p.InitiatedByUs:
		s.links[p.PublicKey] = &peerLink{peer: p, status: StatusListener}
		return
	}

	if s.rootCtx == nil {
		// Not started yet; just record and bail.
		s.links[p.PublicKey] = &peerLink{peer: p, status: StatusBackoff}
		return
	}

	ctx, cancel := context.WithCancel(s.rootCtx)
	link := &peerLink{peer: p, cancel: cancel, status: StatusBackoff}
	s.links[p.PublicKey] = link
	go s.runReconnector(ctx, link)
}

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	spread := int64(d / 4)
	if spread <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(spread*2) - spread)
}

func (s *LinkSupervisor) runReconnector(ctx context.Context, link *peerLink) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	log := logrus.WithFields(logrus.Fields{"peer": link.peer.Name, "id": link.peer.Fingerprint()})

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		link.setStatus(StatusDialing, "", time.Time{})
		start := time.Now()
		err := s.dialOnce(ctx, link)
		upDuration := time.Since(start)

		// Handle "already connected" race resolution from listener.
		if err != nil && err.Error() == "already connected" {
			log.Info("simultaneous initiate: flipping to listener role")
			_ = s.deps.PeerStore.SetInitiatedByUs(link.peer.PublicKey, false)
			s.mu.Lock()
			link.peer.InitiatedByUs = false
			link.setStatus(StatusListener, "", time.Time{})
			s.mu.Unlock()
			return
		}

		errStr := ""
		if err != nil {
			errStr = err.Error()
			log.WithError(err).Debug("dial cycle ended")
		}

		if upDuration > 30*time.Second {
			backoff = time.Second
		}
		sleep := backoff + jitter(backoff)
		if sleep < 0 {
			sleep = backoff
		}
		next := time.Now().Add(sleep)
		link.setStatus(StatusBackoff, errStr, next)

		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// dialOnce dials the peer, performs challenge-response auth, and runs the
// shared session loop. Returns when the session ends or auth fails.
func (s *LinkSupervisor) dialOnce(ctx context.Context, link *peerLink) error {
	addr := link.peer.Address
	if addr == "" {
		return fmt.Errorf("peer has no address")
	}
	u := &url.URL{Scheme: "ws", Host: addr, Path: "/ws/peer"}

	dialer := &websocket.Dialer{
		Proxy:            websocket.DefaultDialer.Proxy,
		HandshakeTimeout: 45 * time.Second,
		ReadBufferSize:   1024 * 32,
		WriteBufferSize:  1024 * 32,
	}
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", u.String(), err)
	}

	// Auth: read challenge, sign, send response, read result.
	var challengeMsg Message
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := conn.ReadJSON(&challengeMsg); err != nil {
		conn.Close()
		return fmt.Errorf("read challenge: %w", err)
	}
	if challengeMsg.Type != MsgChallenge {
		conn.Close()
		return fmt.Errorf("expected challenge got %s", challengeMsg.Type)
	}
	var ch ChallengePayload
	if err := json.Unmarshal(challengeMsg.Payload, &ch); err != nil {
		conn.Close()
		return fmt.Errorf("parse challenge: %w", err)
	}
	challengeBytes, err := base64.StdEncoding.DecodeString(ch.Challenge)
	if err != nil {
		conn.Close()
		return fmt.Errorf("decode challenge: %w", err)
	}
	sig, err := s.deps.Identity.Sign(challengeBytes)
	if err != nil {
		conn.Close()
		return fmt.Errorf("sign: %w", err)
	}
	authMsg, _ := NewMessage(MsgAuth, AuthPayload{
		PublicKey:    s.deps.Identity.PublicKey,
		Signature:    base64.StdEncoding.EncodeToString(sig),
		Capabilities: capabilitiesFor(s.deps),
	})
	if err := conn.WriteJSON(authMsg); err != nil {
		conn.Close()
		return fmt.Errorf("send auth: %w", err)
	}
	var result Message
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := conn.ReadJSON(&result); err != nil {
		conn.Close()
		return fmt.Errorf("read auth result: %w", err)
	}
	conn.SetReadDeadline(time.Time{})
	if result.Type == MsgAuthFail {
		var reason struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(result.Payload, &reason)
		conn.Close()
		return fmt.Errorf("%s", reason.Reason)
	}
	var peerCaps []string
	if result.Type != MsgAuthOK {
		conn.Close()
		return fmt.Errorf("unexpected auth response: %s", result.Type)
	}
	if len(result.Payload) > 0 {
		var okPayload AuthOKPayload
		if err := json.Unmarshal(result.Payload, &okPayload); err == nil {
			peerCaps = okPayload.Capabilities
		}
	}

	// v2-only nodes have no legacy state-sync fallback: reject peers that
	// don't advertise both v2 capabilities rather than silently degrading to
	// (nonexistent) legacy per-field sync. Mirrors the listener-side check
	// in handler.go's HandlePeer.
	if requiresV2Peer(s.deps) && !peerCapsSatisfyV2(peerCaps) {
		logrus.WithFields(logrus.Fields{"peer": link.peer.Name, "addr": addr, "caps": peerCaps}).Warn("rejecting peer: this node is v2-only and remote peer lacks required v2 capabilities")
		conn.Close()
		return fmt.Errorf("peer %s lacks required v2 capabilities", link.peer.Name)
	}

	// Auth ok.
	_ = s.deps.PeerStore.UpdateLastSeen(link.peer.PublicKey)
	link.setStatus(StatusConnected, "", time.Time{})

	logrus.WithFields(logrus.Fields{"peer": link.peer.Name, "addr": addr}).Info("dialer connected")

	err = runSession(ctx, RoleDialer, conn, link.peer, addr, peerCaps, s.deps)
	conn.Close()
	return err
}
