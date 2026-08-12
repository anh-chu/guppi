# Tasks — Server-Authoritative Exclusive Group Membership

Ordered: backend enforcement first, then server-side healing, frontend adoption, docs.
Phase gates: verify all tasks in a phase pass before proceeding.

## Phase 1 — Additive: enforce method + naming integration

- [ ] **T1. `store.enforce(now, winnerID) []string` method.**
      `pkg/groupsync/groupsync.go`: new internal method that takes timestamp and optional winner group ID. Iterates all live groups, collects session keys and their memberships. For each key in >1 group, deletes from all but winner (highest mtime or explicit winnerID). For each modified group, if leaf count < 2, sets deleted=true, bumps mtime. Returns list of changed group IDs. Unit test: enforce with key in 2 groups; verify only winner retains key; verify losing group's mtime bumped.

- [ ] **T2. Call `enforce()` on SetTree.**
      `pkg/groupsync/groupsync.go` in SetTree method (or wrapper): after tree is stored, call `enforce(now, nil)`. Collect changed IDs, notify naming coordinator for each. Verify: integration test — push tree with session in 2 groups; snapshot shows session in only 1.

- [ ] **T3. Call `enforce()` on ApplyRemote.**
      After tree is merged in ApplyRemote, call `enforce(now, nil)`. Timestamp is from remote delta or current time. Verify: test ApplyRemote with session in multiple groups; enforce resolves to one winner.

- [ ] **T4. Call `enforce()` on ApplySnapshot.**
      After snapshot is applied, call `enforce(now, nil)`. Verify: test snapshot with duplicates; enforce cleans them up.

- [ ] **T5. Call `enforce()` on MigrateKey.**
      If key migration introduces duplicates, call `enforce(now, nil)`. Verify: test rename a session that appears in multiple groups; enforce resolves.

- [ ] **T6. Naming coordinator integration.**
      `pkg/namer/namer.go`: add `ObserveTreeMutation(groupID, memberSet)` method (or call `Cancel(groupID)` directly after mutation). Coordinator is idempotent. After enforce() in T1-T5, call naming coordinator for each changed group. Verify: unit test — enforce triggers naming notification; naming coordinator receives correct member sets.

## Phase 2 — Server-side dead-session healing

- [ ] **T7. `store.Reconcile(gone func(sessionKey) bool)` method.**
      `pkg/groupsync/groupsync.go`: new exported method. Iterates all groups (live and tombstoned), scans leaf nodes. For each leaf, if `gone(key)` returns true, removes it. If removal causes group to fall <2 leaves, tombstone it. Bumps mtime of modified groups. Verify: unit test — reconcile with dead session; verify leaf removed; verify group tombstoned if subthreshold.

- [ ] **T8. GET `/api/groups` calls Reconcile.**
      `pkg/server/routes_groups.go` in GET handler: before returning groups, call `store.Reconcile(func(key) bool { return isSessionDead(...) })`. Helper `isSessionDead` checks if session's host is online and session absent from current snapshot. Verify: test GET /api/groups with dead session in a group; verify dead-session leaf removed from response.

- [ ] **T9. Transitive dead-session pruning on session removal.**
      `pkg/server/routes_sessions.go` or session kill path: after session is removed, optionally call pruneGroupSessions to clean group leaves. Verify: kill a session; verify its leaves removed from all groups on next GET /api/groups.

## Phase 3 — Frontend snapshot adoption + push gating

- [ ] **T10. Snapshot replaces group cache.**
      `web/src/state/workspaceReducer.ts` in `groups/snapshot` case: if `authoritative: true`, replace `state.layoutGroups` entirely with snapshot's groups (discard old groups not in snapshot). Verify: test snapshot with fewer groups than client cache; verify old groups discarded.

- [ ] **T11. Active group reconciliation (no push-back).**
      In `groups/snapshot` case, after replacement: if active group is in snapshot and snapshot tree differs from local tree, reconcile (adopt server panes without push). Reconciliation adds server panes, keeps local unsaved state, never prunes server panes. Verify: test snapshot with new panes in active group; verify panes adopted; verify no PUT triggered.

- [ ] **T12. Push gating: only user edits trigger PUT.**
      `web/src/state/workspaceReducer.ts`: add `isUserInitiatedEdit` check (split/close/resize/drag/pair actions). Only these actions trigger PUT `/api/groups/{id}`. Group switch, snapshot reconcile, other actions do not push. Verify: test group switch; verify no PUT. Test user split; verify PUT fires.

