package peer

import (
	"encoding/json"
	"time"

	"github.com/anh-chu/termyard/pkg/activity"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/state"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

// Message types sent from peer to hub over control WebSocket
const (
	// MsgAuth is the challenge-response auth message
	MsgAuth = "auth"
	// MsgToolEvent forwards a local tool event
	MsgToolEvent = "tool-event"
	// MsgSessionRuntime sends periodic session runtime snapshots (volatile state)
	MsgSessionRuntime = "session-runtime"
	// MsgStats sends system stats
	MsgStats = "stats"
	// MsgCapturePaneResult returns a capture-pane snapshot to the hub.
	MsgCapturePaneResult = "capture-pane-result"
)

// Message types sent from hub to peer over control WebSocket
const (
	// MsgChallenge is the auth challenge from hub
	MsgChallenge = "challenge"
	// MsgAuthOK indicates successful authentication
	MsgAuthOK = "auth-ok"
	// MsgAuthFail indicates failed authentication
	MsgAuthFail = "auth-fail"
	// MsgPeerConnected notifies that a new peer joined
	MsgPeerConnected = "peer-connected"
	// MsgPeerDisconnected notifies that a peer left
	MsgPeerDisconnected = "peer-disconnected"
	// MsgCatalogSnapshot carries a full owner catalog snapshot.
	MsgCatalogSnapshot = "catalog-snapshot"
	// MsgWorkspaceSnapshot carries a full owner workspace snapshot.
	MsgWorkspaceSnapshot = "workspace-snapshot"
	// MsgCommandRequest is a reliable command RPC request.
	MsgCommandRequest = "command-request"
	// MsgCommandReply is a reliable command RPC reply.
	MsgCommandReply = "command-reply"
	// MsgForget notifies the receiver that the sender is forgetting them.
	// Receiver should remove sender from its peer store and close the link.
	MsgForget = "forget"
	// MsgCapturePane asks a peer to capture its primary pane's visible buffer.
	MsgCapturePane = "capture-pane"
)

// ProtocolVersion is the single canonical wire protocol version for peer communication.
const ProtocolVersion = 1

// Message types reserved for future per-terminal stream setup.
const (
	// MsgOpenTerminal asks a peer to prepare a dedicated PTY data connection,
	// correlated by one-time token. Sent over the control link.
	MsgOpenTerminal = "open-terminal"
	// MsgStreamToken is the first frame on /ws/peer-stream after auth; it
	// presents the correlation token. It does NOT authorize.
	MsgStreamToken = "stream-token"
)

// Message types for remote file access.
const (
	// MsgFileRead asks a peer to read a file and return its content.
	MsgFileRead = "file-read"
	// MsgFileReadResult returns file content (or error) to the requester.
	MsgFileReadResult = "file-read-result"
)

// Message types for streaming file upload to a peer.
const (
	// MsgOpenUpload asks a peer to prepare a dedicated upload data connection,
	// correlated by one-time token. Sent over the control link.
	MsgOpenUpload = "open-upload"
)

// CapPerStream advertises dedicated /ws/peer-stream PTY data connections.
const CapPerStream = "per-stream"

// CapUpload advertises dedicated /ws/peer-stream file-upload connections.
const CapUpload = "upload"

// hasCap reports whether caps contains the given capability string.
func hasCap(caps []string, cap string) bool {
	for _, c := range caps {
		if c == cap {
			return true
		}
	}
	return false
}

// Command kinds carried by CommandRequestPayload.
const (
	CommandKindSession      = "session"
	CommandKindWorkspace    = "workspace"
	CommandKindRemoteCreate = "remote_create"
)

// Message is the envelope for all control WebSocket messages
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// AuthPayload is sent by the peer in response to a challenge
type AuthPayload struct {
	PublicKey       string   `json:"public_key"`
	Signature       string   `json:"signature"` // base64-encoded signature of the challenge
	ProtocolVersion int      `json:"protocol_version"`
	Capabilities    []string `json:"capabilities,omitempty"` // optional: per-stream, upload
}

// ChallengePayload is sent by the hub to initiate auth
type ChallengePayload struct {
	Challenge string `json:"challenge"` // base64-encoded random bytes
}

// AuthOKPayload advertises the listener's protocol version and optional capabilities.
type AuthOKPayload struct {
	ProtocolVersion int      `json:"protocol_version"`
	Capabilities    []string `json:"capabilities,omitempty"` // optional: per-stream, upload
}

// ToolEventPayload wraps a tool event from a peer
type ToolEventPayload struct {
	Event *toolevents.Event `json:"event"`
}

// SessionRuntimePayload carries runtime snapshots from a peer.
type SessionRuntimePayload struct {
	Owner     state.OwnerID                  `json:"owner"`
	Snapshots []state.SessionRuntimeSnapshot `json:"snapshots"`
}

// StatsPayload carries system stats from a peer
type StatsPayload struct {
	Stats map[string]interface{} `json:"stats"`
}

// HostInfo represents a peer's state as seen by the hub.
//
// ID is the peer's transport identity (public key fingerprint) -- the key
// used for peer-connection lookups (IsLocal, GetPeerConnection). OwnerID is
// the canonical catalog authority identity for this host (state.OwnerID) --
// what SessionRef.Owner values and the target_owner wire field actually
// carry. The two are related but NOT interchangeable: OwnerID is a different
// string encoding of a peer's identity than its fingerprint (see
// state.OwnerIDFromFingerprint), so a caller that needs to route a command
// or terminal attach to this host must use OwnerID, never ID, and vice versa
// for transport-level lookups.
type HostInfo struct {
	ID       string                 `json:"id"` // public key fingerprint
	OwnerID  string                 `json:"owner_id,omitempty"`
	Name     string                 `json:"name"`
	Version  string                 `json:"version,omitempty"`
	Local    bool                   `json:"local,omitempty"`
	Online   bool                   `json:"online"`
	Address  string                 `json:"address,omitempty"`
	Sessions []*model.Session       `json:"sessions"`
	Activity []*activity.Snapshot   `json:"activity,omitempty"`
	Stats    map[string]interface{} `json:"stats,omitempty"`
	LastSeen time.Time              `json:"last_seen"`
}

// PeerNotifyPayload is sent when a peer connects or disconnects
type PeerNotifyPayload struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// OpenTerminalPayload asks a peer to prepare a dedicated PTY data connection.
// Session is the legacy display name; SessionID + Generation are the stable
// identity used for generation-gated attach (a stale generation is refused
// before any PTY bytes stream).
type OpenTerminalPayload struct {
	StreamID     string `json:"stream_id"`
	Session      string `json:"session"`
	SessionID    string `json:"session_id,omitempty"`
	Generation   string `json:"generation,omitempty"`
	Cols         uint16 `json:"cols"`
	Rows         uint16 `json:"rows"`
	Token        string `json:"token"`
	ViewerHostID string `json:"viewer_host_id"`
}

// StreamTokenPayload carries the correlation token on /ws/peer-stream.
type StreamTokenPayload struct {
	Token string `json:"token"`
}

// FileReadPayload asks a peer to read a file at the given path.
type FileReadPayload struct {
	Token   string `json:"token"`
	Path    string `json:"path"`
	Session string `json:"session"` // for resolving relative paths
}

// OpenUploadPayload asks a peer to prepare a dedicated upload data connection.
// The hub streams file content as binary WebSocket frames, then sends a
// text control frame to signal EOF ({"type":"upload-eof"}) or abort
// ({"type":"upload-abort"}). The peer replies with one text result frame:
// {"path":"...","quotedPath":"..."} or {"error":"..."}.
type OpenUploadPayload struct {
	StreamID     string `json:"stream_id"`
	Token        string `json:"token"`
	Filename     string `json:"filename"`
	ViewerHostID string `json:"viewer_host_id"`
}

// FileReadResultPayload returns file content (base64-encoded) or error.
type FileReadResultPayload struct {
	Token       string `json:"token"`
	Data        string `json:"data,omitempty"`         // base64-encoded file content
	ContentType string `json:"content_type,omitempty"` // MIME type
	FileName    string `json:"file_name,omitempty"`    // basename
	Error       string `json:"error,omitempty"`
}

// CatalogSnapshotPayload is the wire form of state.OwnerCatalogSnapshot.
type CatalogSnapshotPayload struct {
	Owner     state.OwnerID         `json:"owner"`
	Revision  int64                 `json:"revision"`
	Sessions  []state.LocalSessionRecord `json:"sessions"`
	Workspace *state.WorkspaceRecord `json:"workspace,omitempty"`
}

// WorkspaceSnapshotPayload is the wire form of a workspace snapshot.
type WorkspaceSnapshotPayload struct {
	Owner     state.OwnerID         `json:"owner"`
	Revision  int64                 `json:"revision"`
	Workspace state.WorkspaceRecord `json:"workspace"`
}

// CommandRequestPayload carries a reliable command RPC request.
// ID is chosen by the caller and must be unique across retries.
type CommandRequestPayload struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"` // "session" | "workspace"
	Command json.RawMessage `json:"command"`
}

