import { describe, it, expect } from 'vitest'
import {
  workspaceReducer,
  createInitialWorkspaceState,
  sessionKey,
  planLegacyMigration,
  type WorkspaceAction,
  type WorkspaceState,
  type LegacyMigrationInput,
} from './workspaceReducer'
import { getLeaves, type PaneTree } from '../lib/paneTree'
import type { Session } from '../hooks/useSessions'
import type { GroupRecordMap } from '../hooks/useGroupSync'

function sess(name: string, host = ''): Session {
  return {
    id: name,
    name,
    host: host || undefined,
    windows: [],
    created: new Date().toISOString(),
  }
}

function reduce(state: WorkspaceState, ...actions: WorkspaceAction[]): WorkspaceState {
  return actions.reduce((s, a) => workspaceReducer(s, a), state)
}

function splitAction(targetKey: string, newKey: string, direction: 'h' | 'v' = 'h'): WorkspaceAction {
  return { type: 'view/split', targetKey, direction, newKey }
}

function snapshot(sessions: Session[], generation: number, now = 0, authoritative = false): WorkspaceAction {
  return { type: 'sessions/snapshot', sessions, generation, now, authoritative }
}

function connection(live: boolean, livenessUnknown: boolean): WorkspaceAction {
  return { type: 'connection', live, livenessUnknown }
}

