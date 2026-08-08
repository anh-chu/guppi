import { describe, it, expect } from 'vitest'
import {
  workspaceReducer,
  createInitialWorkspaceState,
  sessionKey,
  planLegacyMigration,
  type WorkspaceAction,
  type WorkspaceStateWithStreaks,
  type SessionEvent,
  type LegacyMigrationInput,
} from './workspaceReducer'
import type { Session } from '../hooks/useSessions'

function sess(name: string, host = ''): Session {
  return {
    id: name,
    name,
    host: host || undefined,
    windows: [],
    created: new Date().toISOString(),
  }
}

function reduce(state: WorkspaceStateWithStreaks, ...actions: WorkspaceAction[]): WorkspaceStateWithStreaks {
  return actions.reduce((s, a) => workspaceReducer(s, a), state)
}

function splitAction(targetKey: string, newKey: string, direction: 'h' | 'v' = 'h'): WorkspaceAction {
  return { type: 'view/split', targetKey, direction, newKey }
}

function snapshot(sessions: Session[], generation: number, now = 0): WorkspaceAction {
  return { type: 'sessions/snapshot', sessions, generation, now }
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

  it('non-empty snapshot hydrates sessions and clears liveness unknown', () => {
    const s0 = createInitialWorkspaceState()
    const sessions = [sess('alpha')]
    const s1 = workspaceReducer(s0, snapshot(sessions, 1, 0))
    expect(s1.sessions.map(sessionKey)).toEqual(['alpha'])
    expect(s1.loading).toBe(false)
    expect(s1.connection.livenessUnknown).toBe(false)
  })

  it('does not prune while disconnected even after an empty snapshot', () => {
    const s0 = createInitialWorkspaceState()
    const s1 = reduce(
      s0,
      connection(true, false),
      splitAction('alpha', 'beta'),
      snapshot([sess('alpha'), sess('beta')], 1, 0),
      connection(false, true),
      snapshot([], 2, 100),
      { type: 'view/pruneMissing', validKeys: [], now: 200 },
    )
    expect(s1.view.paneTree).not.toBeNull()
    expect(sessionKeyFromLeaves(s1)).toContain('alpha')
    expect(sessionKeyFromLeaves(s1)).toContain('beta')
  })

  it('prunes a missing leaf only after two observations at least 1s apart', () => {
    const s0 = createInitialWorkspaceState()
    const s1 = reduce(
      s0,
      connection(true, false),
      splitAction('alpha', 'beta'),
      snapshot([sess('alpha'), sess('beta')], 1, 0),
    )
    const afterFirst = workspaceReducer(s1, { type: 'view/pruneMissing', validKeys: ['alpha'], now: 1000 })
    expect(sessionKeyFromLeaves(afterFirst)).toContain('beta')
    expect(afterFirst.view.activeKey).toBe('beta')
    const afterSecond = workspaceReducer(afterFirst, { type: 'view/pruneMissing', validKeys: ['alpha'], now: 2100 })
    expect(sessionKeyFromLeaves(afterSecond)).toEqual(['alpha'])
    expect(afterSecond.view.activeKey).toBe('alpha')
  })

  it('ignores snapshot older than a newer event', () => {
    const s0 = createInitialWorkspaceState()
    const s1 = reduce(
      s0,
      snapshot([sess('alpha')], 1, 0),
      { type: 'sessions/event', event: { type: 'session-removed', session: 'alpha' }, generation: 2 },
    )
    expect(s1.sessions).toHaveLength(0)
    const stale = workspaceReducer(s1, snapshot([sess('alpha')], 1, 0))
    expect(stale.sessions).toHaveLength(0)
    expect(stale.transportGeneration).toBe(2)
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
          view: { paneTree: tree, activeKey: 'x', activeGroupId: 'g', singleView: null, currentView: 'session', settingsOpen: false },
          wiki: { target: { path: '/f', nonce: 5 }, history: [] },
        },
      })
      expect(restored.view.paneTree).toEqual(tree)
      expect(restored.view.activeGroupId).toBe('g')
      expect(restored.wiki.target).toEqual({ path: '/f', nonce: 5 })
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
      const s2 = workspaceReducer(s1, snapshot([sess('b')], 3, 0))
      expect(s2.transportGeneration).toBe(3)
      const s3 = workspaceReducer(s2, snapshot([sess('c')], 2, 0))
      expect(s3.transportGeneration).toBe(3)
      expect(s3.sessions.map(sessionKey)).toEqual(['b'])
    })
  })
})

function sessionKeyFromLeaves(state: WorkspaceStateWithStreaks): string[] {
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
