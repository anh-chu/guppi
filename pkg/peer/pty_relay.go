package peer

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// PTYRelay bridges browser WebSocket connections to peer PTY WebSocket connections
type PTYRelay struct {
	mu      sync.RWMutex
	pending map[string]*PendingStream // stream_id -> waiting for peer to connect
	active  map[string]*ActiveBridge  // stream_id -> both sides connected
}

// PendingStream is a browser waiting for a peer to open a PTY WebSocket
type PendingStream struct {
	StreamID  string
	HostID    string
	BrowserWS *websocket.Conn
	Ready     chan *websocket.Conn // peer sends its WS connection here
}

// ActiveBridge is an active PTY relay between browser and peer
type ActiveBridge struct {
	StreamID  string
	BrowserWS *websocket.Conn
	PeerWS    *websocket.Conn
}

// NewPTYRelay creates a new PTY relay
func NewPTYRelay() *PTYRelay {
	return &PTYRelay{
		pending: make(map[string]*PendingStream),
		active:  make(map[string]*ActiveBridge),
	}
}

// GenerateStreamID creates a random stream ID
func GenerateStreamID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// RegisterPending registers a browser connection waiting for a peer PTY
func (r *PTYRelay) RegisterPending(streamID, hostID string, browserWS *websocket.Conn) *PendingStream {
	ps := &PendingStream{
		StreamID:  streamID,
		HostID:    hostID,
		BrowserWS: browserWS,
		Ready:     make(chan *websocket.Conn, 1),
	}

	r.mu.Lock()
	r.pending[streamID] = ps
	r.mu.Unlock()

	return ps
}

// CompletePending is called when a peer connects its PTY WebSocket for a stream
func (r *PTYRelay) CompletePending(streamID string, peerWS *websocket.Conn) bool {
	r.mu.Lock()
	ps, ok := r.pending[streamID]
	if ok {
		delete(r.pending, streamID)
		r.active[streamID] = &ActiveBridge{
			StreamID:  streamID,
			BrowserWS: ps.BrowserWS,
			PeerWS:    peerWS,
		}
	}
	r.mu.Unlock()

	if ok {
		ps.Ready <- peerWS
		return true
	}
	return false
}

// Remove removes a stream from tracking
func (r *PTYRelay) Remove(streamID string) {
	r.mu.Lock()
	delete(r.pending, streamID)
	delete(r.active, streamID)
	r.mu.Unlock()
}

// HandlePeerPTY handles the /ws/peer-pty endpoint where peers connect their PTY WebSocket
func (r *PTYRelay) HandlePeerPTY(w http.ResponseWriter, req *http.Request) {
	streamID := req.URL.Query().Get("stream")
	if streamID == "" {
		http.Error(w, "missing stream ID", http.StatusBadRequest)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, req, nil)
	if err != nil {
		logrus.WithError(err).Warn("peer-pty ws upgrade failed")
		return
	}

	log := logrus.WithField("stream", streamID)

	if !r.CompletePending(streamID, conn) {
		log.Warn("no pending stream for peer PTY connection")
		conn.Close()
		return
	}

	log.Debug("peer PTY connection established, relay is handled by the session handler")
	// The actual relay is driven by HandleRemoteSession — this handler just
	// registers the peer WS. The conn stays open; the relay goroutines in
	// HandleRemoteSession read/write it.
}

// Bridge runs the bidirectional relay between browser and peer WebSocket connections.
// This blocks until one side disconnects.
func Bridge(browserWS, peerWS *websocket.Conn, streamID string) {
	log := logrus.WithField("stream", streamID)

	done := make(chan struct{}, 2)

	// Peer → Browser (PTY output)
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			msgType, data, err := peerWS.ReadMessage()
			if err != nil {
				return
			}
			if err := browserWS.WriteMessage(msgType, data); err != nil {
				return
			}
		}
	}()

	// Browser → Peer (keyboard input + resize)
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			msgType, data, err := browserWS.ReadMessage()
			if err != nil {
				return
			}
			if err := peerWS.WriteMessage(msgType, data); err != nil {
				return
			}
		}
	}()

	// Wait for either side to finish
	<-done

	// Close both sides
	browserWS.Close()
	peerWS.Close()

	// Wait for the other goroutine
	<-done

	log.Debug("PTY relay bridge closed")
}
