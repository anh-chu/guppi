// @vitest-environment jsdom
import { renderHook, act, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useSessions, type Session } from './useSessions'
import { sessionKey, optimisticSession } from './useSessions'

function makeSession(name: string): Session {
  return {
    id: name,
    name,
    host: undefined,
    windows: [],
    created: new Date().toISOString(),
    attached: false,
    last_activity: new Date().toISOString(),
  }
}

const response = (sessions: Session[]) =>
  Promise.resolve({ ok: true, json: () => Promise.resolve(sessions) } as Response)

describe('useSessions transport', () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    vi.stubGlobal('fetch', vi.fn())
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('fetches on mount and dispatches a snapshot', async () => {
    const dispatch = vi.fn()
    vi.mocked(fetch).mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([makeSession('a')]) } as Response)
    renderHook(() => useSessions(dispatch, { live: false, livenessUnknown: true }, true))
    await waitFor(() =>
      expect(dispatch).toHaveBeenCalledWith(
        expect.objectContaining({ type: 'sessions/snapshot', generation: 1 }),
      ),
    )
  })

  it('polls while liveness is unknown and pauses when live', async () => {
    const dispatch = vi.fn()
    vi.mocked(fetch).mockImplementation(() => response([makeSession('a')]))
    const { rerender } = renderHook(
      ({ live }: { live: boolean }) =>
        useSessions(dispatch, { live, livenessUnknown: !live }, true),
      { initialProps: { live: false } },
    )
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))
    await act(async () => vi.advanceTimersByTimeAsync(5000))
    expect(fetch).toHaveBeenCalledTimes(2)
    await act(async () => rerender({ live: true }))
    // Live connect triggers a refresh, then polling stops.
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(3))
    await act(async () => vi.advanceTimersByTimeAsync(20000))
    expect(fetch).toHaveBeenCalledTimes(3)
  })

  it('aborts an in-flight fetch when a newer refresh starts', async () => {
    const dispatch = vi.fn()
    vi.mocked(fetch).mockImplementation(() => response([makeSession('a')]))
    const { result } = renderHook(() => useSessions(dispatch, { live: false, livenessUnknown: true }, true))
    expect(fetch).toHaveBeenCalledTimes(1)
    await act(async () => result.current.refresh())
    expect(fetch).toHaveBeenCalledTimes(2)
    // The first request was aborted; only one snapshot is ultimately applied.
    await waitFor(() => expect(dispatch).toHaveBeenCalledTimes(1))
  })
})

describe('optimistic session identity', () => {
  // Regression coverage for the drag-to-split bug: the caller (App.tsx)
  // must build the same key it hands to optimisticSession() as the one it
  // uses for the pane-tree leaf and pending-session guard. If the caller
  // passes a local-host fallback id into optimisticSession() while keeping
  // the pane-tree key unqualified, sessionKey(optimistic) diverges from the
  // pane's key and the split never reconciles with the live session list.

  it('keeps a local session key unqualified, matching the caller-derived key', () => {
    const name = 'shell'
    const hostId = undefined
    const optimisticKey = hostId ? `${hostId}/${name}` : name
    const session = optimisticSession(name, hostId, 'My Laptop', '/tmp')
    expect(sessionKey(session)).toBe(optimisticKey)
    expect(sessionKey(session)).toBe('shell')
  })

  it('keeps a remote session key host-qualified, matching the caller-derived key', () => {
    const name = 'shell'
    const hostId = 'peer-abc123'
    const optimisticKey = hostId ? `${hostId}/${name}` : name
    const session = optimisticSession(name, hostId, 'Remote Box', '/tmp')
    expect(sessionKey(session)).toBe(optimisticKey)
    expect(sessionKey(session)).toBe('peer-abc123/shell')
  })

  it('quick-shell local sessions are always unqualified regardless of a local host id', () => {
    // handleQuickShell always creates a local daemon session and must never
    // qualify the key/host with the local host's own id.
    const name = 'shell-123'
    const sk = name
    const session = optimisticSession(name, undefined, 'My Laptop')
    expect(sessionKey(session)).toBe(sk)
    expect(session.host).toBeUndefined()
  })

  it('defaults to daemon backend for local sessions', () => {
    const session = optimisticSession('shell', undefined, 'My Laptop', '/tmp')
    expect(session.backend).toBe('daemon')
  })

  it('uses tmux backend when explicitly passed for a remote session', () => {
    // Regression coverage: a remote drag-to-split create must produce an
    // optimistic session whose backend matches the server's remote/tmux
    // path, so the optimistic terminal connects via the same WebSocket
    // route (/ws/session) that the reconciled session will use, instead of
    // the daemon route (/ws/daemon-session).
    const hostId = 'peer-abc123'
    const optimisticBackend = hostId ? 'tmux' : 'daemon'
    const session = optimisticSession('shell', hostId, 'Remote Box', '/tmp', optimisticBackend)
    expect(session.backend).toBe('tmux')
    expect(sessionKey(session)).toBe('peer-abc123/shell')
  })
})
