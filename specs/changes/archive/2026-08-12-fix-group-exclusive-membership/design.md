# Design — Server-Authoritative Exclusive Group Membership

**Status:** approved
**Parent:** `proposal.md`, `requirements.md`

## Overview

Enforcement of exclusive membership is a **server-side responsibility**, running automatically on every write path. The server is the authoritative source of truth for group membership; clients adopt server truth via snapshot and never push back partial/divergent states.

**Core principle:** After every write (SetTree, ApplyRemote, ApplySnapshot, MigrateKey), each session key appears as an exclusive leaf in exactly one live group. All other occurrences are pruned, and groups falling below the 2-leaf threshold are tombstoned.

## Storage & persistence

### Group record structure (unchanged API contract)

```go
// pkg/groupsync/groupsync.go
type TreeEntry struct {
    ID    string      // unique group ID
    Name  string      // display name (may be UNNAMED auto-generated)
    Rank  int         // ordering
    Tree  *paneTree   // pane structure with leaves (session keys)
    Mtime int64       // last modification timestamp
    // New field (implicit in behavior):
    Deleted bool      // true if tombstoned; persisted with updated Mtime
}
```

**Disk persistence** (`groups.json`): Tombstoned groups remain as records with `deleted: true` and an updated timestamp. This allows LWW to correctly reject stale deltas (a delta with `mtime < tombstone_mtime` loses). Over time, opportunistic pruning (see R3) may remove tombstone records, freeing space.

## Enforcement mechanism

### `enforce(now, winnerID) []string` method (internal)

Runs on every write path (SetTree, ApplyRemote, ApplySnapshot, MigrateKey).

```go
// pkg/groupsync/groupsync.go (new method)
func (s *store) enforce(now int64, winnerID *string) []string {
    // Returns list of group IDs whose membership changed (for naming notification)
    
    // Step 1: Collect all session keys and their group memberships
    // keysToGroups := map[sessionKey] []groupID (e.g., "host/sessionA" -> ["gr1", "gr2"])
    // Iterate all live trees, collect leaf nodes
    
    // Step 2: For each key appearing in >1 group, delete from all but winner
    // Winner is:
    //   - If winnerID provided, that group (must contain the key)
    //   - Else, group with highest mtime (most recently modified)
    // Update each losing group's tree, bump mtime
    
    // Step 3: For each group that now has <2 leaves, tombstone it
    // Set deleted=true, bump mtime
    
    // Step 4: Collect changed group IDs (for naming notification)
    // Return list
    
    return changedGroupIDs
}
```

### When `enforce()` is called

1. **SetTree (local client PUT):** After tree is stored, before broadcast. Enforces membership in case the pushed tree had duplicates (client bug or stale state).
2. **ApplyRemote (peer delta):** After tree is merged, enforce to resolve any LWW conflicts introduced by the delta.
3. **ApplySnapshot (client reconciliation):** After snapshot is applied, enforce to ensure the new state is clean.
4. **MigrateKey (session renamed):** If the key move introduces duplicates (same key in old and new positions), enforce.

## Exclusive membership & tombstoning

### Algorithm

For each session key appearing in multiple groups:
1. Find all groups containing it.
2. Pick the winner (group with highest `mtime`, or explicit `winnerID`).
3. Prune the key from all other groups.
4. For each pruned group, update its `mtime` to `now` (timestamped prune).

After all keys are resolved, for each group:
- If live leaves count < 2, set `deleted = true`, update `mtime = now`.
- If live leaves count >= 2, ensure `deleted = false`.

### Pruning does not delete trees

A pruned group's tree record remains on disk (with `deleted = true` if subthreshold) for LWW correctness. A later delta with newer `mtime` can re-activate it.

## Dead-session reconciliation

### `Reconcile(gone func(sessionKey) bool)` method (exported)

Called by GET `/api/groups` route.

```go
// pkg/groupsync/groupsync.go (exported)
func (s *store) Reconcile(gone func(string) bool) {
    // For each live group, scan leaf nodes (session keys)
    // If gone(key) returns true, remove that leaf from the tree
    // If any group falls below 2 leaves after pruning, tombstone it
    // Bump mtime of modified groups
}
```

### Integration

In `pkg/server/routes_groups.go` GET `/api/groups` handler:

```go
// Before returning groups, run reconciliation
store.Reconcile(func(sessionKey string) bool {
    // Check if session is dead:
    // - owner host online (peer reachable via peerMgr)
    // - session NOT in current /api/sessions snapshot
    // If both true, session is dead
    return isSessionDead(sessionKey, stateMgr, peerMgr)
})

// Return healed groups to client
```

This is best-effort: reconciliation happens every GET, but clients may linger with stale state until they refresh.

## Naming coordinator notification

### `ObserveTreeMutation(groupID, newMembers)` + `Cancel(groupID)`

After `enforce()` completes, for each group whose membership changed:

```go
if changedGroupIDs := store.enforce(now); len(changedGroupIDs) > 0 {
    for _, gid := range changedGroupIDs {
        // Notify naming coordinator
        namingCoordinator.ObserveTreeMutation(gid, store.getMembers(gid))
        // Coordinator detects stale UNNAMED names and calls Cancel(gid)
        // to trigger regeneration on next name refresh
    }
}
```

The coordinator is idempotent; calling it multiple times per group is safe. Implementation details are in `pkg/namer`.

## Frontend client behavior

### Snapshot reconciliation (without push-back)

In `web/src/state/workspaceReducer.ts` `groups/snapshot` case:

