package peer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/state"
)

// handleStateMessage processes activity/stats/tool-event/peer-notify control
// messages received from an authenticated peer connection.
func handleStateMessage(peerID string, msg *Message, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	switch msg.Type {
	case MsgActivityUpdate:
		var p ActivityUpdatePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		for _, s := range p.Snapshots {
			s.Host = peerID
		}
		deps.Manager.UpdatePeerActivity(peerID, p.Snapshots)

	case MsgToolEvent:
		var p ToolEventPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid tool-event")
			return
		}
		if p.Event == nil {
			return
		}
		// Never trust a claimed host on the wire — stamp the authenticated
		// identity of the connection this message arrived on, mirroring the
		// MsgActivityUpdate case above.
		p.Event.Host = peerID
		p.Event.HostName = deps.Manager.GetHostName(peerID)
		deps.ToolTracker.Record(p.Event)

	case MsgStats:
		var p StatsPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		deps.Manager.UpdatePeerStats(peerID, p.Stats)

	case MsgPeerConnected:
		var p PeerNotifyPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		deps.Manager.RegisterPeer(p.ID, p.Name, "", nil)

	case MsgPeerDisconnected:
		var p PeerNotifyPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		deps.Manager.UnregisterPeer(p.ID)
	}
}

func sendInitialCatalog(pc *PeerConnection, deps SessionDeps) {
	if deps.Manager == nil {
		return
	}
	catalog := deps.Manager.catalog
	if catalog == nil {
		return
	}
	snap := catalog.LocalCatalogSnapshot()
	payload := CatalogSnapshotPayload{
		Owner:     snap.Owner,
		Revision:  snap.Revision,
		Sessions:  snap.Sessions,
		Workspace: snap.Workspace,
	}
	msg, err := NewMessage(MsgCatalogSnapshot, payload)
	if err != nil {
		return
	}
	pc.EnqueueCatalogSnapshot(msg)
}

func sendInitialWorkspace(pc *PeerConnection, deps SessionDeps) {
	if deps.Manager == nil {
		return
	}
	catalog := deps.Manager.catalog
	if catalog == nil {
		return
	}
	// Send the singleton workspace snapshot. The per-connection subscriber
	// below forwards updates after each accepted command so the remote cache
	// never sees intermediate steps.
	ws := catalog.Workspace()
	if ws == nil {
		return
	}
	msg, err := NewMessage(MsgWorkspaceSnapshot, WorkspaceSnapshotPayload{
		Owner:     ws.Owner,
		Revision:  ws.Revision,
		Workspace: *ws,
	})
	if err != nil {
		return
	}
	pc.EnqueueWorkspaceSnapshot(msg)
}

// handleCommandMessage routes catalog, workspace, and command messages.
func handleCommandMessage(peerID string, msg *Message, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	switch msg.Type {
	case MsgCatalogSnapshot:
		var p CatalogSnapshotPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid catalog snapshot")
			return
		}
		deps.Manager.UpdateRemoteCatalog(peerID, pc, state.OwnerCatalogSnapshot{
			Owner:     p.Owner,
			Revision:  p.Revision,
			Sessions:  p.Sessions,
			Workspace: p.Workspace,
		})

	case MsgWorkspaceSnapshot:
		var p WorkspaceSnapshotPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid workspace snapshot")
			return
		}
		deps.Manager.UpdateRemoteWorkspace(peerID, pc, p.Workspace)

	case MsgCommandRequest:
		var p CommandRequestPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid command request")
			return
		}
		handleCommandRequest(peerID, p, pc, deps, log)

	case MsgCommandReply:
		var p CommandReplyPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid command reply")
			return
		}
		deps.Manager.deliverCommandReply(peerID, pc, p)
	}
}

func handleCommandRequest(peerID string, req CommandRequestPayload, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	reply := CommandReplyPayload{ID: req.ID}
	switch req.Kind {
	case CommandKindSession:
		handleSessionCommandRequest(peerID, req, pc, deps, &reply)
	case CommandKindWorkspace:
		handleWorkspaceCommandRequest(peerID, req, pc, deps, &reply)
	case CommandKindRemoteCreate:
		handleRemoteCreateRequest(peerID, req, pc, deps, &reply)
	default:
		reply.Error = fmt.Sprintf("unknown command kind %q", req.Kind)
	}
	sendCommandReply(pc, reply)
}

// handleSessionCommandRequest executes a session command received from an
// authenticated peer connection. peerID (the sender's authenticated
// identity) is threaded into ExecuteSessionCommandFromPeer so the command's
// Ref.Owner is verified against this node's own catalog owner before any
// mutation; a forged/mismatched owner is rejected, never silently ignored.
func handleSessionCommandRequest(peerID string, req CommandRequestPayload, pc *PeerConnection, deps SessionDeps, reply *CommandReplyPayload) {
	if deps.CommandSvc == nil {
		reply.Error = "command service unavailable"
		return
	}
	var cmd state.SessionCommand
	if err := json.Unmarshal(req.Command, &cmd); err != nil {
		reply.Error = "malformed command"
		return
	}
	res, err := deps.CommandSvc.ExecuteSessionCommandFromPeer(context.Background(), cmd, peerID)
	if err != nil {
		setReplyError(reply, err)
		return
	}
	reply.Handled = true
	data, err := json.Marshal(res)
	if err != nil {
		reply.Error = "failed to encode result"
		return
	}
	reply.Result = data
}

// handleWorkspaceCommandRequest executes a workspace command received from
// an authenticated peer connection. peerID is threaded into
// ApplyWorkspaceCommandFromPeer so every SessionRef embedded in the command's
// params is verified against this node's own catalog owner before any
// mutation; a forged/foreign ref is rejected, never silently ignored.
func handleWorkspaceCommandRequest(peerID string, req CommandRequestPayload, pc *PeerConnection, deps SessionDeps, reply *CommandReplyPayload) {
	cat := deps.Catalog
	if cat == nil {
		reply.Error = "catalog not enabled"
		return
	}
	var cmd state.WorkspaceCommand
	if err := json.Unmarshal(req.Command, &cmd); err != nil {
		reply.Error = "malformed workspace command"
		return
	}
	if err := cat.ApplyWorkspaceCommandFromPeer(cmd, peerID); err != nil {
		setReplyError(reply, err)
		return
	}
	reply.Handled = true
}

// handleRemoteCreateRequest executes a remote-create request received from
// an authenticated peer connection. peerID is threaded into
// ExecuteRemoteCreateFromPeer so the request's Requester is verified against
// the authenticated sender before any mutation; a spoofed Requester is
// rejected, never silently ignored.
func handleRemoteCreateRequest(peerID string, req CommandRequestPayload, pc *PeerConnection, deps SessionDeps, reply *CommandReplyPayload) {
	if deps.RemoteCreateCoordinator == nil {
		reply.Error = "remote create coordinator unavailable"
		return
	}
	var r state.RemoteCreateRequest
	if err := json.Unmarshal(req.Command, &r); err != nil {
		reply.Error = "malformed remote create request"
		return
	}
	res, err := deps.RemoteCreateCoordinator.ExecuteRemoteCreateFromPeer(context.Background(), r, peerID)
	if err != nil {
		setReplyError(reply, err)
		return
	}
	reply.Handled = true
	data, err := json.Marshal(res)
	if err != nil {
		reply.Error = "failed to encode remote create result"
		return
	}
	reply.Result = data
}

func sendCommandReply(pc *PeerConnection, reply CommandReplyPayload) {
	msg, err := NewMessage(MsgCommandReply, reply)
	if err != nil {
		return
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	pc.enqueue(pc.lo, wireFrame{data: data})
}
