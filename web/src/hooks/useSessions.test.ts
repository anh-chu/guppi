// @vitest-environment jsdom
import { renderHook, act, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useSessions, type Session } from './useSessions'

function makeSession(name: string): Session {
  return {
    id: name,
    name,
    host: undefined,
    windows: [],
    created: new Date().toISOString(),
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

  it('fetches authoritative snapshot on WS reconnect', async () => {
    const dispatch = vi.fn()
    vi.mocked(fetch).mockResolvedValueOnce({ ok: true, json: () => Promise.resolve([makeSession('a')]) } as Response)
    const { rerender } = renderHook(
      ({ live }: { live: boolean }) =>
        useSessions(dispatch, { live, livenessUnknown: true }, true),
      { initialProps: { live: false } },
    )
    // No fetch while disconnected
    expect(fetch).not.toHaveBeenCalled()
    await act(async () => rerender({ live: true }))
    // Reconnect triggers a fetch with authoritative: true
    await waitFor(() =>
      expect(dispatch).toHaveBeenCalledWith(
        expect.objectContaining({ type: 'sessions/snapshot', generation: 1, authoritative: true }),
      ),
    )
  })

  it('does not poll after reconnect', async () => {
    const dispatch = vi.fn()
    vi.mocked(fetch).mockImplementation(() => response([makeSession('a')]))
    const { rerender } = renderHook(
      ({ live }: { live: boolean }) =>
        useSessions(dispatch, { live, livenessUnknown: false }, true),
      { initialProps: { live: false } },
    )
    expect(fetch).not.toHaveBeenCalled()
    // Reconnect triggers a fetch
    await act(async () => rerender({ live: true }))
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))
    // Advance time 10 seconds - no polling should occur
    await act(async () => vi.advanceTimersByTimeAsync(10000))
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('explicit refresh calls are independent', async () => {
    const dispatch = vi.fn()
    vi.mocked(fetch).mockImplementation(() => response([makeSession('a')]))
    const { result } = renderHook(() => useSessions(dispatch, { live: true, livenessUnknown: false }, true))
    await act(async () => result.current.refresh())
    expect(fetch).toHaveBeenCalledTimes(1)
    await act(async () => result.current.refresh())
    expect(fetch).toHaveBeenCalledTimes(2)
    // Both fetches complete and dispatch
    await waitFor(() => expect(dispatch).toHaveBeenCalledTimes(2))
  })
})