- [ ] **T13. `skipTreeAdoptFor` guard during edit.**
      During local pane-tree edit (split/close), set `skipTreeAdoptFor = true`. In `groups/snapshot` handler, check guard: if true, skip reconciliation. After edit completes (PUT succeeds), clear guard. Verify: test snapshot during edit; verify snapshot not adopted until edit completes.

- [ ] **T14. Delete `detachFromOtherGroups` client-side logic (if exists).**
      Remove client-side assumption that moving a session removes it from old groups. Server enforces this. Verify: client no longer has this logic; server enforcement is the single source of truth.

## Phase 4 — Documentation & E2E

- [ ] **T15. Update `docs/ux-contracts.md` section 2.1a.**
      Add detail that layout group dissolution and exclusive membership are server-enforced in `pkg/groupsync` on every write path (SetTree/ApplyRemote/ApplySnapshot/MigrateKey). Verification pointer: `pkg/groupsync/groupsync.go` enforce method.

- [ ] **T16. Add groups sync contract to `docs/ux-contracts.md` (~section 10.5 area).**
      New subsection or expanded existing: "Groups sync & exclusive membership" describing:
      - Exclusive membership: session in at most one live group, enforced server-side.
      - Dissolution: groups <2 leaves are tombstoned, not deleted.
      - Dead-session healing: pruned opportunistically on GET /api/groups.
      - Tombstone LWW: peer deltas with older timestamp than tombstone are rejected.
      - Naming: enforcement notifies naming coordinator to clean UNNAMED ghosts.
      Verification pointers: `pkg/groupsync/groupsync.go`, `web/src/state/workspaceReducer.ts`.

- [ ] **T17. E2E: session move (exclusive membership).**
      User moves session X from group A to group B (drag or split). Verify: server enforces X removed from A, added to B. Client snapshot reflects X in only B. Kill session X; verify removed from all groups. Check `/api/groups` response; X not in any group.

- [ ] **T18. E2E: group dissolution.**
      Create group with 2 sessions. Kill one. Verify: group falls to 1 leaf and is tombstoned (marked deleted in `/api/groups` response or absent from live list). Remaining session is standalone (not under group header in sidebar).

- [ ] **T19. E2E: dead-session pruning.**
      Create group with session X. Kill host or session X (via CLI or API). Next GET /api/groups (refresh sidebar). Verify: X's leaf removed from group. If group is now <2 leaves, tombstoned.

- [ ] **T20. E2E: peer delta LWW with tombstone.**
      Tombstone a group (delete all leaves). Meanwhile, simulate a peer delta arriving with older timestamp that attempts to add a leaf. Verify: delta is rejected (LWW: delta mtime < tombstone mtime). Tombstone persists. A newer delta reactivates the group (correct LWW).

- [ ] **T21. E2E: client push gating.**
      Client switches group (no edit). Verify: no PUT. Client splits a pane (user edit). Verify: PUT fires. Snapshot arrives during edit; verify snapshot not adopted until PUT completes.

- [ ] **T22. Integration tests for naming coordinator.**
      `pkg/namer_test.go` or similar: verify ObserveTreeMutation is called after enforce() with correct member sets. Coordinator can detect UNNAMED changes and Clean up.

## Phase 5 — Validation & cleanup

- [ ] **T23. Run `go test ./pkg/groupsync ./pkg/server ./pkg/namer` + `web npm test`.**
      All new and existing tests pass. Verify no regressions in group operations.

- [ ] **T24. Load test: enforce() on large group trees.**
      Benchmark enforce() with 100+ groups, 1000+ sessions. Verify no performance regression. Reconcile() on large trees under load.

- [ ] **T25. End-to-end: peer mesh with exclusive membership.**
      Multi-peer setup: groups created on peer A, session moved on peer B, verify all peers converge to exclusive membership via broadcasts and snapshots.

- [ ] **T26. Documentation review.**
      Verify `ux-contracts.md` sections 2.1a and groups sync are clear, complete, and have correct verification pointers. Review by code owner.

## Acceptance on completion

- All phases complete and tests pass.
- `go test ./...` and `web npm test` succeed.
- E2E tests (T17-T22) all pass.
- Session key appears in exactly one live group after any write.
- Groups <2 leaves are tombstoned, never live.
- Dead-session leaves pruned on GET /api/groups.
- Peer deltas with timestamp < tombstone are rejected (LWW safe).
- Client pushes only on user edits; snapshot reconciliation does not push back.
- Naming coordinator is notified and can clean UNNAMED ghosts after enforcement.
- Docs updated; verification pointers confirmed.
