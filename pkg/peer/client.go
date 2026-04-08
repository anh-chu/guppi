package peer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/ekristen/guppi/pkg/activity"
	"github.com/ekristen/guppi/pkg/common"
	"github.com/ekristen/guppi/pkg/identity"
	"github.com/ekristen/guppi/pkg/state"
	"github.com/ekristen/guppi/pkg/stats"
	"github.com/ekristen/guppi/pkg/tmux"
	"github.com/ekristen/guppi/pkg/toolevents"
)

// hasScheme checks if an address string starts with a known URL scheme.
func hasScheme(addr string) bool {
	for _, prefix := range []string{"ws://", "wss://", "http://", "https://"} {
		if len(addr) > len(prefix) && addr[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// Client connects to a hub and syncs local state
type Client struct {
	hubURL      string
	identity    *identity.Identity
	peerStore   *identity.PeerStore
	localMgr    *state.Manager
	peerMgr     *Manager
	actTracker  *activity.Tracker
	toolTracker *toolevents.Tracker
	tmuxClient  *tmux.Client
	insecure    bool

	mu             sync.Mutex
	conn           *websocket.Conn
	pendingCertPEM string // set when hub cert changed; cleared after auth succeeds

	ptyManager *PTYManager
}

// NewClient creates a new peer client
func NewClient(hubURL string, id *identity.Identity, peerStore *identity.PeerStore,
	localMgr *state.Manager, peerMgr *Manager, actTracker *activity.Tracker,
	toolTracker *toolevents.Tracker, tmuxPath string, insecure bool) *Client {

	tmuxClient, _ := tmux.NewClient()
	c := &Client{
		hubURL:      hubURL,
		identity:    id,
		peerStore:   peerStore,
		localMgr:    localMgr,
		peerMgr:     peerMgr,
		actTracker:  actTracker,
		toolTracker: toolTracker,
		tmuxClient:  tmuxClient,
		insecure:    insecure,
	}
	c.ptyManager = NewPTYManager(tmuxPath, actTracker, c)
	return c
}

// getCACert returns the CA certificate PEM for the hub peer, if any.
func (c *Client) getCACert() string {
	peers := c.peerStore.List()
	for _, p := range peers {
		if p.CACertPEM != "" {
			return p.CACertPEM
		}
	}
	return ""
}

// getPinnedCert returns the pinned TLS certificate PEM for the hub peer, if any.
func (c *Client) getPinnedCert() string {
	peers := c.peerStore.List()
	for _, p := range peers {
		if p.TLSCertPEM != "" {
			return p.TLSCertPEM
		}
	}
	return ""
}

// tlsConfig returns the TLS configuration for connecting to the hub.
// Trust priority:
// 1. System CAs (LE, Tailscale)
// 2. CACertPEM — standard RootCA verification (no pin rotation needed)
// 3. TLSCertPEM — legacy pinned cert + pin-rotation
// 4. Reject
func (c *Client) tlsConfig() *tls.Config {
	if c.insecure {
		return &tls.Config{InsecureSkipVerify: true}
	}

	// If we have a CA cert, use standard x509 verification — no pin rotation needed
	caCertPEM := c.getCACert()
	if caCertPEM != "" {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM([]byte(caCertPEM)) {
			return &tls.Config{RootCAs: pool}
		}
	}

	// Legacy path: pinned cert with pin-rotation support
	pinnedPEM := c.getPinnedCert()

	return &tls.Config{
		InsecureSkipVerify: true, // we do our own verification in VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("hub presented no certificates")
			}

			leaf := cs.PeerCertificates[0]
			leafPEM := encodeCertPEM(leaf)

			// 1. Check system CAs
			if isSystemTrusted(cs) {
				c.mu.Lock()
				c.pendingCertPEM = ""
				c.mu.Unlock()
				return nil
			}

			// 2. Pinned cert matches — all good
			if pinnedPEM != "" && leafPEM == pinnedPEM {
				c.mu.Lock()
				c.pendingCertPEM = ""
				c.mu.Unlock()
				return nil
			}

			// 3. Cert changed — allow handshake, flag for post-auth pin update
			if pinnedPEM != "" {
				c.mu.Lock()
				c.pendingCertPEM = leafPEM
				c.mu.Unlock()
				return nil
			}

			// 4. No pin and not system-trusted — reject
			return fmt.Errorf("hub certificate not trusted (not pinned, not system CA)")
		},
	}
}

// TLSConfig returns the TLS configuration (exported for PTYManager)
func (c *Client) TLSConfig() *tls.Config {
	return c.tlsConfig()
}

// Run connects to the hub and maintains the connection with reconnection
func (c *Client) Run(ctx context.Context) {
	log := logrus.WithField("hub", c.hubURL)

	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		connStart := time.Now()
		err := c.connectAndRun(ctx)
		if err != nil {
			log.WithError(err).Warn("hub connection lost")
		}

		// Reset backoff if the connection was up for a reasonable duration
		if time.Since(connStart) > 30*time.Second {
			backoff = time.Second
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (c *Client) connectAndRun(ctx context.Context) error {
	log := logrus.WithField("hub", c.hubURL)

	// Build WebSocket URL — normalize bare host:port to a full URL
	hubAddr := c.hubURL
	if !hasScheme(hubAddr) {
		hubAddr = "wss://" + hubAddr
	}
	u, err := url.Parse(hubAddr)
	if err != nil {
		return fmt.Errorf("parse hub URL: %w", err)
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else if u.Scheme == "http" {
		u.Scheme = "ws"
	}
	u.Path = "/ws/peer"

	dialer := websocket.DefaultDialer
	if tlsCfg := c.tlsConfig(); tlsCfg != nil {
		dialer = &websocket.Dialer{
			TLSClientConfig: tlsCfg,
		}
	}

	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}
	defer conn.Close()

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
	}()

	log.Info("connected to hub")

	// Step 1: Read challenge
	var challengeMsg Message
	if err := conn.ReadJSON(&challengeMsg); err != nil {
		return fmt.Errorf("read challenge: %w", err)
	}
	if challengeMsg.Type != MsgChallenge {
		return fmt.Errorf("expected challenge, got %s", challengeMsg.Type)
	}

	var challenge ChallengePayload
	if err := json.Unmarshal(challengeMsg.Payload, &challenge); err != nil {
		return fmt.Errorf("parse challenge: %w", err)
	}

	challengeBytes, err := base64.StdEncoding.DecodeString(challenge.Challenge)
	if err != nil {
		return fmt.Errorf("decode challenge: %w", err)
	}

	// Step 2: Sign and respond
	sig, err := c.identity.Sign(challengeBytes)
	if err != nil {
		return fmt.Errorf("sign challenge: %w", err)
	}

	authMsg, _ := NewMessage(MsgAuth, AuthPayload{
		PublicKey: c.identity.PublicKey,
		Signature: base64.StdEncoding.EncodeToString(sig),
	})
	if err := conn.WriteJSON(authMsg); err != nil {
		return fmt.Errorf("send auth: %w", err)
	}

	// Step 3: Read auth result
	var resultMsg Message
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := conn.ReadJSON(&resultMsg); err != nil {
		return fmt.Errorf("read auth result: %w", err)
	}
	conn.SetReadDeadline(time.Time{})

	if resultMsg.Type == MsgAuthFail {
		return fmt.Errorf("authentication failed")
	}
	if resultMsg.Type != MsgAuthOK {
		return fmt.Errorf("unexpected message: %s", resultMsg.Type)
	}

	log.Info("authenticated with hub")

	// Ed25519 auth succeeded — if cert changed, update the pin
	c.mu.Lock()
	pendingCert := c.pendingCertPEM
	c.pendingCertPEM = ""
	c.mu.Unlock()
	if pendingCert != "" {
		peers := c.peerStore.List()
		for _, p := range peers {
			if p.TLSCertPEM != "" {
				if err := c.peerStore.UpdateTLSCert(p.PublicKey, pendingCert); err != nil {
					log.WithError(err).Warn("failed to update hub TLS certificate pin")
				} else {
					log.Info("updated hub TLS certificate pin after successful auth")
				}
				break
			}
		}
	}

	// Configure ping/pong for connection liveness detection
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		return nil
	})
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	// Send initial state
	c.sendStateUpdate(conn)

	// Start periodic senders
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()

	go c.pingLoop(ctx2, conn)
	go c.periodicActivity(ctx2, conn)
	go c.periodicStats(ctx2, conn)
	go c.forwardStateEvents(ctx2, conn)
	go c.forwardToolEvents(ctx2, conn)

	// Read loop: process messages from hub
	for {
		var msg Message
		if err := conn.ReadJSON(&msg); err != nil {
			return fmt.Errorf("read from hub: %w", err)
		}

		c.handleHubMessage(&msg, conn, log)
	}
}

