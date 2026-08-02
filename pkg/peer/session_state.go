package peer

import (
	"encoding/json"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/common"
	"github.com/anh-chu/termyard/pkg/model"
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
		sendStateUpdate(pc, deps)
	}
}

func getPeerSessions(m *Manager, peerID string) []*model.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if h, ok := m.hosts[peerID]; ok {
		return h.Sessions
	}
	return nil
}
