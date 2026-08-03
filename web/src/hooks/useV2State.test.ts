// @vitest-environment jsdom
import { renderHook, act, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useV2State } from './useV2State'

class FakeSocket {
  static instances: FakeSocket[] = []
  onopen: (() => void) | null = null
  onmessage: ((evt: MessageEvent) => void) | null = null
  onclose: (() => void) | null = null
  onerror: ((err: unknown) => void) | null = null
  readyState = 0
  constructor(public url: string) {
    FakeSocket.instances.push(this)
  }
  close() {
    this.readyState = 3
    this.onclose?.()
  }
  send() {}
}

function okResponse(body: unknown): Response {
  return { ok: true, status: 200, json: () => Promise.resolve(body), text: () => Promise.resolve('') } as Response
}

describe('useV2State', () => {
  beforeEach(() => {
    FakeSocket.instances = []
    vi.stubGlobal('WebSocket', FakeSocket as unknown as typeof WebSocket)
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        okResponse({
          owner: 'me',
          revision: 1,
          sessions: [],
          layouts: [],
          hosts: [],
          pending: [],
        }),
      ),
    )
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('does not mutate the normalized catalog/workspace when creating a session', async () => {
    const { result } = renderHook(() => useV2State({ enabled: true }))
    await waitFor(() => expect(FakeSocket.instances.length).toBe(1))

    const catalogBefore = result.current.state.catalog
    const workspaceBefore = result.current.state.workspace

    // sendSessionCommand posts a command over the wire; it must never touch
    // the store's catalog/workspace projections directly. Those are only
    // ever replaced by an incoming catalog_snapshot/workspace_snapshot
    // message, which existing sessions' identities are keyed off of. A
    // create command that doesn't touch the projection therefore cannot
    // cause any existing session's terminal to remount.
    vi.mocked(fetch).mockResolvedValueOnce(okResponse({ Ref: { owner: 'me', session: 'new-1' }, Accepted: true }))
    await act(async () => {
      await result.current.createSession({ name: 'new-1' })
    })

    expect(result.current.state.catalog).toBe(catalogBefore)
    expect(result.current.state.workspace).toBe(workspaceBefore)
  })

  it('replaces the catalog only when a catalog_snapshot arrives on the stream, preserving unrelated session identities', async () => {
    const { result } = renderHook(() => useV2State({ enabled: true }))
    await waitFor(() => expect(FakeSocket.instances.length).toBe(1))
    const socket = FakeSocket.instances[0]

    const existingSession = {
      ref: { owner: 'me', session: 'existing', window: 0, pane: 0 },
      owner: 'me',
      phase: 'running',
    }

    act(() => {
      socket.onmessage?.({
        data: JSON.stringify({
          type: 'catalog_snapshot',
          snapshot: { owner: 'me', revision: 2, sessions: [existingSession], layouts: [] },
        }),
      } as MessageEvent)
    })

    await waitFor(() => expect(result.current.state.catalog.sessionsByRef.size).toBe(1))
    const sessionBefore = result.current.state.catalog.sessionsByRef.get('me/existing/0/0')

    // Creating a new session (command call only, no snapshot) must not
    // change the reference identity of the already-known session.
    vi.mocked(fetch).mockResolvedValueOnce(okResponse({ Ref: { owner: 'me', session: 'new-1' }, Accepted: true }))
    await act(async () => {
      await result.current.createSession({ name: 'new-1' })
    })

    const sessionAfter = result.current.state.catalog.sessionsByRef.get('me/existing/0/0')
    expect(sessionAfter).toBe(sessionBefore)
  })

  it('does not mutate the normalized workspace/catalog when moving a pane (layout mutation)', async () => {
    const { result } = renderHook(() => useV2State({ enabled: true }))
    await waitFor(() => expect(FakeSocket.instances.length).toBe(1))
    const socket = FakeSocket.instances[0]

    act(() => {
      socket.onmessage?.({
        data: JSON.stringify({
          type: 'workspace_snapshot',
          workspace: {
            id: 'L1',
            owner: 'me',
            revision: 3,
            tree: {
              type: 'split',
              id: 'sp1',
              direction: 'h',
              ratio: 0.5,
              first: { type: 'leaf', ref: { owner: 'me', session: 'a', window: 0, pane: 0 } },
              second: { type: 'leaf', ref: { owner: 'me', session: 'b', window: 0, pane: 0 } },
            },
          },
        }),
      } as MessageEvent)
    })

    await waitFor(() => expect(result.current.paneTree).not.toBeNull())
    const catalogBefore = result.current.state.catalog
    const workspaceBefore = result.current.state.workspace
    const paneTreeBefore = result.current.paneTree

    // A layout mutation command (move) is a fire-and-forget POST; it must
    // never mutate the store directly -- only an incoming workspace_snapshot
    // does. Terminals are keyed by session key (not by paneTree/workspace
    // object identity), so as long as the command call itself never touches
    // the store, no live pane can be spuriously remounted by the act of
    // issuing the command.
    vi.mocked(fetch).mockResolvedValueOnce(okResponse({ id: 'cmd-1', layout: 'L1', accepted: true }))
    await act(async () => {
      await result.current.workspaceCommand('L1', {
        action: 'move',
        source: { owner: 'me', session: 'a', window: 0, pane: 0 },
        target: { owner: 'me', session: 'b', window: 0, pane: 0 },
        edge: 'right',
      })
    })

    expect(result.current.state.catalog).toBe(catalogBefore)
    expect(result.current.state.workspace).toBe(workspaceBefore)
    expect(result.current.paneTree).toBe(paneTreeBefore)
  })
})