// CommandReplyPayload carries a reliable command RPC reply.
//
// Error carries a human-readable message for logging/legacy compatibility.
// When the failure originated from a state.StateError, ErrorCode/ErrorField/
// ErrorDetail mirror its structured fields across the wire so the receiving
// side can reconstruct a real state.StateError (see replyError) instead of
// an opaque string, letting HTTP handlers map it to the correct status code
// even when the error crossed a peer RPC boundary. ErrorCode is empty for
// non-StateError failures (and for replies from older peers), in which case
// the receiving side falls back to a generic error.
type CommandReplyPayload struct {
	ID          string          `json:"id"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	ErrorCode   string          `json:"error_code,omitempty"`
	ErrorField  string          `json:"error_field,omitempty"`
	ErrorDetail string          `json:"error_detail,omitempty"`
	Handled     bool            `json:"handled"`
}

// CapturePanePayload asks a peer to capture a session's primary pane buffer.
type CapturePanePayload struct {
	Token   string `json:"token"`
	Session string `json:"session"`
	Lines   int    `json:"lines"`
}

// CapturePaneResultPayload returns captured text (or an error) keyed by token.
type CapturePaneResultPayload struct {
	Token string `json:"token"`
	Text  string `json:"text"`
	Error string `json:"error,omitempty"`
}

// NewMessage creates a Message with a typed payload
func NewMessage(msgType string, payload interface{}) (*Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Message{
		Type:    msgType,
		Payload: json.RawMessage(data),
	}, nil
}