func (c *Client) handleHubMessage(msg *Message, conn *websocket.Conn, log *logrus.Entry) {
	switch msg.Type {
	case MsgPeerState:
		var payload PeerStatePayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			log.WithError(err).Debug("invalid peer-state")
			return
		}
		// Update peer manager with remote hosts
		for _, host := range payload.Hosts {
			if c.peerMgr.IsLocal(host.ID) {
				continue
			}
			c.peerMgr.UpdatePeerSessions(host.ID, host.Sessions)
			if host.Online {
				// Register host if not already known
				if !c.peerMgr.HasHost(host.ID) {
					c.peerMgr.RegisterPeer(host.ID, host.Name, "", nil)
				}
			}
		}

	case MsgPeerConnected:
		var payload PeerNotifyPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return
		}
		c.peerMgr.RegisterPeer(payload.ID, payload.Name, "", nil)

	case MsgPeerDisconnected:
		var payload PeerNotifyPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return
		}
		c.peerMgr.UnregisterPeer(payload.ID)

	case MsgPTYOpen:
		var payload PTYOpenPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			log.WithError(err).Debug("invalid pty-open")
			return
		}
		go c.ptyManager.Open(payload)

	case MsgPTYClose:
		var payload PTYClosePayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return
		}
		c.ptyManager.Close(payload.StreamID)

	case MsgPTYResize:
		var payload PTYResizePayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return
		}
		c.ptyManager.Resize(payload.StreamID, payload.Cols, payload.Rows)

	case MsgSessionAction:
		var payload SessionActionPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return
		}
		c.handleSessionAction(&payload, conn, log)

	case MsgRequestState:
		c.sendStateUpdate(conn)

	default:
		log.WithField("type", msg.Type).Debug("unknown message from hub")
	}
}

