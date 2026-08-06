package peer

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/identity"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
	ReadBufferSize:  1024 * 32,
	WriteBufferSize: 1024 * 32,
}

// wsStreamUpgrader is the dedicated upgrader for /ws/peer-stream.
// It negotiates permessage-deflate so the host can compress PTY output,
// while the shared-control /ws/peer upgrader remains uncompressed.
var wsStreamUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
	ReadBufferSize:    1024 * 32,
	WriteBufferSize:   1024 * 32,
	EnableCompression: true,
}

// Handler handles incoming peer WebSocket connections on /ws/peer.
type Handler struct {
	deps      SessionDeps
	streamReg *StreamRegistry
}

// NewHandler creates a new peer connection handler.
func NewHandler(deps SessionDeps, streamReg *StreamRegistry) *Handler {
	return &Handler{deps: deps, streamReg: streamReg}
}

// HandlePeer handles /ws/peer for incoming control-channel connections.
// Performs ed25519 challenge-response auth, then delegates to runSession.
func (h *Handler) HandlePeer(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		logrus.WithError(err).Warn("peer ws upgrade failed")
		return
	}
	// Note: do NOT defer conn.Close() — runSession owns it.

	log := logrus.WithField("remote", r.RemoteAddr)
	ctx, cancel := context.WithTimeout(r.Context(), streamSetupTimeout)
	defer cancel()

	peer, caps, ok := h.authenticatePeer(ctx, conn, log)
	if !ok {
		return
	}

	// Every connected peer must present both canonical capabilities: there is
	// no legacy state-sync fallback to degrade to, so a peer missing either
	// CapCatalogV1 or CapCommandV1 cannot be synchronized with at all and the
	// connection is rejected outright.
	if !peerCapsSatisfyCanonical(caps) {
		log.WithFields(logrus.Fields{"peer": peer.Name, "id": peer.Fingerprint(), "caps": caps}).Warn("rejecting peer: missing required canonical capabilities")
		sendAuthFail(conn, "canonical capabilities required")
		conn.Close()
		return
	}

	// Race resolution: if we already have a live connection for this peer,
	// reject the newer one with "already connected" so the dialer flips role.
	if h.deps.Manager.HasLiveConnection(peer.Fingerprint()) {
		log.WithFields(logrus.Fields{"peer": peer.Name, "id": peer.Fingerprint()}).Warn("rejecting redial: already have live connection (stale conn would block recovery)")
		sendAuthFail(conn, "already connected")
		conn.Close()
		return
	}

	authOK, _ := NewMessage(MsgAuthOK, AuthOKPayload{Capabilities: capabilitiesFor(h.deps)})
	if err := conn.WriteJSON(authOK); err != nil {
		conn.Close()
		return
	}

	log.WithFields(logrus.Fields{"peer": peer.Name, "id": peer.Fingerprint()}).Info("peer authenticated (listener)")

	// runSession does atomic TryRegisterPeer; residual race window closes
	// there (returns immediately if a connection already exists).
	//
	// We pass peer.Address (the dialer's listening host:port stored from
	// bootstrap), NOT r.RemoteAddr — the latter is the dialer's ephemeral
	// source port, which is useless for PTY back-dial.
	_ = runSession(r.Context(), RoleListener, conn, *peer, peer.Address, caps, h.deps)
}

// HandlePeerStream handles /ws/peer-stream for dedicated PTY data connections.
func (h *Handler) HandlePeerStream(w http.ResponseWriter, r *http.Request) {
	conn, err := wsStreamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		logrus.WithError(err).Warn("peer stream ws upgrade failed")
		return
	}

	log := logrus.WithField("remote", r.RemoteAddr)
	ctx, cancel := context.WithTimeout(r.Context(), streamSetupTimeout)
	defer cancel()

	peer, _, ok := h.authenticatePeer(ctx, conn, log)
	if !ok {
		return
	}

	authOK, _ := NewMessage(MsgAuthOK, AuthOKPayload{Capabilities: capabilitiesFor(h.deps)})
	if err := conn.WriteJSON(authOK); err != nil {
		conn.Close()
		return
	}

	// Default writes uncompressed — host role re-enables in BridgePTY path.
	conn.EnableWriteCompression(false)

	conn.SetReadDeadline(streamDeadline(ctx, streamSetupTimeout))
	var msg Message
	if err := conn.ReadJSON(&msg); err != nil {
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})
	if msg.Type != MsgStreamToken {
		sendAuthFail(conn, "unknown or expired stream")
		conn.Close()
		return
	}

	var tp StreamTokenPayload
	if err := json.Unmarshal(msg.Payload, &tp); err != nil || tp.Token == "" {
		sendAuthFail(conn, "unknown or expired stream")
		conn.Close()
		return
	}
	if h.streamReg == nil {
		conn.Close()
		return
	}

	ps, err := h.streamReg.Claim(ctx, tp.Token, peer.Fingerprint())
	if err != nil {
		sendAuthFail(conn, "unknown or expired stream")
		conn.Close()
		return
	}
	h.streamReg.Resolve(ps, conn)
}

// authenticatePeer runs the ed25519 challenge-response and returns the
// verified peer. It does NOT send MsgAuthOK, does NOT touch the Manager,
// and does NOT check for duplicate connections — callers decide.
func (h *Handler) authenticatePeer(ctx context.Context, conn *websocket.Conn, log *logrus.Entry) (*identity.Peer, []string, bool) {
	challengeBytes := make([]byte, 32)
	if _, err := rand.Read(challengeBytes); err != nil {
		log.WithError(err).Error("failed to generate challenge")
		conn.Close()
		return nil, nil, false
	}
	challengeB64 := base64.StdEncoding.EncodeToString(challengeBytes)
	challengeMsg, _ := NewMessage(MsgChallenge, ChallengePayload{Challenge: challengeB64})
	if err := conn.WriteJSON(challengeMsg); err != nil {
		log.WithError(err).Debug("failed to send challenge")
		conn.Close()
		return nil, nil, false
	}

	conn.SetReadDeadline(streamDeadline(ctx, streamSetupTimeout))
	var authMsg Message
	if err := conn.ReadJSON(&authMsg); err != nil {
		log.WithError(err).Debug("failed to read auth")
		conn.Close()
		return nil, nil, false
	}
	conn.SetReadDeadline(time.Time{})

	if authMsg.Type != MsgAuth {
		sendAuthFail(conn, "expected auth message")
		conn.Close()
		return nil, nil, false
	}

	var authPayload AuthPayload
	if err := json.Unmarshal(authMsg.Payload, &authPayload); err != nil {
		sendAuthFail(conn, "invalid auth payload")
		conn.Close()
		return nil, nil, false
	}

	peer := h.deps.PeerStore.GetByPublicKey(authPayload.PublicKey)
	if peer == nil {
		sendAuthFail(conn, "unknown peer")
		conn.Close()
		return nil, nil, false
	}

	sig, err := base64.StdEncoding.DecodeString(authPayload.Signature)
	if err != nil {
		sendAuthFail(conn, "invalid signature encoding")
		conn.Close()
		return nil, nil, false
	}
	if !identity.Verify(authPayload.PublicKey, challengeBytes, sig) {
		sendAuthFail(conn, "invalid signature")
		conn.Close()
		return nil, nil, false
	}

	return peer, authPayload.Capabilities, true
}

func sendAuthFail(conn *websocket.Conn, reason string) {
	msg, _ := NewMessage(MsgAuthFail, map[string]string{"reason": reason})
	_ = conn.WriteJSON(msg)
}
