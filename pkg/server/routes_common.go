package server

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/groupsync"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/sessionattrs"
	"github.com/anh-chu/termyard/pkg/sessionorder"
	"github.com/anh-chu/termyard/pkg/ws"
)

// attrsStoreAdapter bridges sessionattrs.Store to the narrow SessionAttrsSink
// interface the peer package consumes.
type attrsStoreAdapter struct {
	store *sessionattrs.Store
}

func (a attrsStoreAdapter) ApplyRemoteDelta(key string, background, hidden bool, scheduleID string, updatedAt time.Time) (bool, error) {
	_, accepted, err := a.store.ApplyRemote(key, sessionattrs.Attr{
		Background: background, Hidden: hidden, ScheduleID: scheduleID, UpdatedAt: updatedAt,
	})
	return accepted, err
}

func (a attrsStoreAdapter) ApplyRemoteSnapshot(attrs map[string]peer.SessionAttr) ([]string, error) {
	conv := make(map[string]sessionattrs.Attr, len(attrs))
	for k, v := range attrs {
		conv[k] = sessionattrs.Attr{Background: v.Background, Hidden: v.Hidden, ScheduleID: v.ScheduleID, UpdatedAt: v.UpdatedAt}
	}
	return a.store.ApplySnapshot(conv)
}

func (a attrsStoreAdapter) SnapshotAttrs() map[string]peer.SessionAttr {
	snap := a.store.Snapshot()
	out := make(map[string]peer.SessionAttr, len(snap))
	for k, v := range snap {
		out[k] = peer.SessionAttr{Background: v.Background, Hidden: v.Hidden, ScheduleID: v.ScheduleID, UpdatedAt: v.UpdatedAt}
	}
	return out
}

func (a attrsStoreAdapter) SetScheduleID(key, scheduleID string) error {
	_, err := a.store.SetScheduleID(key, scheduleID)
	return err
}

// sessionOrderStoreAdapter bridges sessionorder.Store to the peer narrow sink.
type sessionOrderStoreAdapter struct {
	store *sessionorder.Store
}

func (a sessionOrderStoreAdapter) ApplyRemoteDelta(key, rank string, updatedAt time.Time) (bool, error) {
	_, accepted, err := a.store.ApplyRemote(key, sessionorder.Order{Rank: rank, UpdatedAt: updatedAt})
	return accepted, err
}

func (a sessionOrderStoreAdapter) ApplyRemoteSnapshot(orders map[string]peer.SessionOrder) ([]string, error) {
	conv := make(map[string]sessionorder.Order, len(orders))
	for k, v := range orders {
		conv[k] = sessionorder.Order{Rank: v.Rank, UpdatedAt: v.UpdatedAt}
	}
	return a.store.ApplySnapshot(conv)
}

func (a sessionOrderStoreAdapter) SnapshotOrders() map[string]peer.SessionOrder {
	snap := a.store.Snapshot()
	out := make(map[string]peer.SessionOrder, len(snap))
	for k, v := range snap {
		out[k] = peer.SessionOrder{Rank: v.Rank, UpdatedAt: v.UpdatedAt}
	}
	return out
}

// groupStoreAdapter bridges groupsync.Store to the peer narrow sink.
type groupStoreAdapter struct {
	store *groupsync.Store
}

func (a groupStoreAdapter) ApplyRemoteDelta(id string, group peer.Group) (bool, error) {
	_, accepted, err := a.store.ApplyRemote(id, groupsync.Group{
		Tree:              append(json.RawMessage(nil), group.Tree...),
		TreeUpdatedAt:     group.TreeUpdatedAt,
		Name:              group.Name,
		NameUpdatedAt:     group.NameUpdatedAt,
		NameMode:          groupsync.NameMode(group.NameMode),
		NameModeUpdatedAt: group.NameModeUpdatedAt,
		Rank:              group.Rank,
		RankUpdatedAt:     group.RankUpdatedAt,
		DeletedAt:         group.DeletedAt,
	})
	return accepted, err
}

func (a groupStoreAdapter) ApplyRemoteSnapshot(groups map[string]peer.Group) ([]string, error) {
	conv := make(map[string]groupsync.Group, len(groups))
	for id, g := range groups {
		conv[id] = groupsync.Group{
			Tree:              append(json.RawMessage(nil), g.Tree...),
			TreeUpdatedAt:     g.TreeUpdatedAt,
			Name:              g.Name,
			NameUpdatedAt:     g.NameUpdatedAt,
			NameMode:          groupsync.NameMode(g.NameMode),
			NameModeUpdatedAt: g.NameModeUpdatedAt,
			Rank:              g.Rank,
			RankUpdatedAt:     g.RankUpdatedAt,
			DeletedAt:         g.DeletedAt,
		}
	}
	return a.store.ApplySnapshot(conv)
}

func (a groupStoreAdapter) SnapshotGroups() map[string]peer.Group {
	snap := a.store.Snapshot()
	out := make(map[string]peer.Group, len(snap))
	for id, g := range snap {
		out[id] = peer.Group{
			Tree:              append(json.RawMessage(nil), g.Tree...),
			TreeUpdatedAt:     g.TreeUpdatedAt,
			Name:              g.Name,
			NameUpdatedAt:     g.NameUpdatedAt,
			NameMode:          string(g.NameMode),
			NameModeUpdatedAt: g.NameModeUpdatedAt,
			Rank:              g.Rank,
			RankUpdatedAt:     g.RankUpdatedAt,
			DeletedAt:         g.DeletedAt,
		}
	}
	return out
}

// sessionKey builds the global session identifier used by the frontend and
// sync stores. Local sessions use the bare name; remote sessions are
// host-qualified as "<host>/<name>".
func sessionKey(host, name string) string {
	if host != "" {
		return host + "/" + name
	}
	return name
}

