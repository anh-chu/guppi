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
	// MsgStateUpdate is a full session state snapshot
	MsgStateUpdate = "state-update"
	// MsgStateEvent is an incremental state change
	MsgStateEvent = "state-event"
	// MsgToolEvent forwards a local tool event
	MsgToolEvent = "tool-event"
	// MsgActivityUpdate sends periodic activity data
	MsgActivityUpdate = "activity-update"
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
	// MsgPeerState is aggregated state from all other peers
	MsgPeerState = "peer-state"
	// MsgPeerConnected notifies that a new peer joined
	MsgPeerConnected = "peer-connected"
	// MsgPeerDisconnected notifies that a peer left
	MsgPeerDisconnected = "peer-disconnected"
	// MsgSessionAction forwards an API action to the peer
	MsgSessionAction = "session-action"
	// MsgRequestState asks the peer for a full state refresh
	MsgRequestState = "request-state"
	// MsgV2CatalogSnapshot carries a full v2 owner catalog snapshot.
	MsgV2CatalogSnapshot = "v2-catalog-snapshot"
	// MsgV2WorkspaceSnapshot carries a full v2 owner workspace snapshot.
	MsgV2WorkspaceSnapshot = "v2-workspace-snapshot"
	// MsgV2CommandRequest is a reliable command RPC request.
	MsgV2CommandRequest = "v2-command-request"
	// MsgV2CommandReply is a reliable command RPC reply.
	MsgV2CommandReply = "v2-command-reply"
	// MsgForget notifies the receiver that the sender is forgetting them.
	// Receiver should remove sender from its peer store and close the link.
	MsgForget = "forget"
	// MsgSessionAttrsSnapshot carries the full shared session-attribute map
	// (background/hidden per session key) to a freshly-connected peer for
	// per-key last-write-wins reconciliation.
	MsgSessionAttrsSnapshot = "session-attrs-snapshot"
	// MsgSessionAttrsDelta carries a single-key shared session-attribute
	// update across paired peers. Per-key LWW by UpdatedAt.
	MsgSessionAttrsDelta = "session-attrs-delta"
	// MsgSessionOrderSnapshot carries the full shared session-order map
	// (rank per session key) to a freshly-connected peer for per-key LWW
	// reconciliation.
	MsgSessionOrderSnapshot = "session-order-snapshot"
	// MsgSessionOrderDelta carries a single-key shared session-order update
	// across paired peers. Per-key LWW by UpdatedAt.
	MsgSessionOrderDelta = "session-order-delta"
	// MsgGroupSnapshot carries the full shared group map to a freshly-connected
	// peer for field-level LWW reconciliation.
	MsgGroupSnapshot = "group-snapshot"
	// MsgGroupDelta carries a single group update across paired peers.
	MsgGroupDelta = "group-delta"
	// MsgCapturePane asks a peer to capture its primary pane's visible buffer.
	MsgCapturePane = "capture-pane"
)

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

// CapV2Catalog advertises v2 owner-catalog snapshot exchange.
const CapV2Catalog = "v2-catalog"

// CapV2Command advertises the reliable v2 command RPC path.
const CapV2Command = "v2-command"

// localCapabilities is what this build advertises in the hello.
var localCapabilities = []string{CapPerStream, CapUpload, CapV2Catalog, CapV2Command}

// V2 command kinds carried by V2CommandRequestPayload.
const (
	V2CommandKindSession     = "session"
	V2CommandKindWorkspace   = "workspace"
	V2CommandKindRemoteCreate = "remote_create"
)

// Message is the envelope for all control WebSocket messages
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// AuthPayload is sent by the peer in response to a challenge
type AuthPayload struct {
	PublicKey    string   `json:"public_key"`
	Signature    string   `json:"signature"` // base64-encoded signature of the challenge
	Capabilities []string `json:"capabilities,omitempty"`
}

// ChallengePayload is sent by the hub to initiate auth
type ChallengePayload struct {
	Challenge string `json:"challenge"` // base64-encoded random bytes
}

// AuthOKPayload advertises the listener's capabilities on the hello.
type AuthOKPayload struct {
	Capabilities []string `json:"capabilities,omitempty"`
}

// StateUpdatePayload carries a full session snapshot from a peer
type StateUpdatePayload struct {
	Sessions []*model.Session `json:"sessions"`
	Version  string           `json:"version,omitempty"`
}

// StateEventPayload carries an incremental state change
type StateEventPayload struct {
	EventType string `json:"event_type"` // session-added, session-removed, session-renamed, sessions-changed
	Session   string `json:"session,omitempty"`
	Data      any    `json:"data,omitempty"`
}

