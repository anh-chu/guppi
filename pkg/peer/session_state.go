package peer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/common"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/state"
)

// sendInitialPeerState pushes a snapshot containing only the local host
// (transitivity off, see plan §3.5).
func sendInitialPeerState(pc *PeerConnection, deps SessionDeps, remotePeerID string) {
	hosts := deps.Manager.GetHostsForPeer(remotePeerID)
	msg, err := NewMessage(MsgPeerState, PeerStatePayload{Hosts: hosts})
	if err != nil {
		return
	}
	pc.Enqueue(msg)
}

func sendStateUpdate(pc *PeerConnection, deps SessionDeps) {
	sessions := deps.LocalMgr.GetSessions()
	msg, err := NewMessage(MsgStateUpdate, StateUpdatePayload{Sessions: sessions, Version: common.VERSION})
	if err != nil {
		return
	}
	pc.Enqueue(msg)
}

// handleStateMessage processes peer-state and session-state control messages.
func handleStateMessage(peerID string, msg *Message, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	switch msg.Type {
	case MsgStateUpdate:
		var p StateUpdatePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid state-update")
			return
		}
		deps.Manager.UpdatePeerSessions(peerID, p.Sessions)
		if p.Version != "" {
			deps.Manager.UpdatePeerVersion(peerID, p.Version)
		}

	case MsgStateEvent:
		var p StateEventPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		if p.EventType == "session-renamed" && deps.BrowserHub != nil {
			newName := ""
			switch data := p.Data.(type) {
			case map[string]any:
				if v, ok := data["new_name"].(string); ok {
					newName = v
				}
			case map[string]string:
				newName = data["new_name"]
			}
			if p.Session != "" && newName != "" {
				deps.BrowserHub.BroadcastJSON(map[string]interface{}{
					"type":    "session-renamed",
					"host":    peerID,
					"session": p.Session,
					"data": map[string]string{
						"new_name": newName,
					},
				})
			}
		}
		deps.Manager.UpdatePeerSessions(peerID, getPeerSessions(deps.Manager, peerID))

	case MsgActivityUpdate:
		var p ActivityUpdatePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		for _, s := range p.Snapshots {
			s.Host = peerID
		}
		deps.Manager.UpdatePeerActivity(peerID, p.Snapshots)

	case MsgStats:
		var p StatsPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		deps.Manager.UpdatePeerStats(peerID, p.Stats)

	case MsgPeerState:
		var p PeerStatePayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return
		}
		for _, host := range p.Hosts {
			if deps.Manager.IsLocal(host.ID) {
				continue
			}
			deps.Manager.UpdatePeerSessions(host.ID, host.Sessions)
			if host.Online && !deps.Manager.HasHost(host.ID) {
				deps.Manager.RegisterPeer(host.ID, host.Name, "", nil)
			}
		}

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

	case MsgRequestState:
		// In v2-only mode, deps.LocalMgr is a neutered shim carrying no real
		// session state; skip the legacy reply so we don't advertise an
		// always-empty legacy session list in response to a peer's request.
		if deps.V2CommandSvc == nil {
			sendStateUpdate(pc, deps)
		}
	}
}

func getPeerSessions(m *Manager, peerID string) []*model.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if h, ok := m.hosts[peerID]; ok {
		out := make([]*model.Session, len(h.Sessions))
		for i, s := range h.Sessions {
			out[i] = copySession(s)
		}
		return out
	}
	return nil
}

func sendInitialV2Catalog(pc *PeerConnection, deps SessionDeps) {
	if deps.Manager == nil {
		return
	}
	catalog := deps.Manager.v2Catalog
	if catalog == nil {
		return
	}
	snap := catalog.LocalCatalogSnapshot()
	payload := V2CatalogSnapshotPayload{
		Owner:    snap.Owner,
		Revision: snap.Revision,
		Sessions: snap.Sessions,
		Layouts:  snap.Layouts,
	}
	msg, err := NewMessage(MsgV2CatalogSnapshot, payload)
	if err != nil {
		return
	}
	pc.EnqueueV2CatalogSnapshot(msg)
}

func sendInitialV2Workspace(pc *PeerConnection, deps SessionDeps) {
	if deps.Manager == nil {
		return
	}
	catalog := deps.Manager.v2Catalog
	if catalog == nil {
		return
	}
	// Send the first layout as a complete workspace snapshot. The
	// per-connection subscriber below forwards updates after each accepted
	// command so the remote cache never sees intermediate steps.
	layouts := catalog.Layouts()
	if len(layouts) == 0 {
		return
	}
	snap, err := catalog.WorkspaceSnapshot(layouts[0].ID)
	if err != nil {
		return
	}
	msg, err := NewMessage(MsgV2WorkspaceSnapshot, V2WorkspaceSnapshotPayload{
		Owner:     snap.Record.Owner,
		Revision:  snap.Record.Revision,
		Workspace: snap.Record,
	})
	if err != nil {
		return
	}
	pc.EnqueueV2WorkspaceSnapshot(msg)
}