// pruneSessionAttrs garbage-collects session-attribute keys whose owning host
// is online but whose session is gone from the authoritative mesh list, and
// drops expired tombstones. Genuinely-gone keys are tombstoned and the removal
// is fanned out to peers + browsers. No-op when peer mode is unavailable
// (without the host list we can't prove a session is gone, so we keep keys).
func pruneSessionAttrs(opts *Options, hub *ws.Hub) {
	if opts.AttrsStore == nil || opts.PeerMgr == nil {
		return
	}
	sessions := opts.PeerMgr.GetAllSessions()
	liveKeys := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		// Global key mirrors frontend sessionKey(): "<host>/<name>".
		if s.Host != "" {
			liveKeys[s.Host+"/"+s.Name] = true
		} else {
			liveKeys[s.Name] = true
		}
	}
	online := map[string]bool{}
	for _, h := range opts.PeerMgr.GetHosts() {
		if h.Online {
			online[h.ID] = true
		}
	}
	gone, changed, err := opts.AttrsStore.Prune(liveKeys, online)
	if err != nil || !changed {
		return
	}
	for _, key := range gone {
		fanoutAttrsDeltaToPeers(opts, key, opts.AttrsStore.Get(key))
	}
	if hub != nil {
		hub.BroadcastJSON(map[string]interface{}{"type": "session-attrs-updated"})
	}
}

// fanoutAttrsDeltaToPeers broadcasts a single-key session-attribute delta to
// every connected paired peer over the control WS. Best-effort: a full Send
// queue drops the frame; the peer reconciles from the next snapshot on link-up.
func fanoutAttrsDeltaToPeers(opts *Options, key string, a sessionattrs.Attr) {
	if opts.PeerMgr == nil || opts.Identity == nil {
		return
	}
	msg, err := peer.NewMessage(peer.MsgSessionAttrsDelta, peer.SessionAttrsDeltaPayload{
		Origin: opts.Identity.Fingerprint(),
		Key:    key,
		Attr:   peer.SessionAttr{Background: a.Background, Hidden: a.Hidden, ScheduleID: a.ScheduleID, UpdatedAt: a.UpdatedAt},
	})
	if err != nil {
		return
	}
	for _, pc := range opts.PeerMgr.ConnectedPeers() {
		pc.Enqueue(msg)
	}
}

func fanoutOrderDeltaToPeers(opts *Options, key string, o sessionorder.Order) {
	if opts.PeerMgr == nil || opts.Identity == nil {
		return
	}
	msg, err := peer.NewMessage(peer.MsgSessionOrderDelta, peer.SessionOrderDeltaPayload{
		Origin: opts.Identity.Fingerprint(),
		Key:    key,
		Order:  peer.SessionOrder{Rank: o.Rank, UpdatedAt: o.UpdatedAt},
	})
	if err != nil {
		return
	}
	for _, pc := range opts.PeerMgr.ConnectedPeers() {
		pc.Enqueue(msg)
	}
}

func fanoutGroupDeltaToPeers(opts *Options, id string, g groupsync.Group) {
	if opts.PeerMgr == nil || opts.Identity == nil {
		return
	}
	msg, err := peer.NewMessage(peer.MsgGroupDelta, peer.GroupDeltaPayload{
		Origin: opts.Identity.Fingerprint(),
		ID:     id,
		Group: peer.Group{
			Tree:              append(json.RawMessage(nil), g.Tree...),
			TreeUpdatedAt:     g.TreeUpdatedAt,
			Name:              g.Name,
			NameUpdatedAt:     g.NameUpdatedAt,
			NameMode:          string(g.NameMode),
			NameModeUpdatedAt: g.NameModeUpdatedAt,
			Rank:              g.Rank,
			RankUpdatedAt:     g.RankUpdatedAt,
			DeletedAt:         g.DeletedAt,
		},
	})
	if err != nil {
		return
	}
	for _, pc := range opts.PeerMgr.ConnectedPeers() {
		pc.Enqueue(msg)
	}
}

// EnforceScheduleCap prunes the sessions owned by scheduleID until at most
// keep remain, killing oldest first (by creation time). For a pre-spawn
// call pass max-1 to leave room for the incoming run; for an update-time prune
// pass max. A negative keep is treated as unlimited and is a no-op.
func EnforceScheduleCap(opts *Options, scheduleID string, keep int) {
	if opts == nil || opts.AttrsStore == nil || keep < 0 || scheduleID == "" {
		return
	}
	keys := map[string]bool{}
	for key, sid := range opts.AttrsStore.Sets().ScheduleIDs {
		if sid == scheduleID {
			keys[key] = true
		}
	}
	if len(keys) == 0 {
		return
	}

	// Collect tagged sessions from daemon registry.
	var tagged []*model.Session
	if opts.DaemonReg != nil {
		for _, info := range opts.DaemonReg.List() {
			if keys[sessionKey("", info.ID)] {
				created := time.Time{}
				if t, err := time.Parse(time.RFC3339, info.Created); err == nil {
					created = t
				}
				tagged = append(tagged, &model.Session{
					Name:    info.ID,
					Created: created,
					Backend: "daemon",
				})
			}
		}
	}

	sort.Slice(tagged, func(i, j int) bool {
		return tagged[i].Created.Before(tagged[j].Created)
	})
	for len(tagged) > keep {
		victim := tagged[0]
		tagged = tagged[1:]
		if err := opts.DaemonReg.Kill(victim.Name); err != nil {
			logrus.WithError(err).WithField("session", victim.Name).Warn("schedule cap: kill daemon failed")
		}
	}
}
