package ws

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/common"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

var upgrader = websocket.Upgrader{
	CheckOrigin:     CheckSameOrigin,
	ReadBufferSize:  1024,
	WriteBufferSize: 1024 * 16,
}

// CheckSameOrigin validates that the Origin header matches the Host header,
// preventing cross-site WebSocket hijacking from malicious web pages.
func CheckSameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser clients (curl, CLI) don't send Origin
	}
	// Allow connections from loopback — dev proxy (e.g. Vite) runs on localhost
	// and forwards requests with a different origin than the server host.
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return true
		}
	}
	// Parse the origin to extract the host
	// Origin format: scheme://host[:port]
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// client wraps a WebSocket connection with a per-connection write mutex
type client struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

// Hub manages WebSocket connections for state events and tool events
type Hub struct {
	mu      sync.RWMutex
	clients map[*client]bool
	tracker *toolevents.Tracker
}

// NewHub creates a new WebSocket hub.
func NewHub(tracker *toolevents.Tracker) *Hub {
	return &Hub{
		clients: make(map[*client]bool),
		tracker: tracker,
	}
}


// Run starts broadcasting state events and tool events to connected clients.
// Blocks until ctx is cancelled or its subscriptions are closed.
func (h *Hub) Run(ctx context.Context) {
	toolCh := h.tracker.Subscribe()
	defer h.tracker.Unsubscribe(toolCh)

	for {
		select {
		case <-ctx.Done():
			return

		case evt, ok := <-toolCh:
			if !ok {
				return
			}
			if evt.Kind == "artifact" {
				h.broadcastArtifactEvent(evt)
				continue
			}
			// Wrap tool event with a type prefix so frontend can distinguish.
			// Include additional context fields (cwd, agent_session_id, user_prompt, agent_message, files).
			wrapped := map[string]interface{}{
				"type":              "tool-event",
				"tool":              evt.Tool,
				"status":            evt.Status,
				"host":              evt.Host,
				"host_name":         evt.HostName,
				"session":           evt.Session,
				"session_id":        evt.SessionID,
				"window":            evt.Window,
				"pane":              evt.Pane,
				"message":           evt.Message,
				"cwd":               evt.CWD,
				"agent_session_id":  evt.AgentSessionID,
				"user_prompt":       evt.UserPrompt,
				"agent_message":     evt.AgentMessage,
				"files":             evt.Files,
				"timestamp":         evt.Timestamp,
				"auto_detected":     evt.AutoDetected,
				"artifacts":         evt.Artifacts,
			}
			data, err := json.Marshal(wrapped)
			if err != nil {
				logrus.WithError(err).Warn("failed to marshal tool event")
				continue
			}
			logrus.WithFields(logrus.Fields{
				"tool": evt.Tool, "status": evt.Status, "session": evt.Session,
				"pane": evt.Pane, "auto_detected": evt.AutoDetected,
			}).Trace("hub: broadcasting tool event to WebSocket clients")
			h.broadcastMessage(data)
		}
	}
}

func (h *Hub) broadcastArtifactEvent(evt *toolevents.Event) {
	if evt == nil {
		return
	}
	wrapped := map[string]interface{}{
		"type":      "artifacts",
		"host":      evt.Host,
		"session":   evt.Session,
		"artifacts": evt.Artifacts,
	}
	data, err := json.Marshal(wrapped)
	if err != nil {
		logrus.WithError(err).Warn("failed to marshal artifact event")
		return
	}
	logrus.WithFields(logrus.Fields{
		"session": evt.Session,
		"count":   len(evt.Artifacts),
		"host":    evt.Host,
	}).Trace("hub: broadcasting artifact event to WebSocket clients")
	h.broadcastMessage(data)
}


// BroadcastJSON marshals v and sends it to every connected client. Failed
// connections are pruned.
func (h *Hub) BroadcastJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		logrus.WithError(err).Warn("failed to marshal broadcast")
		return
	}
	h.broadcastMessage(data)
}

// broadcastMessage sends a message to all connected clients
func (h *Hub) broadcastMessage(data []byte) {
	h.mu.RLock()
	// Snapshot clients under read lock
	snapshot := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		snapshot = append(snapshot, c)
	}
	h.mu.RUnlock()

	var failed []*client
	for _, c := range snapshot {
		c.mu.Lock()
		err := c.conn.WriteMessage(websocket.TextMessage, data)
		c.mu.Unlock()
		if err != nil {
			logrus.WithError(err).Debug("failed to write to ws client")
			failed = append(failed, c)
		}
	}

	if len(failed) > 0 {
		h.mu.Lock()
		for _, c := range failed {
			c.conn.Close()
			delete(h.clients, c)
		}
		h.mu.Unlock()
	}
}

// HandleEvents handles WebSocket connections for state event streaming
func (h *Hub) HandleEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logrus.WithError(err).Warn("ws upgrade failed")
		return
	}

	c := &client{conn: conn}

	// Send welcome message with server version
	welcome, _ := json.Marshal(map[string]string{
		"type":    "welcome",
		"version": common.VERSION,
		"commit":  common.COMMIT,
	})
	c.mu.Lock()
	_ = c.conn.WriteMessage(websocket.TextMessage, welcome)
	c.mu.Unlock()

	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()

	logrus.Debug("state ws client connected")

	// Keep connection alive by reading (and discarding) client messages
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}

	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	conn.Close()
	logrus.Debug("state ws client disconnected")
}