func (c *Client) handleSessionAction(payload *SessionActionPayload, conn *websocket.Conn, log *logrus.Entry) {
	if c.tmuxClient == nil {
		log.Warn("no tmux client available for session action")
		return
	}

	switch payload.Action {
	case "new":
		var params struct {
			Name    string `json:"name"`
			Path    string `json:"path,omitempty"`
			Command string `json:"command,omitempty"`
		}
		if err := json.Unmarshal(payload.Params, &params); err != nil || params.Name == "" {
			log.WithError(err).Debug("invalid new session params")
			return
		}
		if err := c.tmuxClient.NewSession(params.Name, params.Path, params.Command); err != nil {
			log.WithError(err).Warn("failed to create session on peer")
			return
		}
		// Send updated state so hub sees the new session
		c.sendStateUpdate(conn)

	case "rename":
		var params struct {
			OldName string `json:"old_name"`
			NewName string `json:"new_name"`
		}
		if err := json.Unmarshal(payload.Params, &params); err != nil {
			return
		}
		if err := c.tmuxClient.RenameSession(params.OldName, params.NewName); err != nil {
			log.WithError(err).Warn("failed to rename session on peer")
			return
		}
		c.sendStateUpdate(conn)

	case "select-window":
		var params struct {
			Session string `json:"session"`
			Window  int    `json:"window"`
			Pane    string `json:"pane,omitempty"`
		}
		if err := json.Unmarshal(payload.Params, &params); err != nil {
			return
		}
		c.tmuxClient.SelectWindow(params.Session, fmt.Sprintf("%d", params.Window))
		if params.Pane != "" {
			c.tmuxClient.SelectPane(params.Pane)
		}

	default:
		log.WithField("action", payload.Action).Debug("unknown session action")
	}
}