```typescript
case 'groups/snapshot':
  // action.payload = { groups, authoritative: true }
  if (action.payload.authoritative) {
    // Replace client group cache entirely
    state.layoutGroups = new Map(action.payload.groups.map(g => [g.id, g]));
    
    // Reconcile active group's local pane tree (adopt server membership)
    if (shouldReconcileActiveGroup(state) && !isLocalEditInFlight(state)) {
      const activeGroupFromServer = state.layoutGroups.get(state.activeGroupID);
      if (activeGroupFromServer && activeGroupFromServer.tree) {
        // Adopt server panes, keep local unsaved state
        reconcileActivePaneTree(state, activeGroupFromServer.tree);
      }
    }
    
    // NO PUSH: snapshot reconciliation is not an edit
  }
```

Key points:
- **Replace, don't merge.** Old groups not in snapshot are discarded from cache.
- **Adopt server membership.** Local panes from server are trusted; client adds its own edits but doesn't prune server panes.
- **No push-back.** The reconciliation itself is not a user edit; it doesn't generate a PUT.

### Push gating (only on user edits)

Track `paneTreeRev` (server version) and local `rev` (client version):

```typescript
// In useWorkspace or a reducer effect
if (isUserInitiatedEdit(action)) {  // split/close/resize/drag/pair
  const newRev = state.paneTreeRev + 1;
  const localTree = buildLocalTree(state);
  
  // Push to server only if edit is complete (not partial)
  PUT('/api/groups/{id}', { tree: localTree, rev: newRev })
    .then(() => {
      // Bump server version
      state.paneTreeRev = newRev;
    });
} else if (action.type === 'groups/snapshot') {
  // Reconcile without push
  reconcileSnapshot(action.payload);
}
```

Also guard against snapshot landing during edit (`skipTreeAdoptFor` mechanism):

```typescript
// During local edit, set skipTreeAdoptFor = true
// Snapshot handler checks this guard:
if (!state.skipTreeAdoptFor) {
  reconcileSnapshot(...);
}
```

### No `detachFromOtherGroups` client-side logic

Delete the client's blind assumption that moving a session means removing it from old groups. The server enforces this; clients trust the server's snapshot.

## Server-side pruning mirroring

### `pruneGroupSessions(gone func(key) bool)` in server routes

Mirrors `pruneSessionAttrs(gone ...)` from session attribute handling:

```go
// pkg/server/routes_groups.go (new or updated)
func (opts *Options) pruneGroupSessions(gone func(string) bool) {
    opts.GroupSync.Reconcile(gone)
}

// In relevant endpoints (session removal, host disconnect, etc.):
opts.pruneGroupSessions(func(key string) bool {
    // Check if key is dead
    return !sessionExists(key, opts.StateMgr, opts.PeerMgr)
})
```

This ensures that when a session is killed or a host goes offline, associated group leaves are cleaned opportunistically.

## API contract (unchanged)

- **GET `/api/groups`** → returns all groups (excluding `deleted: true` records, or including tombstones with `deleted` flag for informational purposes; clarify in implementation).
- **POST `/api/groups`** → SetTree/Name/Rank/Delete operations; each triggers `enforce()`.
- **WS `groups-updated`** → broadcast includes enforced state; clients adopt it.
- **WS `groups/snapshot`** → authoritative server state; marked `authoritative: true`.

No new endpoint or message type; enforcement is transparent.

## Constants & timing

- **Group dissolution threshold:** 2 leaves (unchanged).
- **Enforce on every write:** SetTree, ApplyRemote, ApplySnapshot, MigrateKey (all sync paths).
- **Dead-session check on GET:** Per request, best-effort.
- **Naming notification:** Per enforcement; idempotent.

## Test strategy

### Server-side (pkg/groupsync)

1. **Exclusive membership enforcement:** Test enforce() with session in multiple groups; verify only winner remains.
2. **Tombstoning:** Test group dissolution when membership < 2.
3. **Peer delta LWW:** Test ApplyRemote with stale delta < tombstone timestamp; verify delta is rejected.
4. **Reconciliation:** Test Reconcile with gone callback; verify dead-session leaves are removed.
5. **Naming notification:** Test enforce() calls ObserveTreeMutation with correct member sets.

### Frontend (web/src/state/workspaceReducer.ts)

1. **Snapshot reconciliation:** Test snapshot replace and active-group reconcile without push.
2. **Push gating:** Test user edits push, group switch doesn't push.
3. **skipTreeAdoptFor guard:** Test snapshot during edit doesn't reconcile.

### E2E

1. User moves session X from group A to group B; verify X removed from A, added to B.
2. Kill session X; verify removed from all groups.
3. Group falls to 1 leaf; verify tombstoned.
4. Peer receives stale delta after tombstone; verify delta rejected.
5. Session's host dies; GET /api/groups prunes its leaves.

## Risks & mitigations

| Risk | Mitigation |
|---|---|
| `Reconcile` blocks GET if gone-check is slow | Callback is in-memory, not I/O. Tested under load. |
| Naming coordinator spam if enforce() called many times | Idempotent; safe to call many times. Coordinator dedupes. |
| Stale client state during partition | Documented in ux-contracts; server snapshot on reconnect fixes it. |
| Disk bloat from tombstones | Opportunistic pruning over time; optional on-demand cleanup API (future). |
| Complex LWW semantics | Timestamp bumps on prune ensure correctness; documented in requirements. |

## Related files

- `pkg/groupsync/groupsync.go` - store, enforce, Reconcile methods
- `pkg/server/routes_groups.go` - GET/POST handlers, Reconcile call
- `pkg/namer/namer.go` - ObserveTreeMutation, Cancel
- `web/src/state/workspaceReducer.ts` - snapshot case, push gating
- `docs/ux-contracts.md` - contract documentation (sections 2.1a, groups sync)
