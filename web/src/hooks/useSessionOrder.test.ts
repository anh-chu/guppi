// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useSessionOrder } from './useSessionOrder'
import type { SessionView } from '../state/session/viewModel'

const STORAGE_KEY = 'termyard:session-order-v1'

function createSession(key: string, createdAt?: string, state = 'idle'): SessionView {
  const parts = key.split('/')
  const isLocal = !key.includes('/')
  return {
    key,
    id: parts[parts.length - 1],
    ref: { session: parts[parts.length - 1], owner: parts[0] || null },
    label: `Session ${key}`,
    displayName: `Session ${key}`,
    isLocal,
    ownerId: parts[0] || '',
    host: isLocal ? undefined : { peer_id: parts[0], owner_id: parts[0], name: `Host ${parts[0]}` },
    hostOnline: true,
    agentType: undefined,
    createdAt: createdAt || new Date().toISOString(),
    cwd: undefined,
    worktreeBranch: undefined,
    scheduleId: undefined,
  } as SessionView
}

describe('useSessionOrder', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  afterEach(() => {
    localStorage.clear()
  })

  it('recovers from invalid storage', () => {
    localStorage.setItem(STORAGE_KEY, 'invalid json')
    const sessions = [createSession('local/a'), createSession('local/b')]
    const { result } = renderHook(() =>
      useSessionOrder(sessions, true, () => [], () => false),
    )
    expect(result.current.order.orderedKeys).toEqual([])
    expect(result.current.ordered.length).toBe(2)
  })

  it('discards non-string values', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(['local/a', 123, 'local/b', null, 'local/b']))
    const sessions = [createSession('local/a'), createSession('local/b')]
    const { result } = renderHook(() =>
      useSessionOrder(sessions, true, () => [], () => false),
    )
    expect(result.current.order.orderedKeys).toEqual(['local/a', 'local/b'])
  })

  it('removes duplicate keys from storage', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(['local/a', 'local/b', 'local/a']))
    const sessions = [createSession('local/a'), createSession('local/b')]
    const { result } = renderHook(() =>
      useSessionOrder(sessions, true, () => [], () => false),
    )
    expect(result.current.order.orderedKeys).toEqual(['local/a', 'local/b'])
  })

  it('appends new sessions in default sort order (state→newest→key)', () => {
    const now = new Date().toISOString()
    const earlier = new Date(Date.now() - 1000000).toISOString()
    const sessions = [
      createSession('local/a', now, 'idle'),
      createSession('local/b', earlier, 'idle'),
      createSession('local/c', now, 'idle'),
    ]
    const { result } = renderHook(() =>
      useSessionOrder(sessions, true, () => [], () => false),
    )
    // Default sort: by createdAt descending (newest first), then key
    // So: local/a (now), local/c (now but later key), local/b (earlier)
    expect(result.current.ordered.map(s => s.key)).toEqual([
      'local/a',
      'local/c',
      'local/b',
    ])
  })

  it('prunes missing sessions only after bootstrap', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(['local/a', 'local/missing']))
    const sessions = [createSession('local/a')]

    // While not bootstrapped, keep the stored order as-is
    const { rerender, result: result1 } = renderHook(
      ({ bootstrapped }) =>
        useSessionOrder(sessions, bootstrapped, () => [], () => false),
      { initialProps: { bootstrapped: false } },
    )
    expect(result1.current.order.orderedKeys).toEqual(['local/a', 'local/missing'])

    // After bootstrap, prune missing keys
    rerender({ bootstrapped: true })
    expect(result1.current.order.orderedKeys).toEqual(['local/a'])
  })

  it('applies stored order to known sessions', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(['local/b', 'local/a']))
    const sessions = [createSession('local/a'), createSession('local/b')]
    const { result } = renderHook(() =>
      useSessionOrder(sessions, true, () => [], () => false),
    )
    expect(result.current.ordered.map(s => s.key)).toEqual(['local/b', 'local/a'])
  })

  it('moveToTop moves a session to the start', () => {
    const sessions = [createSession('local/a'), createSession('local/b'), createSession('local/c')]
    const { result } = renderHook(() =>
      useSessionOrder(sessions, true, () => [], () => false),
    )

    act(() => {
      result.current.order.moveToTop('local/b')
    })

    // orderedKeys only includes explicitly ordered sessions
    expect(result.current.order.orderedKeys).toEqual(['local/b'])
    // But the full "ordered" result has all sessions with explicit ones first
    expect(result.current.ordered.map(s => s.key)).toEqual(['local/b', 'local/a', 'local/c'])
  })

  it('moveUp swaps with the previous session', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(['local/a', 'local/b', 'local/c']))
    const sessions = [createSession('local/a'), createSession('local/b'), createSession('local/c')]
    const { result } = renderHook(() =>
      useSessionOrder(sessions, true, () => [], () => false),
    )

    act(() => {
      result.current.order.moveUp('local/c')
    })

    expect(result.current.order.orderedKeys).toEqual(['local/a', 'local/c', 'local/b'])
    expect(result.current.ordered.map(s => s.key)).toEqual(['local/a', 'local/c', 'local/b'])
  })

  it('moveDown swaps with the next session', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(['local/a', 'local/b', 'local/c']))
    const sessions = [createSession('local/a'), createSession('local/b'), createSession('local/c')]
    const { result } = renderHook(() =>
      useSessionOrder(sessions, true, () => [], () => false),
    )

    act(() => {
      result.current.order.moveDown('local/a')
    })

    expect(result.current.order.orderedKeys).toEqual(['local/b', 'local/a', 'local/c'])
  })

  it('reset clears the order', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(['local/a', 'local/b']))
    const sessions = [createSession('local/a'), createSession('local/b')]
    const { result } = renderHook(() =>
      useSessionOrder(sessions, true, () => [], () => false),
    )

    act(() => {
      result.current.order.reset()
    })

    expect(result.current.order.orderedKeys).toEqual([])
    expect(localStorage.getItem(STORAGE_KEY)).toBe(JSON.stringify([]))
  })

  it('renaming does not change order (uses immutable key)', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(['local/a', 'local/b']))
    const sessions = [
      { ...createSession('local/a'), label: 'Old Name' },
      { ...createSession('local/b'), label: 'Another' },
    ]
    const { result: result1 } = renderHook(() =>
      useSessionOrder(sessions, true, () => [], () => false),
    )

    // Rename local/a in the next render
    const renamedSessions = [
      { ...sessions[0], label: 'New Name' },
      sessions[1],
    ]
    const { result: result2 } = renderHook(() =>
      useSessionOrder(renamedSessions, true, () => [], () => false),
    )

    // Order should be unchanged because key is the same
    expect(result2.current.order.orderedKeys).toEqual(['local/a', 'local/b'])
    expect(result2.current.ordered[0].label).toBe('New Name')
  })

  it('does not collide remote and local keys', () => {
    // Remote key: "peer-id/session-id"
    // Local key: just "session-id" (no owner)
    const sessions = [
      createSession('local/s1'), // local
      createSession('remote-peer/s1'), // remote with same session id
    ]
    localStorage.setItem(STORAGE_KEY, JSON.stringify(['remote-peer/s1', 'local/s1']))
    const { result } = renderHook(() =>
      useSessionOrder(sessions, true, () => [], () => false),
    )

    // Should preserve the exact order, keys are distinct
    expect(result.current.ordered.map(s => s.key)).toEqual(['remote-peer/s1', 'local/s1'])
  })

  it('persists order to localStorage on mutation', () => {
    const sessions = [createSession('local/a'), createSession('local/b'), createSession('local/c')]
    const { result } = renderHook(() =>
      useSessionOrder(sessions, true, () => [], () => false),
    )

    act(() => {
      result.current.order.moveToTop('local/c')
    })

    const stored = JSON.parse(localStorage.getItem(STORAGE_KEY) || '[]')
    expect(stored).toEqual(['local/c'])
  })

  it('handles move operations gracefully when not in stored order', () => {
    const sessions = [createSession('local/a'), createSession('local/b')]
    const { result } = renderHook(() =>
      useSessionOrder(sessions, true, () => [], () => false),
    )

    // moveUp on a key not in the stored order should be a no-op
    act(() => {
      result.current.order.moveUp('local/a')
    })
    expect(result.current.order.orderedKeys).toEqual([])

    // moveDown on a key not in the stored order should be a no-op
    act(() => {
      result.current.order.moveDown('local/b')
    })
    expect(result.current.order.orderedKeys).toEqual([])
  })

  it('appends new sessions without altering existing order', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(['local/a', 'local/b']))
    const sessions = [
      createSession('local/a'),
      createSession('local/c'), // new
      createSession('local/b'),
    ]
    const { result } = renderHook(() =>
      useSessionOrder(sessions, true, () => [], () => false),
    )

    expect(result.current.ordered.map(s => s.key)).toEqual(['local/a', 'local/b', 'local/c'])
  })
})