func (c *Client) sendStateUpdate(conn *websocket.Conn) {
	sessions := c.localMgr.GetSessions()
	msg, err := NewMessage(MsgStateUpdate, StateUpdatePayload{Sessions: sessions, Version: common.VERSION})
	if err != nil {
		return
	}
	c.writeJSON(conn, msg)
}

func (c *Client) forwardStateEvents(ctx context.Context, conn *websocket.Conn) {
	ch := c.localMgr.Subscribe()
	defer c.localMgr.Unsubscribe(ch)

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			msg, err := NewMessage(MsgStateEvent, StateEventPayload{
				EventType: evt.Type,
				Session:   evt.Session,
			})
			if err != nil {
				continue
			}
			c.writeJSON(conn, msg)

			// Also send full state update on change
			c.sendStateUpdate(conn)
		}
	}
}

func (c *Client) forwardToolEvents(ctx context.Context, conn *websocket.Conn) {
	ch := c.toolTracker.Subscribe()
	defer c.toolTracker.Unsubscribe(ch)

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			logrus.WithFields(logrus.Fields{
				"tool":    evt.Tool,
				"status":  evt.Status,
				"session": evt.Session,
			}).Debug("forwarding tool event to hub")
			msg, err := NewMessage(MsgToolEvent, ToolEventPayload{Event: evt})
			if err != nil {
				continue
			}
			c.writeJSON(conn, msg)
		}
	}
}

func (c *Client) pingLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			c.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (c *Client) periodicActivity(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snapshots := c.actTracker.GetAll()
			// Stamp each snapshot with our host ID so the hub can key them correctly
			localID := c.peerMgr.LocalID()
			for _, s := range snapshots {
				if s.Host == "" {
					s.Host = localID
				}
			}
			msg, err := NewMessage(MsgActivityUpdate, ActivityUpdatePayload{Snapshots: snapshots})
			if err != nil {
				continue
			}
			c.writeJSON(conn, msg)
		}
	}
}

func (c *Client) collectStats() map[string]interface{} {
	s := stats.SystemStats()
	sessions := c.localMgr.GetSessions()
	s["processes"] = stats.ProcessCountsFromSessions(sessions)
	return s
}

func (c *Client) periodicStats(ctx context.Context, conn *websocket.Conn) {
	// Send immediately on connect
	if msg, err := NewMessage(MsgStats, StatsPayload{Stats: c.collectStats()}); err == nil {
		c.writeJSON(conn, msg)
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if msg, err := NewMessage(MsgStats, StatsPayload{Stats: c.collectStats()}); err == nil {
				c.writeJSON(conn, msg)
			}
		}
	}
}

func (c *Client) writeJSON(conn *websocket.Conn, msg *Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn.WriteJSON(msg)
}

// HubURL returns the hub URL for PTY connections
func (c *Client) HubURL() string {
	return c.hubURL
}

// Insecure returns whether TLS verification is skipped
func (c *Client) Insecure() bool {
	return c.insecure
}

// encodeCertPEM encodes an x509.Certificate to PEM format.
func encodeCertPEM(cert *x509.Certificate) string {
	block := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	}
	return string(pem.EncodeToMemory(block))
}

// isSystemTrusted verifies the peer certificate chain against system root CAs.
func isSystemTrusted(cs tls.ConnectionState) bool {
	if len(cs.PeerCertificates) == 0 {
		return false
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		return false
	}
	opts := x509.VerifyOptions{
		Roots:         pool,
		Intermediates: x509.NewCertPool(),
	}
	for _, cert := range cs.PeerCertificates[1:] {
		opts.Intermediates.AddCert(cert)
	}
	_, err = cs.PeerCertificates[0].Verify(opts)
	return err == nil
}
