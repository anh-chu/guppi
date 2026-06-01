package peer

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/ekristen/guppi/pkg/identity"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
	ReadBufferSize:  1024,
	WriteBufferSize: 1024 * 16,
}

// Handler handles incoming peer WebSocket connections on /ws/peer.
type Handler struct {
	deps SessionDeps
}

// NewHandler creates a new peer connection handler.
func NewHandler(deps SessionDeps) *Handler {
	return &Handler{deps: deps}
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

	// Send challenge.
	challengeBytes := make([]byte, 32)
	if _, err := rand.Read(challengeBytes); err != nil {
		log.WithError(err).Error("failed to generate challenge")
		conn.Close()
		return
	}
	challengeB64 := base64.StdEncoding.EncodeToString(challengeBytes)
	challengeMsg, _ := NewMessage(MsgChallenge, ChallengePayload{Challenge: challengeB64})
	if err := conn.WriteJSON(challengeMsg); err != nil {
		log.WithError(err).Debug("failed to send challenge")
		conn.Close()
		return
	}

	// Read auth.
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var authMsg Message
	if err := conn.ReadJSON(&authMsg); err != nil {
		log.WithError(err).Debug("failed to read auth")
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})

	if authMsg.Type != MsgAuth {
		sendAuthFail(conn, "expected auth message")
		conn.Close()
		return
	}

	var authPayload AuthPayload
	if err := json.Unmarshal(authMsg.Payload, &authPayload); err != nil {
		sendAuthFail(conn, "invalid auth payload")
		conn.Close()
		return
	}

	peer := h.deps.PeerStore.GetByPublicKey(authPayload.PublicKey)
	if peer == nil {
		sendAuthFail(conn, "unknown peer")
		conn.Close()
		return
	}

	sig, err := base64.StdEncoding.DecodeString(authPayload.Signature)
	if err != nil {
		sendAuthFail(conn, "invalid signature encoding")
		conn.Close()
		return
	}
	if !identity.Verify(authPayload.PublicKey, challengeBytes, sig) {
		sendAuthFail(conn, "invalid signature")
		conn.Close()
		return
	}

	// Race resolution: if we already have a live connection for this peer,
	// reject the newer one with "already connected" so the dialer flips role.
	if h.deps.Manager.HasLiveConnection(peer.Fingerprint()) {
		sendAuthFail(conn, "already connected")
		conn.Close()
		return
	}

	authOK, _ := NewMessage(MsgAuthOK, nil)
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
	_ = runSession(r.Context(), RoleListener, conn, *peer, peer.Address, h.deps)
}

func sendAuthFail(conn *websocket.Conn, reason string) {
	msg, _ := NewMessage(MsgAuthFail, map[string]string{"reason": reason})
	_ = conn.WriteJSON(msg)
}
