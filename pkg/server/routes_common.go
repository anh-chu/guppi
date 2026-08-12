package server

import (
	"encoding/json"
	"sort"
	"time"

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

func (a groupStoreAdapter) ApplyRemoteDelta(id string, group peer.Group) (bool, map[string]peer.Group, map[string]peer.Group, error) {
	_, accepted, enforced, enforcedPrior, err := a.store.ApplyRemote(id, groupsync.Group{
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
	if err != nil {
		return accepted, nil, nil, err
	}
	// Convert enforced groups to peer.Group format
	enforcedPeer := make(map[string]peer.Group, len(enforced))
	for eid, eg := range enforced {
		enforcedPeer[eid] = peer.Group{
			Tree:              append(json.RawMessage(nil), eg.Tree...),
			TreeUpdatedAt:     eg.TreeUpdatedAt,
			Name:              eg.Name,
			NameUpdatedAt:     eg.NameUpdatedAt,
			NameMode:          string(eg.NameMode),
			NameModeUpdatedAt: eg.NameModeUpdatedAt,
			Rank:              eg.Rank,
			RankUpdatedAt:     eg.RankUpdatedAt,
			DeletedAt:         eg.DeletedAt,
		}
	}
	// Convert prior groups to peer.Group format
	enforcedPriorPeer := make(map[string]peer.Group, len(enforcedPrior))
	for pid, pg := range enforcedPrior {
		enforcedPriorPeer[pid] = peer.Group{
			Tree:              append(json.RawMessage(nil), pg.Tree...),
			TreeUpdatedAt:     pg.TreeUpdatedAt,
			Name:              pg.Name,
			NameUpdatedAt:     pg.NameUpdatedAt,
			NameMode:          string(pg.NameMode),
			NameModeUpdatedAt: pg.NameModeUpdatedAt,
			Rank:              pg.Rank,
			RankUpdatedAt:     pg.RankUpdatedAt,
			DeletedAt:         pg.DeletedAt,
		}
	}
	return accepted, enforcedPeer, enforcedPriorPeer, nil
}

func (a groupStoreAdapter) ApplyRemoteSnapshot(groups map[string]peer.Group) ([]string, map[string]peer.Group, map[string]peer.Group, error) {
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
	changed, enforced, enforcedPrior, err := a.store.ApplySnapshot(conv)
	if err != nil {
		return nil, nil, nil, err
	}
	// Convert enforced groups to peer.Group format
	enforcedPeer := make(map[string]peer.Group, len(enforced))
	for eid, eg := range enforced {
		enforcedPeer[eid] = peer.Group{
			Tree:              append(json.RawMessage(nil), eg.Tree...),
			TreeUpdatedAt:     eg.TreeUpdatedAt,
			Name:              eg.Name,
			NameUpdatedAt:     eg.NameUpdatedAt,
			NameMode:          string(eg.NameMode),
			NameModeUpdatedAt: eg.NameModeUpdatedAt,
			Rank:              eg.Rank,
			RankUpdatedAt:     eg.RankUpdatedAt,
			DeletedAt:         eg.DeletedAt,
		}
	}
	// Convert prior groups to peer.Group format
	enforcedPriorPeer := make(map[string]peer.Group, len(enforcedPrior))
	for pid, pg := range enforcedPrior {
		enforcedPriorPeer[pid] = peer.Group{
			Tree:              append(json.RawMessage(nil), pg.Tree...),
			TreeUpdatedAt:     pg.TreeUpdatedAt,
			Name:              pg.Name,
			NameUpdatedAt:     pg.NameUpdatedAt,
			NameMode:          string(pg.NameMode),
			NameModeUpdatedAt: pg.NameModeUpdatedAt,
			Rank:              pg.Rank,
			RankUpdatedAt:     pg.RankUpdatedAt,
			DeletedAt:         pg.DeletedAt,
		}
	}
	return changed, enforcedPeer, enforcedPriorPeer, nil
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

// pruneGroupSessions garbage-collects group membership by removing session keys
// whose owning host is online but whose session is genuinely gone from the
// authoritative mesh. Enforces membership exclusivity (each key owned by at most
// one group). Changed groups are broadcast + fanned out to peers; tombstoned
// groups trigger naming coordinator cancellation. No-op when peer mode is
// unavailable or group store is absent.
func pruneGroupSessions(opts *Options, hub *ws.Hub, coordinator *groupNamingCoordinator) {
	if opts.GroupStore == nil || opts.PeerMgr == nil {
		return
	}

	sessions := opts.PeerMgr.GetAllSessions()
	liveKeys := make(map[string]bool, len(sessions)*2)
	// Track how many sessions we've received from each remote host
	hostSessions := make(map[string]int)
	for _, s := range sessions {
		// Both bare name (local) and host/name forms
		if s.Host != "" {
			liveKeys[s.Host+"/"+s.Name] = true
			hostSessions[s.Host]++
		} else {
			hostSessions[""]++ // local host
		}
		liveKeys[s.Name] = true
	}

	onlineHosts := make(map[string]bool)
	for _, h := range opts.PeerMgr.GetHosts() {
		if h.Online {
			onlineHosts[h.ID] = true
		}
	}

	// gone predicate: key is absent from live sessions AND the owner is provably gone.
	// For local keys, we check StateMgr. For remote keys, we require that the host is
	// online AND we have received at least one session from it (hostSessions[host] > 0).
	// If a remote host has zero known sessions, we haven't received data from it yet, so
	// absence of a key is not proof of deletion (prune race hardening).
	gone := func(key string) bool {
		if liveKeys[key] {
			return false // key is live
		}

		// Check owner status
		host, name := splitSessionKey(key)
		if host == "" {
			// Local key: owned by this host
			// Double-check against StateMgr if available
			if opts.StateMgr != nil {
				for _, s := range opts.StateMgr.GetSessions() {
					if s.Name == name {
						return false // found locally, not gone
					}
				}
			}
			return true // local key and session absent
		}

		// Remote key: check if owner host is online AND we've heard from it
		if !onlineHosts[host] {
			return false // owner offline, can't confirm gone
		}
		if hostSessions[host] == 0 {
			return false // no known sessions from host yet; absence is not proof
		}
		return true // owner online, we've received sessions, and key absent
	}

	changed, prior, err := opts.GroupStore.Reconcile(gone)
	if err != nil || len(changed) == 0 {
		return
	}

	// Broadcast and fanout each changed group
	for id, g := range changed {
		if !g.DeletedAt.IsZero() {
			// Tombstoned: cancel naming if active
			if coordinator != nil {
				coordinator.Cancel(id)
			}
		} else {
			// Live: observe mutation for potential re-naming
			if coordinator != nil {
				priorState := prior[id]
				coordinator.ObserveTreeMutation(id, priorState, g)
			}
		}
		// Broadcast to browser tabs
		if hub != nil {
			hub.BroadcastJSON(map[string]interface{}{
				"type": "groups-updated",
				"id":   id,
				"op":   "tree",
			})
		}
		// Fanout to peers
		fanoutGroupDeltaToPeers(opts, id, g)
	}
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

// fanoutGroupPeerDelta fanouts a peer.Group (from remote enforcement) to all peers.
func fanoutGroupPeerDelta(opts *Options, id string, g peer.Group) {
	if opts.PeerMgr == nil || opts.Identity == nil {
		return
	}
	msg, err := peer.NewMessage(peer.MsgGroupDelta, peer.GroupDeltaPayload{
		Origin: opts.Identity.Fingerprint(),
		ID:     id,
		Group:  g,
	})
	if err != nil {
		return
	}
	for _, pc := range opts.PeerMgr.ConnectedPeers() {
		pc.Enqueue(msg)
	}
}

// MakeGroupFanoutCallback creates a callback for peer group enforcement propagation.
// Used by the peer handlers to fanout loser records when enforcement occurs.
func MakeGroupFanoutCallback(opts *Options) func(id string, g peer.Group) {
	return func(id string, g peer.Group) {
		fanoutGroupPeerDelta(opts, id, g)
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
		// Service.Kill logs failures with full context (session + reason), so we only
		// call it without re-logging. Kill errors are non-fatal in this loop.
		_ = opts.Launch.Kill(victim.Name, "schedule-cap-prune")
	}
}
