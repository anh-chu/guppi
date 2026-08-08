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
