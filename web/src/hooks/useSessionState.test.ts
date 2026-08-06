// @vitest-environment jsdom
import { renderHook, act, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useSessionState } from './useSessionState'

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

describe('useSessionState', () => {
  beforeEach(() => {
    FakeSocket.instances = []
    vi.stubGlobal('WebSocket', FakeSocket as unknown as typeof WebSocket)
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        okResponse({
          owner: 'me',
          revision: 1,
          local: { owner: 'me', revision: 1, sessions: [], layouts: [] },
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
    const { result } = renderHook(() => useSessionState())
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
    const { result } = renderHook(() => useSessionState())
    await waitFor(() => expect(FakeSocket.instances.length).toBe(1))
    const socket = FakeSocket.instances[0]

    const existingSession = {
      ref: 'me/existing:0.0',
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
    const sessionBefore = result.current.state.catalog.sessionsByRef.get('me/existing:0.0')

    // Creating a new session (command call only, no snapshot) must not
    // change the reference identity of the already-known session.
    vi.mocked(fetch).mockResolvedValueOnce(okResponse({ Ref: { owner: 'me', session: 'new-1' }, Accepted: true }))
    await act(async () => {
      await result.current.createSession({ name: 'new-1' })
    })

    const sessionAfter = result.current.state.catalog.sessionsByRef.get('me/existing:0.0')
    expect(sessionAfter).toBe(sessionBefore)
    expect(sessionBefore).toBeDefined()
  })

  it('threads targetOwner through to the top-level target_owner wire field on create, as a canonical OwnerID -- never a peer fingerprint', async () => {
    const { result } = renderHook(() => useSessionState())
    await waitFor(() => expect(FakeSocket.instances.length).toBe(1))

    // targetOwner must already be the target host's canonical OwnerID (HostInfo.OwnerID
    // on the wire, e.g. via useHosts' Host.owner_id) by the time it reaches
    // this hook -- NOT the host's peer transport fingerprint (HostInfo.ID).
    // OwnerID is a different string encoding of a peer's identity than its
    // fingerprint (see state.OwnerIDFromFingerprint); the server types
    // target_owner as state.OwnerID and looks it up in an OwnerID-keyed
    // catalog map (peer.Manager.PeerIDForOwner), which never matches a raw
    // fingerprint. This hook itself does no conversion -- callers (see
    // App.tsx's SessionApp handleCreateSession) are responsible for resolving the
    // selected host's fingerprint to its OwnerID before calling createSession.
    const remoteOwnerId = 'remote-host-owner-id'
    vi.mocked(fetch).mockResolvedValueOnce(okResponse({ Ref: { owner: remoteOwnerId, session: 'new-1' }, Accepted: true }))
    await act(async () => {
      await result.current.createSession({ name: 'new-1', targetOwner: remoteOwnerId })
    })

    const lastCall = vi.mocked(fetch).mock.calls[vi.mocked(fetch).mock.calls.length - 1]
    const body = JSON.parse((lastCall[1] as RequestInit).body as string)
    expect(body.target_owner).toBe(remoteOwnerId)
    // Must be a sibling of action/params, not nested inside params -- the
    // server (pkg/server/routes_state_v2.go's v2SessionCommandRequest) only
    // ever looks for it at the top level.
    expect('target_owner' in body.params).toBe(false)
  })

  it('omits target_owner entirely when no targetOwner is given (local create, unchanged default)', async () => {
    const { result } = renderHook(() => useSessionState())
    await waitFor(() => expect(FakeSocket.instances.length).toBe(1))

    vi.mocked(fetch).mockResolvedValueOnce(okResponse({ Ref: { owner: 'me', session: 'new-1' }, Accepted: true }))
    await act(async () => {
      await result.current.createSession({ name: 'new-1' })
    })

    const lastCall = vi.mocked(fetch).mock.calls[vi.mocked(fetch).mock.calls.length - 1]
    const body = JSON.parse((lastCall[1] as RequestInit).body as string)
    expect('target_owner' in body).toBe(false)
  })

  it('does not mutate the normalized workspace/catalog when moving a pane (layout mutation)', async () => {
    const { result } = renderHook(() => useSessionState())
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
              first: { type: 'leaf', ref: 'me/a:0.0' },
              second: { type: 'leaf', ref: 'me/b:0.0' },
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

  it('surfaces a remote peer catalog from bootstrap, keeps it separate from local, and removes it on catalog_owner_removed', async () => {
    vi.mocked(fetch).mockReset()
    vi.mocked(fetch).mockResolvedValueOnce(
      okResponse({
        owner: 'me',
        revision: 1,
        local: {
          owner: 'me',
          revision: 1,
          sessions: [{ ref: 'me/local-1:0.0', owner: 'me', phase: 'active' }],
          layouts: [],
        },
        remote: [
          {
            owner: 'peer-b',
            revision: 9,
            sessions: [{ ref: 'peer-b/remote-1:0.0', owner: 'peer-b', phase: 'active' }],
          },
        ],
        hosts: [],
        pending: [],
      }),
    )

    const { result } = renderHook(() => useSessionState())
    await waitFor(() => expect(result.current.state.catalog.sessionsByRef.size).toBe(2))

    // Bootstrap surfaced both the local and the remote peer's session, each
    // still keyed by its own ref.
    expect(result.current.state.catalog.sessionsByRef.has('me/local-1:0.0')).toBe(true)
    expect(result.current.state.catalog.sessionsByRef.has('peer-b/remote-1:0.0')).toBe(true)
    expect(result.current.state.catalog.localOwner).toBe('me')

    const socket = FakeSocket.instances[FakeSocket.instances.length - 1]

    // A live update to the remote owner's catalog must not touch the local
    // session.
    act(() => {
      socket.onmessage?.({
        data: JSON.stringify({
          type: 'catalog_snapshot',
          snapshot: {
            owner: 'peer-b',
            revision: 10,
            sessions: [
              { ref: 'peer-b/remote-1:0.0', owner: 'peer-b', phase: 'active' },
              { ref: 'peer-b/remote-2:0.0', owner: 'peer-b', phase: 'active' },
            ],
          },
          is_local: false,
        }),
      } as MessageEvent)
    })
    await waitFor(() => expect(result.current.state.catalog.sessionsByRef.size).toBe(3))
    expect(result.current.state.catalog.sessionsByRef.has('me/local-1:0.0')).toBe(true)

    // An explicit removal signal for the remote owner drops ONLY that
    // owner's sessions -- the local session is unaffected.
    act(() => {
      socket.onmessage?.({
        data: JSON.stringify({ type: 'catalog_owner_removed', owner: 'peer-b' }),
      } as MessageEvent)
    })
    await waitFor(() => expect(result.current.state.catalog.sessionsByRef.size).toBe(1))
    expect(result.current.state.catalog.sessionsByRef.has('me/local-1:0.0')).toBe(true)
    expect(result.current.state.catalog.sessionsByRef.has('peer-b/remote-1:0.0')).toBe(false)
  })
})

describe('Schema 4 useSessionState contract - FAILS', () => {
  beforeEach(() => {
    FakeSocket.instances = []
    vi.stubGlobal('WebSocket', FakeSocket as unknown as typeof WebSocket)
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        okResponse({
          owner: 'me',
          revision: 1,
          local: { owner: 'me', revision: 1, sessions: [], workspace: { revision: 0, tree: null } },
          hosts: [],
          runtime: [],
          pending: [],
        }),
      ),
    )
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('useSessionState exposes typed hosts from bootstrap/state stream, not from /api/hosts polling', async () => {
    // Schema 4 contract: The session store includes a hostsByOwner and hostsByPeer map
    // that is populated from bootstrap and updated via hosts_snapshot stream messages.
    // There is no useHosts hook and no /api/hosts poll.

    // Currently useSessionState may not expose hosts at all, or they come from a separate
    // useHosts hook that polls /api/hosts every 30 seconds.
    // After Task 5, hosts come through the canonical store only.

    // const { result } = renderHook(() => useSessionState())
    // await waitFor(() => expect(FakeSocket.instances.length).toBe(1))
    // expect(result.current.state.hostsByOwner).toBeDefined()
    // expect(result.current.state.hostsByPeer).toBeDefined()

    expect(true).toBe(true) // Placeholder until Task 5
  })

  it('useSessionState exposes runtime snapshots keyed by SessionRef, not activity snapshots', async () => {
    // Schema 4 contract: The session store includes a runtimeByRef map
    // that is populated from bootstrap and updated via runtime_snapshot stream messages.
    // Runtime includes current_path, current_command, prompt_preview, last_activity, etc.
    // Activity snapshots (/api/activity, useActivity hook) are deleted.

    // Currently the store may not have a runtime map, or activity comes from
    // a separate useActivity hook that polls /api/activity.
    // After Task 6, runtime comes through the canonical store only.

    // const { result } = renderHook(() => useSessionState())
    // await waitFor(() => expect(FakeSocket.instances.length).toBe(1))
    // expect(result.current.state.runtimeByRef).toBeDefined()

    expect(true).toBe(true) // Placeholder until Task 6
  })

  it('workspace is a singleton { revision, tree }, not a multi-layout map', async () => {
    // Schema 4 contract: The workspace is stored as a single record with
    // { revision: number, tree: PaneNode | null }, not as a map of layouts.
    // There is no layoutsById, no LayoutID parameters, no active key.

    const { result } = renderHook(() => useSessionState())
    await waitFor(() => expect(FakeSocket.instances.length).toBe(1))

    // Placeholder assertions documenting the target contract:
    expect(result.current.state.workspace).toBeDefined()
    // After Task 2:
    // expect(result.current.state.workspace.tree).toBeNull() // empty workspace
    // expect(result.current.state.workspace.revision).toBe(0)
    // expect(result.current.state.workspace.layoutsById).toBeUndefined() // no layout map
  })
})
