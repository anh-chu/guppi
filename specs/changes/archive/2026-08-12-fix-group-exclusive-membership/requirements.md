# Requirements: Server-Authoritative Exclusive Group Membership

## R1. Exclusive membership after any write

GIVEN a session key is currently a leaf in group A
WHEN a SetTree/ApplyRemote/ApplySnapshot/MigrateKey write places the same session key in group B
THEN the session is removed from group A's leaf list, added to group B, and persisted; `groups-updated` broadcast confirms the change; the session key appears in exactly one live group on disk and in the client snapshot.

GIVEN group A has timestamps `t1` and group B has timestamp `t2` where `t1 < t2`, both containing the same session key
WHEN a write runs `enforce(now)` without explicit `winnerID`
THEN group B wins (most recent); the session is removed from group A and kept in group B; group A loses the leaf but may persist as a skeleton if it has other members.

GIVEN a session key appears in multiple groups after a write (e.g., due to ApplyRemote reordering)
WHEN the next write runs `enforce()`
THEN exactly one group wins; all others lose the leaf; the new state is idempotent across subsequent calls.

## R2. Group dissolution at subthreshold membership

GIVEN a group has 2 leaves (minimal valid count)
WHEN a write removes 1 leaf (e.g., session move or `enforce()` removes it as non-winner)
THEN the group falls to 1 leaf and is immediately tombstoned: a deletion record is written to `groups.json` with an updated timestamp and deleted flag, the record persists on disk, and `groups-updated` broadcasts include the tombstone.

GIVEN a group has 0 leaves (all removed via enforcement or kills)
THEN the group is tombstoned immediately with deleted flag.

GIVEN a group is tombstoned
WHEN a subsequent write (SetTree, ApplyRemote, ApplySnapshot) targets that group
THEN the group may be re-created (new timestamp > tombstone timestamp re-activates it) or the tombstone persists; the client behavior is consistent.

## R3. Opportunistic dead-session pruning

GIVEN a session's owner host is online and the session absent from the `/api/sessions` snapshot
WHEN GET /api/groups runs `Reconcile(gone func(key) bool)` where `gone(key)` returns true for that session
THEN the session's leaf nodes are removed from all group trees; the healed trees are returned in the response; tombstoned groups with no remaining leaves are cleaned from persistence (optional, best-effort).

GIVEN a dead session with a leaf in group A
WHEN the client calls GET /api/groups
THEN the response does not include that leaf; if group A still has ≥2 other leaves, it is returned without the dead session; if group A falls below 2 leaves after pruning, it may be tombstoned or returned as a skeleton (healing is opportunistic, not guaranteed until next reconcile).

## R4. Tombstones never resurrected by peer deltas

GIVEN a group is tombstoned with `deleted: true` and timestamp `t_delete` in the group store
WHEN a peer delta (ApplyRemote) arrives with an older timestamp `t_delta < t_delete` and attempts to add leaves to that group
THEN the delta is rejected for that group (LWW comparison: `t_delta < t_delete` means tombstone wins); the group remains deleted.

GIVEN the delta timestamp `t_delta > t_delete` and includes member updates
THEN the tombstone is overridden (group re-activated) if the delta is genuine from a newer write; this is correct LWW behavior.

GIVEN enforcement removes a session from multiple groups via pruning or moves
WHEN those prunes bump the timestamps of the losing groups
THEN a stale delta with older timestamp cannot resurrect any of those groups; prune timestamps act as the timestamp-bump that LWW requires.

## R5. Client push gating during divergence

GIVEN a client has received a `groups/snapshot` from the server (authoritative state)
WHEN the client's local pane tree diverges from the snapshot (e.g., client received two competing snapshots or a snapshot during a mutation)
THEN the client reconciles locally (adopts server truth) without pushing the reconciled tree back to the server.

GIVEN a client in the middle of a user-initiated edit (split/close/resize/drag/pair)
WHEN the edit is incomplete (`paneTreeRev` < server version, or `skipTreeAdoptFor` guard is active)
THEN the client does not push the partially-mutated tree; only completed edits push.

GIVEN a group switch event (user clicks a different group in the sidebar)
WHEN the pane tree in the new group differs from the server record
THEN the client renders the local version but does not push it; snapshot-on-reconnect will correct divergence.

## R6. Snapshot replaces and reconciles

GIVEN a client receives `groups/snapshot` with `authoritative: true` from the server
WHEN the snapshot's group IDs/shapes differ from the client's cached groups
THEN the client replaces its group map entirely (old groups not in snapshot are discarded from cache).

GIVEN the active group's pane tree in the snapshot differs from the client's local tree
WHEN no local pane-tree edit is in flight (no `paneTreeRev` push pending, `skipTreeAdoptFor` guard expired)
THEN the client reconciles: adopts server panes (adds new panes), keeps local unsaved edits if any, but does not prune local panes — server is authoritative for membership, client for local edits.

GIVEN a local edit is in flight
WHEN the snapshot arrives
THEN the client skips reconciliation for this snapshot (waits for the next one after the push completes); local tree is not overwritten mid-edit.

## R7. Enforcement triggers naming cleanup

GIVEN a group's leaf membership is changed by `enforce()` (exclusive winner chosen, other groups' leaves pruned)
WHEN the enforcement completes
THEN `ObserveTreeMutation(groupID, newMemberSet)` is called on the naming coordinator with the group ID and the new member set; the coordinator can detect UNNAMED placeholder names and schedule a `Cancel(groupID)` to regenerate the group name.

GIVEN a group had an autogenerated UNNAMED name before enforcement
WHEN `ObserveTreeMutation` fires with the new member set
THEN the naming coordinator detects that the members changed and clears the stale name; the next client snapshot will refresh it.

GIVEN enforcement is called multiple times in one request (e.g., pruning the same session from multiple groups)
WHEN `ObserveTreeMutation` fires once per group
THEN the naming coordinator receives multiple calls (idempotent); each call refines the group's name based on the latest member set.

## R8. Overall authoritativeness

GIVEN the server is the sole authority for group membership (exclusive leaves, dissolution, tombstones)
WHEN any client edit is applied to the group store (SetTree, ApplyRemote)
THEN enforcement immediately runs, conflicts are resolved, and the server's state is pushed to all clients via `groups-updated` broadcast.

GIVEN a client receives a `groups-updated` broadcast with new membership
WHEN the client's local group cache was already updated optimistically
THEN the client reconciles: trusts the server's exclusive membership (no local merge), updates its cache, and re-renders the sidebar.

GIVEN two clients push conflicting edits (session X moved to different groups)
WHEN both edits arrive at the server
THEN enforcement runs: the most-recent edit (by timestamp) wins; the other group loses the leaf immediately; both clients converge on the same membership via the next broadcast or snapshot.