// ToolEventPayload wraps a tool event from a peer
type ToolEventPayload struct {
	Event *toolevents.Event `json:"event"`
}

// ActivityUpdatePayload carries activity data from a peer
type ActivityUpdatePayload struct {
	Snapshots []*activity.Snapshot `json:"snapshots"`
}

// StatsPayload carries system stats from a peer
type StatsPayload struct {
	Stats map[string]interface{} `json:"stats"`
}

// PeerStatePayload is the aggregated state sent from hub to peers
type PeerStatePayload struct {
	Hosts []HostInfo `json:"hosts"`
}

// HostInfo represents a peer's state as seen by the hub
type HostInfo struct {
	ID       string                 `json:"id"` // public key fingerprint
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

// SessionAttr is the shared attribute set for one global session key. Mirrors
// sessionattrs.Attr without importing that package into pkg/peer.
type SessionAttr struct {
	Background bool      `json:"background"`
	Hidden     bool      `json:"hidden"`
	ScheduleID string    `json:"schedule_id,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SessionAttrsSnapshotPayload carries the full attribute map to a peer.
type SessionAttrsSnapshotPayload struct {
	Origin string                 `json:"origin"` // node fingerprint that produced the change
	Attrs  map[string]SessionAttr `json:"attrs"`
}

// SessionAttrsDeltaPayload carries a single-key attribute update.
type SessionAttrsDeltaPayload struct {
	Origin string      `json:"origin"`
	Key    string      `json:"key"`
	Attr   SessionAttr `json:"attr"`
}

// SessionOrder is the shared rank set for one global session key.
type SessionOrder struct {
	Rank      string    `json:"rank"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SessionOrderSnapshotPayload carries the full order map to a peer.
type SessionOrderSnapshotPayload struct {
	Origin string                  `json:"origin"`
	Orders map[string]SessionOrder `json:"orders"`
}

// SessionOrderDeltaPayload carries a single-key order update.
type SessionOrderDeltaPayload struct {
	Origin string       `json:"origin"`
	Key    string       `json:"key"`
	Order  SessionOrder `json:"order"`
}

// Group is the wire form for one synced group record.
type Group struct {
	Tree              json.RawMessage `json:"tree"`
	TreeUpdatedAt     time.Time       `json:"tree_updated_at"`
	Name              string          `json:"name"`
	NameUpdatedAt     time.Time       `json:"name_updated_at"`
	NameMode          string          `json:"name_mode,omitempty"`
	NameModeUpdatedAt time.Time       `json:"name_mode_updated_at,omitempty"`
	Rank              string          `json:"rank"`
	RankUpdatedAt     time.Time       `json:"rank_updated_at"`
	DeletedAt         time.Time       `json:"deleted_at"`
}

// GroupSnapshotPayload carries the full group map to a peer.
type GroupSnapshotPayload struct {
	Origin string           `json:"origin"`
	Groups map[string]Group `json:"groups"`
}

// GroupDeltaPayload carries a single group update.
type GroupDeltaPayload struct {
	Origin string `json:"origin"`
	ID     string `json:"id"`
	Group  Group  `json:"group"`
}

// SessionActionPayload forwards a session API action to a peer
type SessionActionPayload struct {
	Action string          `json:"action"` // new, rename, select-window
	Params json.RawMessage `json:"params"`
}

// V2CatalogSnapshotPayload is the v2 wire form of state.OwnerCatalogSnapshot.
type V2CatalogSnapshotPayload struct {
	Owner    state.OwnerID              `json:"owner"`
	Revision int64                      `json:"revision"`
	Sessions []state.LocalSessionRecord `json:"sessions"`
	Layouts  []state.LayoutRecord       `json:"layouts,omitempty"`
}

// V2WorkspaceSnapshotPayload is the v2 wire form of a workspace snapshot.
type V2WorkspaceSnapshotPayload struct {
	Owner     state.OwnerID         `json:"owner"`
	Revision  int64                 `json:"revision"`
	Workspace state.WorkspaceRecord `json:"workspace"`
}

// V2CommandRequestPayload carries a reliable command RPC request.
// ID is chosen by the caller and must be unique across retries.
type V2CommandRequestPayload struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"` // "session" | "workspace"
	Command json.RawMessage `json:"command"`
}

// V2CommandReplyPayload carries a reliable command RPC reply.
type V2CommandReplyPayload struct {
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
	Handled bool            `json:"handled"`
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