// handleV2Message routes v2 catalog, workspace, and command messages.
func handleV2Message(peerID string, msg *Message, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	switch msg.Type {
	case MsgV2CatalogSnapshot:
		var p V2CatalogSnapshotPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid v2 catalog snapshot")
			return
		}
		deps.Manager.UpdateRemoteCatalog(peerID, pc, state.OwnerCatalogSnapshot{
			Owner:    p.Owner,
			Revision: p.Revision,
			Sessions: p.Sessions,
			Layouts:  p.Layouts,
		})

	case MsgV2WorkspaceSnapshot:
		var p V2WorkspaceSnapshotPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid v2 workspace snapshot")
			return
		}
		deps.Manager.UpdateRemoteWorkspace(peerID, pc, p.Workspace)

	case MsgV2CommandRequest:
		var p V2CommandRequestPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid v2 command request")
			return
		}
		handleV2CommandRequest(peerID, p, pc, deps, log)

	case MsgV2CommandReply:
		var p V2CommandReplyPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid v2 command reply")
			return
		}
		deps.Manager.deliverCommandReply(peerID, pc, p)
	}
}

func handleV2CommandRequest(peerID string, req V2CommandRequestPayload, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	reply := V2CommandReplyPayload{ID: req.ID}
	switch req.Kind {
	case V2CommandKindSession:
		handleV2SessionCommandRequest(peerID, req, pc, deps, &reply)
	case V2CommandKindWorkspace:
		handleV2WorkspaceCommandRequest(peerID, req, pc, deps, &reply)
	case V2CommandKindRemoteCreate:
		handleV2RemoteCreateRequest(peerID, req, pc, deps, &reply)
	default:
		reply.Error = fmt.Sprintf("unknown command kind %q", req.Kind)
	}
	sendV2CommandReply(pc, reply)
}

// handleV2SessionCommandRequest executes a session command received from an
// authenticated peer connection. peerID (the sender's authenticated
// identity) is threaded into ExecuteSessionCommandFromPeer so the command's
// Ref.Owner is verified against this node's own catalog owner before any
// mutation; a forged/mismatched owner is rejected, never silently ignored.
func handleV2SessionCommandRequest(peerID string, req V2CommandRequestPayload, pc *PeerConnection, deps SessionDeps, reply *V2CommandReplyPayload) {
	if deps.V2CommandSvc == nil {
		reply.Error = "v2 command service unavailable"
		return
	}
	var cmd state.SessionCommand
	if err := json.Unmarshal(req.Command, &cmd); err != nil {
		reply.Error = "malformed command"
		return
	}
	res, err := deps.V2CommandSvc.ExecuteSessionCommandFromPeer(context.Background(), cmd, peerID)
	if err != nil {
		reply.Error = err.Error()
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

// handleV2WorkspaceCommandRequest executes a workspace command received from
// an authenticated peer connection. peerID is threaded into
// ApplyWorkspaceCommandFromPeer so every SessionRef embedded in the command's
// params is verified against this node's own catalog owner before any
// mutation; a forged/foreign ref is rejected, never silently ignored.
func handleV2WorkspaceCommandRequest(peerID string, req V2CommandRequestPayload, pc *PeerConnection, deps SessionDeps, reply *V2CommandReplyPayload) {
	cat := deps.V2Catalog
	if cat == nil {
		reply.Error = "v2 catalog not enabled"
		return
	}
	var cmd state.WorkspaceCommand
	if err := json.Unmarshal(req.Command, &cmd); err != nil {
		reply.Error = "malformed workspace command"
		return
	}
	if err := cat.ApplyWorkspaceCommandFromPeer(cmd, peerID); err != nil {
		reply.Error = err.Error()
		return
	}
	reply.Handled = true
}

// handleV2RemoteCreateRequest executes a remote-create request received from
// an authenticated peer connection. peerID is threaded into
// ExecuteRemoteCreateFromPeer so the request's Requester is verified against
// the authenticated sender before any mutation; a spoofed Requester is
// rejected, never silently ignored.
func handleV2RemoteCreateRequest(peerID string, req V2CommandRequestPayload, pc *PeerConnection, deps SessionDeps, reply *V2CommandReplyPayload) {
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
		reply.Error = err.Error()
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

func sendV2CommandReply(pc *PeerConnection, reply V2CommandReplyPayload) {
	msg, err := NewMessage(MsgV2CommandReply, reply)
	if err != nil {
		return
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	pc.enqueue(pc.lo, wireFrame{data: data})
}