describe('workspaceReducer', () => {
  it('first empty snapshot leaves sessions empty and loading false', () => {
    const s0 = createInitialWorkspaceState()
    expect(s0.loading).toBe(true)
    const s1 = workspaceReducer(s0, snapshot([], 1, 0))
    expect(s1.sessions).toEqual([])
    expect(s1.loading).toBe(false)
    expect(s1.connection.livenessUnknown).toBe(true)
  })

  it('non-authoritative snapshot hydrates sessions but does not clear liveness unknown', () => {
    const s0 = createInitialWorkspaceState()
    const sessions = [sess('alpha')]
    const s1 = workspaceReducer(s0, snapshot(sessions, 1, 0, false)) // non-authoritative
    expect(s1.sessions.map(sessionKey)).toEqual(['alpha'])
    expect(s1.loading).toBe(false)
    expect(s1.connection.livenessUnknown).toBe(true)
  })

  it('authoritative snapshot hydrates sessions and clears liveness unknown', () => {
    const s0 = createInitialWorkspaceState()
    const sessions = [sess('alpha')]
    const s1 = workspaceReducer(s0, snapshot(sessions, 1, 0, true)) // authoritative
    expect(s1.sessions.map(sessionKey)).toEqual(['alpha'])
    expect(s1.loading).toBe(false)
    expect(s1.connection.livenessUnknown).toBe(false)
  })

  it('authoritative snapshot immediately prunes missing leaves', () => {
    const s0 = createInitialWorkspaceState()
    const s1 = reduce(
      s0,
      connection(true, false),
      splitAction('alpha', 'beta'),
      snapshot([sess('alpha'), sess('beta')], 1, 0, false), // non-authoritative (connection live but liveness unknown)
    )
    expect(s1.view.paneTree).not.toBeNull()
    expect(sessionKeyFromLeaves(s1)).toContain('alpha')
    expect(sessionKeyFromLeaves(s1)).toContain('beta')
    // Now reconnect with an authoritative snapshot missing 'beta'
    const s2 = workspaceReducer(s1, {
      type: 'sessions/snapshot',
      sessions: [sess('alpha')],
      generation: 2,
      now: 100,
      authoritative: true,
    })
    expect(s2.view.paneTree).not.toBeNull()
    expect(sessionKeyFromLeaves(s2)).toEqual(['alpha'])
    expect(s2.connection.livenessUnknown).toBe(false)
  })

  it('non-authoritative snapshot does not prune or reconcile pane tree', () => {
    const s0 = createInitialWorkspaceState()
    const s1 = reduce(
      s0,
      connection(true, false),
      splitAction('alpha', 'beta'),
      snapshot([sess('alpha'), sess('beta')], 1, 0, false), // non-authoritative
    )
    expect(sessionKeyFromLeaves(s1)).toEqual(['alpha', 'beta'])
    // Non-authoritative snapshot missing 'beta' should NOT prune it
    const s2 = workspaceReducer(s1, snapshot([sess('alpha')], 2, 100, false))
    expect(sessionKeyFromLeaves(s2)).toEqual(['alpha', 'beta'])
    expect(s2.view.paneTree).not.toBeNull()
  })

  it('authoritative empty snapshot prunes all local sessions, clears activeKey and singleView', () => {
    const s0 = createInitialWorkspaceState()
    const s1 = reduce(
      s0,
      connection(true, false),
      splitAction('alpha', 'beta'),
      snapshot([sess('alpha'), sess('beta')], 1, 0, false),
      { type: 'view/setActiveKey', key: 'alpha' },
      { type: 'view/setSingleView', sessionKey: 'gamma' }, // singleView to a missing session
    )
    expect(s1.view.paneTree).not.toBeNull()
    expect(s1.view.activeKey).toBe('alpha')
    // Reconnect with authoritative empty snapshot
    const s2 = workspaceReducer(s1, {
      type: 'sessions/snapshot',
      sessions: [],
      generation: 2,
      now: 100,
      authoritative: true,
    })
    expect(s2.view.paneTree).toBeNull()
    expect(s2.view.activeKey).toBeNull()
    expect(s2.view.singleView).toBeNull()
    expect(s2.connection.livenessUnknown).toBe(false)
  })

  it('saved group with stale session key is pruned on authoritative snapshot', () => {
    const s0 = createInitialWorkspaceState()
    const s1 = reduce(
      s0,
      snapshot([sess('alpha'), sess('beta'), sess('gamma')], 1, 0, false),
      { type: 'groups/snapshot', groups: {
        g1: { tree: { type: 'leaf' as const, sessionKey: 'alpha' }, rank: 'a0' },
        g2: { tree: { type: 'leaf' as const, sessionKey: 'beta' }, rank: 'b0' },
      }, generation: 1 },
    )
    const g1Tree = s1.groups.g1?.tree
    const g2Tree = s1.groups.g2?.tree
    expect(g1Tree && g1Tree.type === 'leaf' && g1Tree.sessionKey).toBe('alpha')
    expect(g2Tree && g2Tree.type === 'leaf' && g2Tree.sessionKey).toBe('beta')
    // Authoritative snapshot: only alpha and gamma remain. beta is gone.
    const s2 = workspaceReducer(s1, {
      type: 'sessions/snapshot',
      sessions: [sess('alpha'), sess('gamma')],
      generation: 2,
      now: 0,
      authoritative: true,
    })
    // g1 is still present with alpha
    const s2g1Tree = s2.groups.g1?.tree
    expect(s2g1Tree && s2g1Tree.type === 'leaf' && s2g1Tree.sessionKey).toBe('alpha')
    // g2's tree is pruned away, and the group is deleted
    expect(s2.groups.g2).toBeUndefined()
  })

  it('authoritative snapshot deletes a group when pruned to single-leaf', () => {
    const s0 = createInitialWorkspaceState()
    const s1 = reduce(
      s0,
      snapshot([sess('alpha'), sess('beta'), sess('gamma')], 1, 0, false),
      { type: 'groups/snapshot', groups: {
        g1: { tree: { type: 'split', direction: 'h', ratio: 0.5, first: { type: 'leaf', sessionKey: 'alpha' }, second: { type: 'leaf', sessionKey: 'beta' } }, rank: 'a0' },
      }, generation: 1 },
    )
    // g1 has two leaves: alpha and beta
    const g1Before = s1.groups.g1?.tree
    if (g1Before && g1Before.type === 'split') {
      const leaves = [getLeaves(g1Before)]
      expect(leaves[0]?.length).toBe(2)
    }
    // Authoritative snapshot: only alpha and gamma remain, beta is gone.
    const s2 = workspaceReducer(s1, {
      type: 'sessions/snapshot',
      sessions: [sess('alpha'), sess('gamma')],
      generation: 2,
      now: 0,
      authoritative: true,
    })
    // g1 is now single-leaf (alpha only) and must be deleted
    expect(s2.groups.g1).toBeUndefined()
  })

  it('sessions/remove deletes a group when pruned to single-leaf', () => {
    const s0 = createInitialWorkspaceState()
    const s1 = reduce(
      s0,
      snapshot([sess('alpha'), sess('beta')], 1, 0, false),
      { type: 'groups/snapshot', groups: {
        g1: { tree: { type: 'split', direction: 'h', ratio: 0.5, first: { type: 'leaf', sessionKey: 'alpha' }, second: { type: 'leaf', sessionKey: 'beta' } }, rank: 'a0' },
      }, generation: 1 },
    )
    // g1 has two leaves: alpha and beta
    expect(s1.groups.g1).toBeDefined()
    // Remove beta: the group should be left with just alpha (single-leaf) and deleted
    const s2 = workspaceReducer(s1, { type: 'sessions/remove', key: 'beta' })
    expect(s2.groups.g1).toBeUndefined()
  })

  it('renames a session across sessions, view, and wiki state', () => {
    const s0 = createInitialWorkspaceState()
    const s1 = reduce(
      s0,
      snapshot([sess('alpha', 'host')], 1, 0),
      splitAction('host/alpha', 'host/beta'),
      { type: 'wiki/open', target: { path: '/x', session: 'alpha', hostId: 'host', nonce: 1 } },
    )
    const s2 = workspaceReducer(s1, { type: 'rename', oldKey: 'host/alpha', newKey: 'host/alpha2' })
    expect(s2.sessions[0].name).toBe('alpha2')
    expect(sessionKeyFromLeaves(s2)).toContain('host/alpha2')
    expect(sessionKeyFromLeaves(s2)).not.toContain('host/alpha')
    expect(s2.wiki.target?.session).toBe('alpha2')
    expect(s2.wiki.history[0]?.session).toBe('alpha2')
  })

  it('removes active pane and focuses the next leaf', () => {
    const s0 = createInitialWorkspaceState()
    const s1 = reduce(
      s0,
      splitAction('alpha', 'beta'),
      { type: 'view/setActiveKey', key: 'alpha' },
    )
    const s2 = workspaceReducer(s1, { type: 'view/close', sessionKey: 'alpha' })
    expect(sessionKeyFromLeaves(s2)).toEqual(['beta'])
    expect(s2.view.activeKey).toBe('beta')
  })

  describe('group dissolve and promote', () => {
    it('dissolves a single-leaf group into singleView', () => {
      const s0 = createInitialWorkspaceState()
      const s1 = reduce(
        s0,
        { type: 'view/select', key: 'alpha' },
        { type: 'view/dissolveToSingle' },
      )
      expect(s1.view.singleView).toBe('alpha')
      expect(s1.view.paneTree).toBeNull()
      expect(s1.view.activeKey).toBeNull()
    })

    it('promotes the next ranked group when the active group empties', () => {
      const s0 = createInitialWorkspaceState()
      const groups = {
        g1: { tree: { type: 'leaf' as const, sessionKey: 'a' }, rank: 'b' },
        g2: { tree: { type: 'leaf' as const, sessionKey: 'c' }, rank: 'a' },
      } as const
      const s1 = reduce(
        s0,
        { type: 'groups/snapshot', groups, generation: 1 },
        { type: 'view/setActiveGroup', groupId: 'g1', tree: groups.g1.tree },
        { type: 'view/promoteNextGroup' },
      )
      expect(s1.view.activeGroupId).toBe('g2')
      expect(s1.view.paneTree).toEqual(groups.g2.tree)
      expect(s1.view.activeKey).toBe('c')
      expect(s1.view.currentView).toBe('session')
    })
  })

  describe('optimistic stub reconciliation', () => {
    it('keeps optimistic stubs until confirmed or TTL expired', () => {
      const s0 = createInitialWorkspaceState()
      const stub = sess('pending')
      const s1 = workspaceReducer(s0, { type: 'optimistic/add', session: stub, now: 0 })
      expect(s1.sessions.map(sessionKey)).toContain('pending')
      const s2 = workspaceReducer(s1, snapshot([sess('other')], 1, 1000))
      expect(s2.sessions.map(sessionKey)).toContain('pending')
      const s3 = workspaceReducer(s2, { type: 'optimistic/add', session: stub, now: 1000 })
      const s4 = workspaceReducer(s3, snapshot([sess('other')], 2, 8000))
      expect(s4.sessions.map(sessionKey)).not.toContain('pending')
    })
  })

  describe('wiki target and history', () => {
    it('preserves target and history across rename and close', () => {
      const s0 = createInitialWorkspaceState()
      const target = { path: '/foo', session: 'alpha', nonce: 1 }
      const s1 = workspaceReducer(s0, { type: 'wiki/open', target })
      expect(s1.wiki.target).toEqual(target)
      expect(s1.wiki.history).toHaveLength(1)
      const s2 = workspaceReducer(s1, { type: 'rename', oldKey: 'alpha', newKey: 'beta' })
      expect(s2.wiki.target?.session).toBe('beta')
      expect(s2.wiki.history[0].session).toBe('beta')
      const s3 = workspaceReducer(s2, { type: 'wiki/close' })
      expect(s3.wiki.target).toBeNull()
      expect(s3.wiki.history).toHaveLength(1)
    })
  })

  describe('restore', () => {
    it('restores persisted view and wiki state', () => {
      const s0 = createInitialWorkspaceState()
      const tree = { type: 'leaf' as const, sessionKey: 'x' }
      const restored = workspaceReducer(s0, {
        type: 'restore',
        snapshot: {
          view: { paneTree: tree, activeKey: 'x', activeGroupId: 'g', singleView: null, currentView: 'session', settingsOpen: false, paneTreeRev: 0, paneTreeRevSynced: 0 },
          wiki: { target: { path: '/f', nonce: 5 }, history: [] },
        },
      })
      expect(restored.view.paneTree).toEqual(tree)
      expect(restored.view.activeGroupId).toBe('g')
      expect(restored.wiki.target).toEqual({ path: '/f', nonce: 5 })
    })
  })

  describe('groups/snapshot behavior', () => {
    it('replaces groups wholesale', () => {
      const s0 = createInitialWorkspaceState()
      const groups1: GroupRecordMap = {
        g1: { tree: { type: 'leaf', sessionKey: 'a' }, rank: 'a0' },
        g2: { tree: { type: 'leaf', sessionKey: 'b' }, rank: 'a1' },
      }
      const s1 = workspaceReducer(s0, {
        type: 'groups/snapshot',
        groups: groups1,
      })
      expect(Object.keys(s1.groups).sort()).toEqual(['g1', 'g2'])
      // Now replace with new groups
      const groups2: GroupRecordMap = {
        g3: { tree: { type: 'leaf', sessionKey: 'c' }, rank: 'b0' },
      }
      const s2 = workspaceReducer(s1, {
        type: 'groups/snapshot',
        groups: groups2,
      })
      expect(Object.keys(s2.groups)).toEqual(['g3'])
      expect(s2.groups.g1).toBeUndefined()
    })

    it('adopts active-group tree when it differs from local paneTree and rev === revSynced', () => {
      const localTree: PaneTree = { type: 'leaf', sessionKey: 'local' }
      const serverTree: PaneTree = { type: 'leaf', sessionKey: 'server' }
      const s0 = createInitialWorkspaceState()
      const s1 = reduce(
        s0,
        { type: 'sessions/snapshot', sessions: [sess('local'), sess('server')], generation: 1, now: 0, authoritative: true },
        { type: 'view/setPaneTree', tree: localTree },
        { type: 'groups/treeSaved', id: s0.view.activeGroupId, rev: 1 },
      )
      expect(s1.view.paneTree).toEqual(localTree)
      expect(s1.view.paneTreeRev).toBe(1) // bumped by setPaneTree
      expect(s1.view.paneTreeRevSynced).toBe(1) // synced by treeSaved
      const activeId = s1.view.activeGroupId
      const s2 = workspaceReducer(s1, {
        type: 'groups/snapshot',
        groups: { [activeId]: { tree: serverTree } },
      })
      expect(s2.view.paneTree).toEqual(serverTree) // adopted
      expect(s2.view.paneTreeRev).toBe(1) // NOT bumped by adoption
    })

    it('repairs activeKey when adopted tree has different leaves', () => {
      const oldTree: PaneTree = { type: 'leaf', sessionKey: 'gone' }
      const newTree: PaneTree = {
        type: 'split',
        direction: 'h',
        ratio: 0.5,
        first: { type: 'leaf', sessionKey: 'a' },
        second: { type: 'leaf', sessionKey: 'b' },
      }
      const s0 = createInitialWorkspaceState()
      const s1 = reduce(
        s0,
        { type: 'sessions/snapshot', sessions: [sess('gone'), sess('a'), sess('b')], generation: 1, now: 0, authoritative: true },
        { type: 'view/setPaneTree', tree: oldTree },
        { type: 'view/setActiveKey', key: 'gone' },
        { type: 'groups/treeSaved', id: s0.view.activeGroupId, rev: 1 },
      )
      expect(s1.view.activeKey).toBe('gone')
      const activeId = s1.view.activeGroupId
      const s2 = workspaceReducer(s1, {
        type: 'groups/snapshot',
        groups: { [activeId]: { tree: newTree } },
      })
      expect(s2.view.paneTree).toEqual(newTree)
      expect(s2.view.activeKey).toBe('a') // repaired to first leaf
    })

    it('respects skipTreeAdoptFor and does not adopt those group trees', () => {
      const localTree: PaneTree = { type: 'leaf', sessionKey: 'local' }
      const serverTree: PaneTree = { type: 'leaf', sessionKey: 'server' }
      const s0 = createInitialWorkspaceState()
      const s1 = reduce(
        s0,
        { type: 'sessions/snapshot', sessions: [sess('local'), sess('server')], generation: 1, now: 0, authoritative: true },
        { type: 'view/setPaneTree', tree: localTree },
        { type: 'groups/treeSaved', id: s0.view.activeGroupId, rev: 1 },
      )
      const activeId = s1.view.activeGroupId
      const s2 = workspaceReducer(s1, {
        type: 'groups/snapshot',
        groups: { [activeId]: { tree: serverTree } },
        skipTreeAdoptFor: [activeId],
      })
      expect(s2.view.paneTree).toEqual(localTree) // NOT adopted due to skipTreeAdoptFor
    })

    it('does NOT clear paneTree when active group absent but covered by skipTreeAdoptFor (in-flight tree POST)', () => {
      const localTree: PaneTree = { type: 'leaf', sessionKey: 'local' }
      const s0 = createInitialWorkspaceState()
      const s1 = reduce(
        s0,
        { type: 'sessions/snapshot', sessions: [sess('local')], generation: 1, now: 0, authoritative: true },
        { type: 'view/setPaneTree', tree: localTree },
      )
      const activeId = s1.view.activeGroupId
      // Snapshot from a racing POST that predates the new group's persistence.
      const s2 = workspaceReducer(s1, {
        type: 'groups/snapshot',
        groups: {},
        skipTreeAdoptFor: [activeId],
      })
      expect(s2.view.paneTree).toEqual(localTree)
      expect(s2.view.activeKey).toBe(s1.view.activeKey)
    })

    it('clears paneTree when active group absent and not in-flight', () => {
      const localTree: PaneTree = { type: 'leaf', sessionKey: 'local' }
      const s0 = createInitialWorkspaceState()
      const s1 = reduce(
        s0,
        { type: 'sessions/snapshot', sessions: [sess('local')], generation: 1, now: 0, authoritative: true },
        { type: 'view/setPaneTree', tree: localTree },
      )
      const s2 = workspaceReducer(s1, { type: 'groups/snapshot', groups: {} })
      expect(s2.view.paneTree).toBeNull()
      expect(s2.view.activeKey).toBeNull()
    })

    it('rename bumps paneTreeRev when the pane tree references the old key', () => {
      const tree: PaneTree = {
        type: 'split', direction: 'h', ratio: 0.5,
        first: { type: 'leaf', sessionKey: 'a' },
        second: { type: 'leaf', sessionKey: 'shell' },
      }
      const s0 = createInitialWorkspaceState()
      const s1 = reduce(
        s0,
        { type: 'sessions/snapshot', sessions: [sess('a'), sess('shell')], generation: 1, now: 0, authoritative: true },
        { type: 'view/setPaneTree', tree },
      )
      const revBefore = s1.view.paneTreeRev
      const s2 = workspaceReducer(s1, { type: 'rename', oldKey: 'shell', newKey: 'shell-3' })
      expect(s2.view.paneTreeRev).toBe(revBefore + 1)
      expect(s2.view.paneTree).toEqual({
        type: 'split', direction: 'h', ratio: 0.5,
        first: { type: 'leaf', sessionKey: 'a' },
        second: { type: 'leaf', sessionKey: 'shell-3' },
      })
    })

    it('rename does not bump paneTreeRev when the tree does not reference the old key', () => {
      const tree: PaneTree = { type: 'leaf', sessionKey: 'a' }
      const s0 = createInitialWorkspaceState()
      const s1 = reduce(
        s0,
        { type: 'sessions/snapshot', sessions: [sess('a'), sess('b')], generation: 1, now: 0, authoritative: true },
        { type: 'view/setPaneTree', tree },
      )
      const revBefore = s1.view.paneTreeRev
      const s2 = workspaceReducer(s1, { type: 'rename', oldKey: 'b', newKey: 'b-2' })
      expect(s2.view.paneTreeRev).toBe(revBefore)
    })

    it('does NOT adopt tree when paneTreeRev > paneTreeRevSynced (pending local edit)', () => {
      const localTree: PaneTree = { type: 'leaf', sessionKey: 'local' }
      const serverTree: PaneTree = { type: 'leaf', sessionKey: 'server' }
      const s0 = createInitialWorkspaceState()
      const s1 = reduce(
        s0,
        { type: 'sessions/snapshot', sessions: [sess('local'), sess('server')], generation: 1, now: 0, authoritative: true },
        { type: 'view/setPaneTree', tree: localTree },
      )
      expect(s1.view.paneTreeRev).toBe(1)
      expect(s1.view.paneTreeRevSynced).toBe(0)
      const activeId = s1.view.activeGroupId
      const s2 = workspaceReducer(s1, {
        type: 'groups/snapshot',
        groups: { [activeId]: { tree: serverTree } },
      })
      expect(s2.view.paneTree).toEqual(localTree) // NOT adopted because rev > revSynced
    })

    it('adopts tree after treeSaved unblocks by matching revSynced to rev', () => {
      const localTree: PaneTree = { type: 'leaf', sessionKey: 'local' }
      const serverTree: PaneTree = { type: 'leaf', sessionKey: 'server' }
      const s0 = createInitialWorkspaceState()
      const s1 = reduce(
        s0,
        { type: 'sessions/snapshot', sessions: [sess('local'), sess('server')], generation: 1, now: 0, authoritative: true },
        { type: 'view/setPaneTree', tree: localTree },
      )
      expect(s1.view.paneTreeRev).toBe(1)
      expect(s1.view.paneTreeRevSynced).toBe(0)
      // Now treeSaved unblocks by setting revSynced = 1
      const s1a = workspaceReducer(s1, { type: 'groups/treeSaved', id: s1.view.activeGroupId, rev: 1 })
      expect(s1a.view.paneTreeRevSynced).toBe(1)
      const activeId = s1a.view.activeGroupId
      // Now snapshot can adopt
      const s2 = workspaceReducer(s1a, {
        type: 'groups/snapshot',
        groups: { [activeId]: { tree: serverTree } },
      })
      expect(s2.view.paneTree).toEqual(serverTree) // adopted after revSynced = rev
    })

    it('clears paneTree and activeGroupId when active group is absent from snapshot', () => {
      const s0 = createInitialWorkspaceState()
      const activeId = s0.view.activeGroupId
      const s1 = reduce(
        s0,
        { type: 'sessions/snapshot', sessions: [sess('a')], generation: 1, now: 0, authoritative: true },
        { type: 'view/setPaneTree', tree: { type: 'leaf', sessionKey: 'a' } as PaneTree },
      )
      expect(s1.view.paneTree).not.toBeNull()
      expect(s1.view.activeGroupId).toBe(activeId)
      const groups: GroupRecordMap = {
        otherGroup: { tree: { type: 'leaf', sessionKey: 'b' } },
      }
      const s2 = workspaceReducer(s1, {
        type: 'groups/snapshot',
        groups,
      })
      expect(s2.view.paneTree).toBeNull()
      expect(s2.view.activeKey).toBeNull()
    })
  })

  describe('paneTreeRev tracking', () => {
    it('initializes paneTreeRev to 0', () => {
      const s0 = createInitialWorkspaceState()
      expect(s0.view.paneTreeRev).toBe(0)
    })

    it('increments paneTreeRev on user-edit actions (split, close, move, swap, removeFromLayout, setPaneTree)', () => {
      const s0 = createInitialWorkspaceState()
      const s1 = reduce(
        s0,
        { type: 'sessions/snapshot', sessions: [sess('a'), sess('b'), sess('c')], generation: 1, now: 0, authoritative: true },
      )
      expect(s1.view.paneTreeRev).toBe(0)

      // split: bumps rev
      const s2 = workspaceReducer(s1, { type: 'view/split', targetKey: 'a', direction: 'h', newKey: 'b' })
      expect(s2.view.paneTreeRev).toBe(1)

      // close: bumps rev
      const s3 = workspaceReducer(s2, { type: 'view/close', sessionKey: 'b' })
      expect(s3.view.paneTreeRev).toBe(2)

      // move: bumps rev
      const s4 = reduce(s3, { type: 'view/split', targetKey: 'a', direction: 'h', newKey: 'c' })
      expect(s4.view.paneTreeRev).toBe(3)
      const s5 = workspaceReducer(s4, { type: 'view/move', sourceKey: 'c', targetKey: 'a', edge: 'right' })
      expect(s5.view.paneTreeRev).toBe(4)

      // setPaneTree: bumps rev
      const s6 = workspaceReducer(s5, { type: 'view/setPaneTree', tree: { type: 'leaf', sessionKey: 'a' } as PaneTree })
      expect(s6.view.paneTreeRev).toBe(5)
    })

    it('does NOT increment paneTreeRev on setActiveGroup', () => {
      const s0 = createInitialWorkspaceState()
      const s1 = reduce(
        s0,
        { type: 'sessions/snapshot', sessions: [sess('a'), sess('b')], generation: 1, now: 0, authoritative: true },
        { type: 'view/split', targetKey: 'a', direction: 'h', newKey: 'b' },
      )
      const rev = s1.view.paneTreeRev
      const s2 = workspaceReducer(s1, {
        type: 'view/setActiveGroup',
        groupId: 'newGroup',
        tree: { type: 'leaf', sessionKey: 'a' } as PaneTree,
      })
      expect(s2.view.paneTreeRev).toBe(rev) // unchanged
    })

    it('does NOT increment paneTreeRev when groups/snapshot adopts a tree', () => {
      const s0 = createInitialWorkspaceState()
      const s1 = reduce(
        s0,
        { type: 'sessions/snapshot', sessions: [sess('a')], generation: 1, now: 0, authoritative: true },
        { type: 'view/setPaneTree', tree: { type: 'leaf', sessionKey: 'a' } as PaneTree },
      )
      const rev = s1.view.paneTreeRev
      const activeId = s1.view.activeGroupId
      const groups: GroupRecordMap = {
        [activeId]: { tree: { type: 'leaf', sessionKey: 'a' } },
      }
      const s2 = workspaceReducer(s1, {
        type: 'groups/snapshot',
        groups,
      })
      expect(s2.view.paneTreeRev).toBe(rev) // unchanged
    })

    it('treeSaved uses Math.max for monotonic revSynced even with out-of-order completion', () => {
      const s0 = createInitialWorkspaceState()
      const s1 = reduce(
        s0,
        { type: 'sessions/snapshot', sessions: [sess('a'), sess('b')], generation: 1, now: 0, authoritative: true },
        { type: 'view/setPaneTree', tree: { type: 'leaf', sessionKey: 'a' } as PaneTree },
      )
      expect(s1.view.paneTreeRev).toBe(1)
      expect(s1.view.paneTreeRevSynced).toBe(0)
      const activeId = s1.view.activeGroupId

      // treeSaved with rev=1 completes first
      const s2 = workspaceReducer(s1, { type: 'groups/treeSaved', id: activeId, rev: 1 })
      expect(s2.view.paneTreeRevSynced).toBe(1)

      // User makes another edit (bumps rev to 2)
      const s3 = reduce(s2, { type: 'view/setPaneTree', tree: { type: 'leaf', sessionKey: 'b' } as PaneTree })
      expect(s3.view.paneTreeRev).toBe(2)
      expect(s3.view.paneTreeRevSynced).toBe(1)

      // treeSaved with rev=2 completes
      const s4 = workspaceReducer(s3, { type: 'groups/treeSaved', id: activeId, rev: 2 })
      expect(s4.view.paneTreeRevSynced).toBe(2)

      // Now hypothetically rev=1 completes out-of-order (should not regress revSynced)
      const s5 = workspaceReducer(s4, { type: 'groups/treeSaved', id: activeId, rev: 1 })
      expect(s5.view.paneTreeRevSynced).toBe(2) // Math.max(2, 1) = 2, not regressed
    })
  })

  describe('legacy migration planner', () => {
    it('assigns ranks only to groups/session keys missing server ranks', () => {
      const legacy: LegacyMigrationInput = {
        groups: [
          { id: 'g1', tree: { type: 'leaf', sessionKey: 'a' }, name: 'first' },
          { id: 'g2', tree: { type: 'leaf', sessionKey: 'b' } },
        ],
        order: ['g2', 'g1'],
        sessionOrder: ['y', 'x'],
      }
      const currentGroups = {
        g2: { tree: { type: 'leaf', sessionKey: 'b' }, rank: 'a0' },
      } as const
      const currentSessionOrder = { x: 'a0' } as const
      const plan = planLegacyMigration(legacy, currentGroups, currentSessionOrder, 'g1', legacy.groups[0].tree, 'first')
      expect(plan.groupRanks.map(r => r.id)).toEqual(['g1'])
      expect(plan.sessionRanks.map(r => r.key)).toEqual(['y'])
      expect(plan.order).toEqual(['g2', 'g1'])
    })
  })

  describe('monotonic transport generation', () => {
    it('applies events with newer generation and ignores stale responses', () => {
      const s0 = createInitialWorkspaceState()
      const s1 = workspaceReducer(s0, snapshot([sess('a')], 1, 0))
      expect(s1.transportGeneration).toBe(1)
      const s2 = workspaceReducer(s1, snapshot([sess('b')], 3, 0, true))
      expect(s2.transportGeneration).toBe(3)
      const s3 = workspaceReducer(s2, snapshot([sess('c')], 2, 0, true))
      expect(s3.transportGeneration).toBe(3)
      expect(s3.sessions.map(sessionKey)).toEqual(['b'])
    })
  })
})

function sessionKeyFromLeaves(state: WorkspaceState): string[] {
  const tree = state.view.paneTree
  if (!tree) return []
  const leaves: string[] = []
  walk(tree)
  return leaves

  function walk(node: { type: 'leaf'; sessionKey: string } | { type: 'split'; first: typeof node; second: typeof node }) {
    if (node.type === 'leaf') leaves.push(node.sessionKey)
    else {
      walk(node.first)
      walk(node.second)
    }
  }
}
