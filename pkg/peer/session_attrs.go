package peer

import (
	"encoding/json"

	"github.com/sirupsen/logrus"

)

// sendInitialSessionAttrs pushes our full shared session-attribute map once on
// link-up so the remote can reconcile via per-key LWW.
func sendInitialSessionAttrs(pc *PeerConnection, deps SessionDeps) {
	if deps.AttrsSink == nil {
		return
	}
	attrs := deps.AttrsSink.SnapshotAttrs()
	if len(attrs) == 0 {
		return
	}
	msg, err := NewMessage(MsgSessionAttrsSnapshot, SessionAttrsSnapshotPayload{
		Origin: deps.Identity.Fingerprint(),
		Attrs:  attrs,
	})
	if err != nil {
		return
	}
	pc.Enqueue(msg)
}

func sendInitialSessionOrder(pc *PeerConnection, deps SessionDeps) {
	if deps.OrderSink == nil {
		return
	}
	orders := deps.OrderSink.SnapshotOrders()
	if len(orders) == 0 {
		return
	}
	msg, err := NewMessage(MsgSessionOrderSnapshot, SessionOrderSnapshotPayload{
		Origin: deps.Identity.Fingerprint(),
		Orders: orders,
	})
	if err != nil {
		return
	}
	pc.Enqueue(msg)
}

func sendInitialGroups(pc *PeerConnection, deps SessionDeps) {
	if deps.GroupSink == nil {
		return
	}
	groups := deps.GroupSink.SnapshotGroups()
	if len(groups) == 0 {
		return
	}
	msg, err := NewMessage(MsgGroupSnapshot, GroupSnapshotPayload{
		Origin: deps.Identity.Fingerprint(),
		Groups: groups,
	})
	if err != nil {
		return
	}
	pc.Enqueue(msg)
}

// handleAttrsMessage processes shared session-attrs/order/group sync messages.
func handleAttrsMessage(peerID string, msg *Message, pc *PeerConnection, deps SessionDeps, log *logrus.Entry) {
	switch msg.Type {
	case MsgSessionAttrsSnapshot:
		var p SessionAttrsSnapshotPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid session-attrs-snapshot")
			return
		}
		if p.Origin == deps.Identity.Fingerprint() || deps.AttrsSink == nil {
			return
		}
		changed, err := deps.AttrsSink.ApplyRemoteSnapshot(p.Attrs)
		if err != nil {
			log.WithError(err).Warn("apply remote session-attrs snapshot failed")
			return
		}
		if len(changed) > 0 && deps.BrowserHub != nil {
			deps.BrowserHub.BroadcastJSON(map[string]interface{}{
				"type":   "session-attrs-updated",
				"origin": p.Origin,
			})
		}
		log.WithField("origin", p.Origin).WithField("changed", len(changed)).Debug("applied remote session-attrs snapshot")

	case MsgSessionAttrsDelta:
		var p SessionAttrsDeltaPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid session-attrs-delta")
			return
		}
		if p.Origin == deps.Identity.Fingerprint() || deps.AttrsSink == nil {
			return
		}
		accepted, err := deps.AttrsSink.ApplyRemoteDelta(p.Key, p.Attr.Background, p.Attr.Hidden, p.Attr.ScheduleID, p.Attr.UpdatedAt)
		if err != nil {
			log.WithError(err).Warn("apply remote session-attrs delta failed")
			return
		}
		if !accepted {
			return
		}
		if deps.BrowserHub != nil {
			deps.BrowserHub.BroadcastJSON(map[string]interface{}{
				"type":   "session-attrs-updated",
				"origin": p.Origin,
				"key":    p.Key,
			})
		}
		log.WithField("origin", p.Origin).WithField("key", p.Key).Debug("applied remote session-attrs delta")

	case MsgSessionOrderSnapshot:
		var p SessionOrderSnapshotPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid session-order-snapshot")
			return
		}
		if p.Origin == deps.Identity.Fingerprint() || deps.OrderSink == nil {
			return
		}
		changed, err := deps.OrderSink.ApplyRemoteSnapshot(p.Orders)
		if err != nil {
			log.WithError(err).Warn("apply remote session-order snapshot failed")
			return
		}
		if len(changed) > 0 && deps.BrowserHub != nil {
			deps.BrowserHub.BroadcastJSON(map[string]interface{}{
				"type":   "session-order-updated",
				"origin": p.Origin,
			})
		}
		log.WithField("origin", p.Origin).WithField("changed", len(changed)).Debug("applied remote session-order snapshot")

	case MsgSessionOrderDelta:
		var p SessionOrderDeltaPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid session-order-delta")
			return
		}
		if p.Origin == deps.Identity.Fingerprint() || deps.OrderSink == nil {
			return
		}
		accepted, err := deps.OrderSink.ApplyRemoteDelta(p.Key, p.Order.Rank, p.Order.UpdatedAt)
		if err != nil {
			log.WithError(err).Warn("apply remote session-order delta failed")
			return
		}
		if !accepted {
			return
		}
		if deps.BrowserHub != nil {
			deps.BrowserHub.BroadcastJSON(map[string]interface{}{
				"type":   "session-order-updated",
				"origin": p.Origin,
				"key":    p.Key,
			})
		}
		log.WithField("origin", p.Origin).WithField("key", p.Key).Debug("applied remote session-order delta")

	case MsgGroupSnapshot:
		var p GroupSnapshotPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid group-snapshot")
			return
		}
		if p.Origin == deps.Identity.Fingerprint() || deps.GroupSink == nil {
			return
		}
		changed, enforced, _, err := deps.GroupSink.ApplyRemoteSnapshot(p.Groups)
		if err != nil {
			log.WithError(err).Warn("apply remote group snapshot failed")
			return
		}
		totalChanged := len(changed) + len(enforced)
		if totalChanged > 0 && deps.BrowserHub != nil {
			deps.BrowserHub.BroadcastJSON(map[string]interface{}{
				"type":   "groups-updated",
				"origin": p.Origin,
			})
		}
		// Fanout enforced (loser) groups to peers
		for id, changed := range enforced {
			if deps.GroupFanoutCallback != nil {
				deps.GroupFanoutCallback(id, changed)
			}
		}
		log.WithField("origin", p.Origin).WithField("changed", len(changed)).WithField("enforced", len(enforced)).Debug("applied remote group snapshot")

	case MsgGroupDelta:
		var p GroupDeltaPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.WithError(err).Debug("invalid group-delta")
			return
		}
		if p.Origin == deps.Identity.Fingerprint() || deps.GroupSink == nil {
			return
		}
		accepted, enforced, _, err := deps.GroupSink.ApplyRemoteDelta(p.ID, p.Group)
		if err != nil {
			log.WithError(err).Warn("apply remote group delta failed")
			return
		}
		if !accepted {
			return
		}
		if deps.BrowserHub != nil {
			deps.BrowserHub.BroadcastJSON(map[string]interface{}{
				"type":   "groups-updated",
				"origin": p.Origin,
				"id":     p.ID,
			})
			// Broadcast enforcement-changed groups
			for eid := range enforced {
				deps.BrowserHub.BroadcastJSON(map[string]interface{}{
					"type":   "groups-updated",
					"origin": p.Origin,
					"id":     eid,
				})
			}
		}
		// Fanout enforced (loser) groups to peers
		for id, changed := range enforced {
			if deps.GroupFanoutCallback != nil {
				deps.GroupFanoutCallback(id, changed)
			}
		}
		log.WithField("origin", p.Origin).WithField("id", p.ID).Debug("applied remote group delta")
	}
}
