// @vitest-environment jsdom
import { renderHook, act, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useGroupSync } from './useGroupSync'

function okResponse(body: unknown): Response {
  return { ok: true, status: 200, json: () => Promise.resolve(body), text: () => Promise.resolve('') } as Response
}

function errorResponse(status: number, message: string): Response {
  return { ok: false, status, json: () => Promise.resolve({}), text: () => Promise.resolve(message) } as Response
}

describe('useGroupSync', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn())
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('setName sends manual mode for a non-empty name', async () => {
    vi.mocked(fetch).mockResolvedValue(okResponse({}))
    const dispatch = vi.fn()
    const { result } = renderHook(() => useGroupSync(true, dispatch))
    await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/groups', expect.any(Object)))
    vi.mocked(fetch).mockClear()

    await act(async () => result.current.setName('g1', 'My Group'))

    expect(fetch).toHaveBeenCalledTimes(1)
    expect(fetch).toHaveBeenCalledWith(
      '/api/groups',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ id: 'g1', op: 'name', name: 'My Group', mode: 'manual' }),
      }),
    )
  })

  it('setName sends auto mode when clearing the name', async () => {
    vi.mocked(fetch).mockResolvedValue(okResponse({}))
    const dispatch = vi.fn()
    const { result } = renderHook(() => useGroupSync(true, dispatch))
    await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/groups', expect.any(Object)))
    vi.mocked(fetch).mockClear()

    await act(async () => result.current.setName('g1', ''))

    expect(fetch).toHaveBeenCalledTimes(1)
    expect(fetch).toHaveBeenCalledWith(
      '/api/groups',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ id: 'g1', op: 'name', name: '', mode: 'auto' }),
      }),
    )
  })

  it('forceAiName POSTs the minimal id-based op and dispatches the returned snapshot', async () => {
    const snapshot = { g1: { tree: { type: 'leaf', sessionKey: 'a' }, name: 'Generated' } }
    vi.mocked(fetch).mockResolvedValue(okResponse(snapshot))
    const dispatch = vi.fn()
    const { result } = renderHook(() => useGroupSync(true, dispatch))
    await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/groups', expect.any(Object)))
    vi.mocked(fetch).mockClear()

    let returned: boolean | undefined
    await act(async () => {
      returned = await result.current.forceAiName('g1')
    })

    expect(returned).toBe(true)
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(fetch).toHaveBeenCalledWith(
      '/api/groups',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ id: 'g1', op: 'ai-name' }),
      }),
    )
    expect(dispatch).toHaveBeenCalledWith(expect.objectContaining({ type: 'groups/snapshot', groups: snapshot }))
  })

  it('forceAiName returns false and does not dispatch for an unsupported server op', async () => {
    vi.mocked(fetch).mockResolvedValue(errorResponse(400, 'invalid op'))
    const dispatch = vi.fn()
    const { result } = renderHook(() => useGroupSync(true, dispatch))
    await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/groups', expect.any(Object)))
    vi.mocked(fetch).mockClear()
    dispatch.mockClear()

    let returned: boolean | undefined
    await act(async () => {
      returned = await result.current.forceAiName('g1')
    })

    expect(returned).toBe(false)
    expect(dispatch).not.toHaveBeenCalled()
  })

  it('forceAiName sets namingGroupId optimistically while the request is in flight', async () => {
    let resolveFetch!: (value: Response) => void
    const fetchPromise = new Promise<Response>((resolve) => { resolveFetch = resolve })
    vi.mocked(fetch).mockReturnValue(fetchPromise)
    const dispatch = vi.fn()
    const { result } = renderHook(() => useGroupSync(true, dispatch))
    await waitFor(() => expect(fetch).toHaveBeenCalled())

    act(() => {
      result.current.forceAiName('g1')
    })
    expect(result.current.namingGroupId).toBe('g1')

    await act(async () => resolveFetch(okResponse({ g1: { tree: { type: 'leaf', sessionKey: 'a' } } })))
    expect(result.current.namingGroupId).toBeNull()
  })

  it('setTree skips POST when tree has <2 leaves (one-leaf trees are tombstoned server-side)', async () => {
    vi.mocked(fetch).mockResolvedValue(okResponse({}))
    const dispatch = vi.fn()
    const { result } = renderHook(() => useGroupSync(true, dispatch))
    await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/groups', expect.any(Object)))
    vi.mocked(fetch).mockClear()
    dispatch.mockClear()

    await act(async () => {
      result.current.setTree('g1', { type: 'leaf', sessionKey: 'a' }, 1)
    })

    // Should not call fetch for the tree POST
    expect(fetch).not.toHaveBeenCalledWith(
      '/api/groups',
      expect.objectContaining({ body: expect.stringContaining('op') }),
    )
    // Should not dispatch treeSaved
    expect(dispatch).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: 'groups/treeSaved' }),
    )
  })

  it('setTree dispatches treeSaved only on successful POST with 2+ leaves', async () => {
    vi.mocked(fetch).mockResolvedValue(okResponse({ g1: { tree: { type: 'leaf', sessionKey: 'a' } } }))
    const dispatch = vi.fn()
    const { result } = renderHook(() => useGroupSync(true, dispatch))
    await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/groups', expect.any(Object)))
    vi.mocked(fetch).mockClear()
    dispatch.mockClear()

    await act(async () => {
      result.current.setTree('g1', {
        type: 'split',
        direction: 'h',
        ratio: 0.5,
        first: { type: 'leaf', sessionKey: 'a' },
        second: { type: 'leaf', sessionKey: 'b' },
      }, 1)
    })

    // Should POST the tree
    expect(fetch).toHaveBeenCalledWith(
      '/api/groups',
      expect.objectContaining({ method: 'POST', body: expect.stringContaining('"op":"tree"') }),
    )
    // Should dispatch treeSaved with rev=1
    expect(dispatch).toHaveBeenCalledWith(
      expect.objectContaining({ type: 'groups/treeSaved', id: 'g1', rev: 1 }),
    )
  })

  it('setTree calls refresh on fetch failure and does not dispatch treeSaved', async () => {
    let refreshCount = 0
    vi.mocked(fetch).mockImplementation(() => {
      // First call is initial refresh, second is POST attempt, third is refresh on failure
      refreshCount++
      if (refreshCount === 2) {
        return Promise.resolve(errorResponse(500, 'Internal error'))
      }
      return Promise.resolve(okResponse({}))
    })
    const dispatch = vi.fn()
    const { result } = renderHook(() => useGroupSync(true, dispatch))
    await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/groups', expect.any(Object)))
    vi.mocked(fetch).mockClear()
    dispatch.mockClear()

    await act(async () => {
      result.current.setTree('g1', {
        type: 'split',
        direction: 'h',
        ratio: 0.5,
        first: { type: 'leaf', sessionKey: 'a' },
        second: { type: 'leaf', sessionKey: 'b' },
      }, 1)
      // Wait for the promise chain to complete
      await new Promise(resolve => setTimeout(resolve, 10))
    })
    // Wait for any async operations to settle
    await new Promise(resolve => setTimeout(resolve, 10))

    // Should have called fetch 3 times (initial refresh, POST attempt, refresh on failure)
    expect(fetch).toHaveBeenCalled()
    // Should not dispatch treeSaved
    expect(dispatch).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: 'groups/treeSaved' }),
    )
  })
})
