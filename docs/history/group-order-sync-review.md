> **Historical.** This document predates the destructive rewrite (see `docs/release-notes/hard-rewrite.md`). It describes a design/flag/subsystem that no longer exists in current code. Kept for historical record only; do not follow as current instructions.

# Group/order sync spec review

## Critical

1. **`Rank` in `sessionattrs.Attr` creates same-key write conflicts with background/hidden/schedule.** `Attr` has one `UpdatedAt` for `Background`, `Hidden`, `ScheduleID` (`pkg/sessionattrs/sessionattrs.go:42-46`), and all remote merge is record-level LWW (`pkg/sessionattrs/sessionattrs.go:143-151`); a reorder racing a hide/background/schedule edit on the same session can drop one edit. **Recommendation:** put order in its own tiny `sessionorder` keyed store, or add field-level clocks (`rank_updated_at`, `bits_updated_at`, `schedule_updated_at`) before folding it into `Attr`.

2. **Current peer wire already strips `ScheduleID`; adding `Rank` without fixing this will strip rank too.** `peer.SessionAttr` mirrors only `Background`, `Hidden`, `UpdatedAt` (`pkg/peer/protocol.go:165-171`), and `attrsStoreAdapter` converts only those fields in both directions (`pkg/server/server.go:92-112`, `pkg/server/server.go:160-164`). **Recommendation:** if `Attr` carries rank, update `peer.SessionAttr`, adapter, snapshot, delta, tests, and schedule fan-out together; otherwise do not claim rank “rides existing messages for free.”

3. **Whole-group LWW loses updates when rename-leaf migration races normal group edits.** Spec stores `{tree,name,rank}` as one blob (`docs/plans/group-order-sync.md:66-74`), while current group rename edits only `name` (`web/src/App.tsx:877-882`) and leaf migration would edit `tree`/`activeKey`; record-level LWW means whichever write lands second silently erases the other. **Recommendation:** make group records field-LWW (`tree_updated_at`, `name_updated_at`, `rank_updated_at`, `deleted_at`) or model leaf migration as a merge transform applied to latest tree before stamping.

4. **Fractional indexing is not conflict-free unless duplicate ranks have a rule.** Two devices moving different sessions between the same neighbors can compute the same midpoint (`docs/plans/group-order-sync.md:42-53`); `orderSessions` today expects a total order from a list and falls back only for unranked items (`web/src/components/Sidebar.tsx:177-186`). **Recommendation:** sort by `(rank, sessionKey)` and add opportunistic duplicate-rank repair; no full sequence CRDT needed unless users demand exact concurrent drag intent.

5. **24h tombstone TTL is unsafe for durable groups.** `sessionattrs` drops tombstones after 24h (`pkg/sessionattrs/sessionattrs.go:51-53`, `pkg/sessionattrs/sessionattrs.go:198-203`); a peer offline longer than that can reconnect with an old snapshot and resurrect a deleted group. **Recommendation:** group tombstones must be effectively permanent, or retained until all known peers have observed a deletion epoch.

## Should-fix

1. **First-upgrade localStorage migration is underspecified and can recreate the old “empty client wins” class.** Existing groups/order load from `localStorage` (`web/src/App.tsx:115-145`, `web/src/components/Sidebar.tsx:227`) and persist immediately (`web/src/App.tsx:257-265`, `web/src/components/Sidebar.tsx:389-392`). **Recommendation:** add one-shot import: browser fetches server groups first, uploads local groups only when server has no record for that group id, then marks local migration complete and stops writing synced keys.

2. **Active group is partly data today, not just cursor.** Active layout is rendered as a group even when not in `savedGroups` (`web/src/App.tsx:1160-1164`), and switching groups saves current `paneTree` into `savedGroups` (`web/src/App.tsx:847-852`). If cursor stays local, active unsaved structure may never reach other machines. **Recommendation:** sync every named/layout group record, but keep only “currently selected group id” local; ensure active group edits write the group record before hiding cursor sync.

3. **Delete-only group GC means stale/dangling groups accumulate forever.** Spec correctly avoids liveness prune (`docs/plans/group-order-sync.md:102-111`), but groups whose sessions are permanently killed will never clear. **Recommendation:** keep delete-only server semantics, but add UI cleanup/“missing sessions” affordance instead of automatic GC.

4. **Client-side reconnect grace should be removed only from synced persistence, not from local UI projection.** Grace currently protects saved group pruning and order pruning (`web/src/App.tsx:682-698`, `web/src/App.tsx:704-737`, `web/src/components/Sidebar.tsx:398-411`); server truth removes need to mutate synced records client-side, but the live pane view and project filters are still local. **Recommendation:** delete grace-gated writes for synced groups/order, keep grace for local pane-tree/single-view/project-filter pruning and any render-time “hide missing leaves” pass.

5. **Recovery key story is weaker than spec says.** Snapshot/rebuild read/write schedule ownership by bare `session.Name` (`pkg/recovery/snapshotter.go:74-75`, `pkg/recovery/rebuild.go:87-90`), while spec relies on host-qualified keys (`docs/plans/group-order-sync.md:122-129`). **Recommendation:** explicitly test recovery in mesh mode and normalize local bare vs `<localfp>/name` before claiming rank/groups reattach for free.

6. **Auth/multi-user scope needs an explicit product decision.** Existing store path is process-global (`pkg/sessionattrs/sessionattrs.go:63-74`) and `/api/session-attrs` has no user scope (`pkg/server/server.go:1446-1483`). **Recommendation:** declare groups/order global per Termyard mesh, or add user/workspace scope before shipping if multiple humans can share a server.

7. **Group snapshot/delta should be capability-gated for rolling upgrades.** Existing peers ignore unknown message types (`pkg/peer/session.go:635-636`), but rank fields added to old `session-attrs` payloads will be dropped by old binaries. **Recommendation:** advertise group/order sync capability and tolerate mixed-version peers as “no sync,” not partial lossy sync.

## Nit

1. **The spec should name the frontend API shape.** `useSessionAttrs` returns flat sets only (`web/src/hooks/useSessionAttrs.ts:19-32`), unsuitable for ranks. **Recommendation:** add `/api/session-order` or extend response with `ranks: Record<string,string>` only if using separate field clocks.

2. **Tie repair can be lazy.** Duplicate rank repair need not run on every read. **Recommendation:** repair only after a reorder or when duplicate ranks are detected in a fetched snapshot.

3. **Use LexoRank/string ranks from day one.** Spec says float midpoints are fine to start (`docs/plans/group-order-sync.md:51-56`), but migration from floats buys little. **Recommendation:** use strings immediately; code stays smaller than later migration.

## Verdict

Sessionattrs’ transport pattern is sound: server truth, global keys, per-key deltas, snapshots on peer connect, browser refetch on WS event. But the exact data model in the spec is too optimistic. `Rank` should not share one `UpdatedAt` record with background/hidden/schedule, and group blobs need field-level merge or rename-leaf migration will lose concurrent edits. No real sequence CRDT needed for sidebar/group order; fractional ranks plus deterministic tie-break and lazy repair are enough. Simpler correct design: mirror sessionattrs twice (`sessionorder` keyed by sessionKey, `groupsync` keyed by groupID) and make `groupsync` field-LWW with durable tombstones.
