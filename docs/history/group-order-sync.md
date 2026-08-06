> **Historical.** This document predates the destructive rewrite (see `docs/release-notes/hard-rewrite.md`). It describes a design/flag/subsystem that no longer exists in current code. Kept for historical record only; do not follow as current instructions.

# Sync groups and ordering across peers

## Problem

Saved groups (split layouts) and sidebar ordering live in per-device
`localStorage`. Switching machines loses the workflow: groups, group order, and
session order do not follow the user. Goal is to make this durable structure
sync across the mesh, the same way `sessionattrs` already syncs background /
hidden / schedule bits.

What is device-local today:

- Sidebar order: `termyard:session-order` (array of `sessionKey`s), in `Sidebar.tsx`.
- Groups: `termyard:saved-groups` (`LayoutGroup{id, tree, activeKey, name}`) +
  `termyard:group-order` (array of group ids), in `App.tsx`.
- Cursor: `termyard:active-group-id` / `termyard:active-group-name` (which group
  is currently being viewed).

## Prior art to reuse

`pkg/sessionattrs` already solved this class of problem and its own doc comment
records why the naive approach failed:

- Server owns the truth, no `localStorage` source of truth.
- Keys are global, host-qualified (`<owner-fp>/<name>`), identical to
  `sessionKey()` on the frontend, no translation layer.
- Updates are per-key deltas with per-key last-write-wins timestamps. No
  whole-blob LWW (the old design where a fresh client's empty push stamped
  `now()` and wiped everyone).
- GC happens server-side via `Prune`, gated on the owning host being online, so
  a client can never fan a reset.

Fan-out rides `MsgSessionAttrsDelta` / `MsgSessionAttrsSnapshot`
(`pkg/peer/protocol.go`); hub seeds a peer on connect with a snapshot, then
streams single-key deltas. Browser refetches on a `session-attrs-updated` WS
event.

**Reuse the transport pattern, not the exact `Attr` record.** Two findings from
review forced this (both verified):

1. `sessionattrs.Attr` has a single `UpdatedAt` covering Background, Hidden, and
   ScheduleID (`sessionattrs.go:42-46`); merge is record-level LWW
   (`sessionattrs.go:143-151`). Folding order into that record means a reorder
   racing a hide / background / schedule edit on the same session drops one of
   them. Order gets its own store.
2. The peer wire `SessionAttr` mirrors only Background / Hidden / UpdatedAt
   (`protocol.go:167-171`), and the adapter converts only those
   (`server.go:92-112`, `server.go:158-164`). **`ScheduleID` is stored locally
   but never fanned to peers today** — a latent gap. Anything new added to the
   record (rank) would be silently stripped the same way. New sync surfaces get
   their own message types, fully plumbed.

## Design

### 1. Sidebar ordering: new `pkg/sessionorder` store, keyed by `sessionKey`

A separate per-key store mirroring `sessionattrs`'s transport (Snapshot,
ApplyRemote, ApplySnapshot, on-disk JSON, new `MsgSessionOrderDelta` /
`MsgSessionOrderSnapshot`, `session-order-updated` WS event), holding one
fractional rank per session key:

```
sessionKey -> { rank string, updatedAt time.Time }   // per-key LWW
```

Reordering is a **single-key write**: drag a session between B and C, set its
rank to a value between B's and C's, POST only that key. Per-key LWW means a
reorder on one session never clobbers another.

**Concurrency is not fully conflict-free; tie-break instead of a sequence CRDT.**
Two devices inserting different sessions between the same neighbors can compute
the same rank string. That is acceptable: sort by `(rank, sessionKey)` so a
collision still yields a deterministic, identical order on every peer. A real
sequence CRDT is not warranted for sidebar order. Add opportunistic
duplicate-rank repair: when a fetched snapshot or a local reorder surfaces a
tie, re-space the colliding keys and write them back. Lazy, not on every read.

Use LexoRank-style string ranks from the start (e.g. the `fractional-indexing`
npm package). Floats buy nothing here and a later float→string migration costs
more than starting with strings.

`termyard:session-order` array is removed once this lands.

### 2. Groups: new `pkg/groupsync` store, keyed by group id, FIELD-level LWW

A group is a split-layout _tree_ plus name plus order, not a per-session bit, so
it cannot ride on `sessionKey`. Whole-group LWW is **not** safe: rename-leaf
migration (see divergence 2) mutates `tree` while a user renaming the group on
another device mutates `name`, and record-level LWW would silently erase
whichever write lands second (current rename touches only `name`,
`App.tsx:877-882`). So each independently-edited field carries its own clock:

```
groupID -> {
  tree        json,   tree_updated_at   time.Time
  name        string, name_updated_at   time.Time
  rank        string, rank_updated_at   time.Time
  deleted_at  time.Time   // tombstone; zero = live
}
```

Merge per field by its own `*_updated_at`. `group-order` becomes `rank` (same
fractional indexing + `(rank, groupID)` tie-break as §1). New message pair
(`MsgGroupSnapshot` / `MsgGroupDelta`) carrying the full field set, fully
plumbed through the peer adapter, plus a `groups-updated` WS event.

### 3. Keep local (do NOT sync)

`active-group-id` only (the currently-selected group). It is a cursor, not data;
syncing it would yank the other machine's view around.

**Caveat: the active group is partly data today.** The active layout renders as
a group even when it is not yet in `savedGroups` (`App.tsx:1160-1164`), and
switching groups saves the current `paneTree` into `savedGroups`
(`App.tsx:847-852`). So the active _structure_ must be written to its group
record (synced) before we treat the active _id_ as a local-only cursor.
Otherwise an unsaved active layout never reaches other machines.

