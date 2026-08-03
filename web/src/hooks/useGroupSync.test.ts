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
})