## Cross-play with existing mechanisms

### Rides existing rails

- **Auto-recovery (`pkg/recovery`).** Rebuilder restores tmux sessions by name;
  per-key stores survive on disk under the same key, so records reattach after a
  rebuild (this is how `ScheduleID` already round-trips locally,
  `rebuild.go:87-90`). Group records survive the same way; `group.tree` leaves
  reference session names recovery restores under the same name.
- **Offline peer.** `group.tree` can point at a peer's session that is offline.
  The frontend already prunes missing leaves at render time (`App.tsx`) and
  rejoins them when the peer reconnects. Host-qualified keys mean no collision.

### Divergences (do not blind-copy `sessionattrs`)

1. **Group prune semantics must invert.** `sessionattrs.Prune` tombstones a key
   when the owner is online but the session is gone, correct for ephemeral
   per-session bits. A group is a durable user record and must be tombstoned
   **only on explicit user delete**, never because its member sessions vanished
   (recovery window, peer offline, closed session). Copying Prune as-is would
   let a brief crash GC the user's saved layouts mesh-wide. Group liveness is
   not member liveness.

2. **Rename must migrate leaves inside group trees.** `sessionattrs.MigrateKey`
   rewrites the attrs map on rename (manual, AI auto-naming, peer-driven),
   `sessionattrs.go:257`. The session-order rank migrates the same way (rewrite
   the renamed key). But `group.tree` stores session keys as leaves _inside a
   blob_ that MigrateKey never sees, so a rename silently drops the renamed
   session out of its group via the frontend's missing-leaf prune. Fix: on
   rename, rewrite matching leaf keys inside every group record and stamp
   `tree_updated_at`, using the same host-ownership guard MigrateKey uses so a
   rename here never clobbers a peer's leaf. Field-level clocks (§2) keep this
   tree write from racing a concurrent `name` edit.

3. **Group tombstones must be durable, not 24h.** `sessionattrs` drops
   tombstones after 24h (`sessionattrs.go:51-53`, `:198-203`). A peer offline
   longer than that reconnects with an old snapshot and resurrects a deleted
   group. Group `deleted_at` tombstones are retained effectively permanently (or
   until every known peer has observed the delete). Sessions are ephemeral;
   group deletes are not.

### Multi-host key resolution (must test, not assume)

Recovery reads/writes by bare `session.Name` (`snapshotter.go:74-75`,
`rebuild.go:87-90`), while group / order keys are host-qualified mesh-wide. A
rebuilt local session's leaf is `<localfp>/<name>` while recovery knows it as
`<name>`. Normalize bare vs `<localfp>/name` and add an explicit mesh-mode
recovery test; do not claim rank / groups reattach "for free" without it.

## Garbage story for dead groups

Delete-only GC (divergence 1) means groups whose sessions are permanently killed
never auto-clear, and dangling leaves accumulate. Keep delete-only on the
server; surface a UI affordance instead (a "missing sessions" indicator on a
group, manual cleanup / "remove dead leaves"). No automatic server GC.

## Interaction with commit 28037cf (already merged)

`fix(web): keep grouping and ordering across server restarts` is the
client-side version of divergence 1. After a WS reconnect the session list is
still converging; the prune effects treated that half-built list as
authoritative and permanently deleted `localStorage` entries for sessions that
had not reappeared yet. The fix adds a 12s `reconnectGrace` window, OR'd with
the existing `recovering` flag into `pruningSuspended`, gating the saved-groups,
session-order, and project-filter prunes (`App.tsx`, `Sidebar.tsx`).

**This work deletes the grace-gated writes for SYNCED data, not all of it.**
Once groups/order are server truth, client-side pruning of those records is
gone (the server owns liveness, no timer guessing). But the grace window still
guards local-only concerns: the live pane-tree / single-view projection and the
project-filter prune (`Sidebar.tsx:416`). Keep grace for those; remove it only
from the synced-group and synced-order paths. So: delete the synced-data prune
effects, keep `pruningSuspended` for local UI projection.

## First-upgrade migration (required, easy to get wrong)

Existing groups/order load from `localStorage` and persist immediately
(`App.tsx:115-145`, `:257-265`; `Sidebar.tsx:227`, `:389-392`). A naive upload
re-creates the exact "empty client wins" failure `sessionattrs` was built to
avoid. One-shot import: on first run after upgrade, fetch server records first,
upload a local group/order entry **only when the server has no record for that
id/key**, then mark migration complete and stop writing the old `localStorage`
keys. Never bulk-overwrite server state from a client.

## Open decisions

- **Auth / multi-user scope.** The existing store is process-global and
  `/api/session-attrs` has no user scope (`server.go:1446-1483`). Decision
  needed: groups/order are global per Termyard mesh (single-user assumption), or
  add user/workspace scoping before shipping if multiple humans share a server.
  Default: global per mesh, matching `sessionattrs` today.
- **Rolling-upgrade capability gate.** Peers ignore unknown message types
  (`session.go:635-636`), so a new `MsgGroup*` / `MsgSessionOrder*` simply does
  nothing on an old binary, that is fine. Advertise the capability so a mixed
  mesh degrades to "no sync" cleanly rather than partial lossy sync.

## Scope boundary

- Two new per-key stores (`sessionorder`, `groupsync`) over one generified
  keyed-record store: keep them separate. They diverge (flat rank vs field-LWW
  record, ephemeral vs durable tombstone), so a shared abstraction would leak.
- No new sync channel. Reuse the peer delta / snapshot transport pattern.
- No sequence CRDT. Fractional rank + `(rank, key)` tie-break + lazy repair.
