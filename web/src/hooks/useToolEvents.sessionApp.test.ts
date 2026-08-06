// @vitest-environment jsdom
//
// Regression proof for round-8 Finding C: SessionApp's session keys are always
// `${ownerId}/${stableSessionId}` (SessionView.ownerId is the canonical OwnerID, never the
// peer transport fingerprint), but tool/activity events arrive keyed by
// `{host: peer-fingerprint, session: mutable-display-label}` with a SEPARATE
// stable `session_id` field the frontend previously never read (see
// pkg/toolevents.Event / pkg/ws/hub.go's WS wrapping). Before the fix, these
// two encodings never matched, so working/waiting/stuck indicators silently
// never updated for v2 sessions.
//
// Task 5 (identity canonicalization at ingestion): the hook now takes the
// HostIndex directly (useHosts' hostIndex, byPeerId keyed) rather than a raw
// Host[] the hook itself had to scan, and every event carries the canonical
// `key` set once by normalizeToolEvent -- these tests build a HostIndex the
// same way useHosts.ts does.
//
// This test drives useToolEvents' REAL implementation (not mocked, unlike
// App.test.tsx's App-level mock of this hook) through its actual public
// surface (handleEvent, the same function App.tsx wires to the WebSocket;
// sessionNeedsAttention/getSessionEvents/isSessionInActiveTurn, the same
// functions Sidebar/Overview call), and asserts the actual UI-facing
// derived status via sessionViewSignal -- the exact function App.tsx's `glance`
// summary and session badges are computed from -- not just an internal map
// having an entry.
import { renderHook, act, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useToolEvents, normalizeToolEvent } from './useToolEvents'
import type { HostSnapshot } from '../state/session/wireTypes'
import type { HostIndex, SessionView } from '../state/session/viewModel'
import { sessionViewSignal } from '../state/session/viewModel'

const emptyJsonResponse = () =>
  Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)

function makeHostIndex(hosts: HostSnapshot[]): HostIndex {
  const byPeerId = new Map<string, HostSnapshot>()
  const byOwnerId = new Map<string, HostSnapshot>()
  let local: HostSnapshot | undefined
  for (const h of hosts) {
    byPeerId.set(h.peer_id, h)
    if (h.owner_id) byOwnerId.set(h.owner_id, h)
    if (h.local) local = h
  }
  return { hosts, local, byPeerId, byOwnerId }
}

function makeView(ownerId: string, stableSessionId: string): SessionView {
  return {
    key: `${ownerId}/${stableSessionId}`,
    ref: { owner: ownerId, session: stableSessionId, window: 0, pane: 0 },
    id: stableSessionId,
    ownerId,
    displayName: undefined,
    label: stableSessionId,
    createdAt: new Date().toISOString(),
    generation: undefined,
    hidden: false,
    background: false,
    scheduleId: undefined,
    cwd: undefined,
    shell: undefined,
    agentType: undefined,
    worktreeBranch: undefined,
    isLocal: false,
    host: undefined,
    hostOnline: true,
  }
}

describe('useToolEvents SessionApp identity normalization (Finding C)', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(() => emptyJsonResponse()))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('marks an SessionApp session (keyed by OwnerID/stableSessionId) as needing attention from a fingerprint/display-label "waiting" event carrying session_id', async () => {
    const fingerprint = 'peer-fingerprint-abc123'
    const ownerId = 'owner-xyz789' // canonical OwnerID -- a DIFFERENT string encoding than the fingerprint
    const stableSessionId = 'session-stable-001'

    const hosts: HostSnapshot[] = [
      {
        peer_id: fingerprint,
        owner_id: ownerId,
        name: 'remote-box',
        online: true,
        last_seen: new Date().toISOString(),
      },
    ]
    const hostIndex = makeHostIndex(hosts)

    const { result } = renderHook(() => useToolEvents(hostIndex))
    await waitFor(() => expect(fetch).toHaveBeenCalled())

    // Real event shape as broadcast by the server: host is the peer
    // fingerprint, session is the mutable display label, session_id is the
    // stable identity the frontend must actually key on.
    act(() => {
      result.current.handleEvent({
        type: 'tool-event',
        tool: 'claude',
        status: 'waiting',
        host: fingerprint,
        host_name: 'remote-box',
        session: 'my-renamed-display-label',
        session_id: stableSessionId,
        window: 0,
        pane: 'pane-1',
        message: 'needs input',
        timestamp: new Date().toISOString(),
      })
    })

    // The exact key SessionView.key / sessionRefToKey produce for a v2
    // session: ownerId is always the OwnerID, id the stable SessionID.
    const v2SessionKey = `${ownerId}/${stableSessionId}`

    // The hook must resolve the event under the OwnerID/stable-id key, not
    // the raw fingerprint/display-label key it physically arrived with.
    expect(result.current.sessionNeedsAttention(v2SessionKey)).toBe(true)
    expect(result.current.getSessionEvents(v2SessionKey)).toHaveLength(1)
    // The key was set once, at ingestion, and matches normalizeToolEvent
    // called directly -- not reconstructed ad hoc by the test or a consumer.
    expect(result.current.getSessionEvents(v2SessionKey)[0].key).toBe(v2SessionKey)

    // Actual UI-facing derived status -- the same sessionViewSignal() App.tsx's
    // glance summary and Sidebar/Overview badges are computed from.
    const view = makeView(ownerId, stableSessionId)
    const signal = sessionViewSignal(
      view,
      result.current.getSessionEvents(v2SessionKey),
      undefined,
      result.current.isSessionInActiveTurn(v2SessionKey),
    )
    expect(signal.state).toBe('needs_you')

    // And the mismatched raw fingerprint/display-label key must NOT match
    // (proves this isn't accidentally passing via some other fallback).
    expect(result.current.sessionNeedsAttention(`${fingerprint}/my-renamed-display-label`)).toBe(false)
  })

  it('marks a local SessionApp session as working from a hook "active" event whose host is empty (local)', async () => {
    const ownerId = 'owner-local-001'
    const stableSessionId = 'session-stable-local-1'

    const hosts: HostSnapshot[] = [
      {
        peer_id: 'local-fingerprint',
        owner_id: ownerId,
        name: 'this machine',
        local: true,
        online: true,
        last_seen: new Date().toISOString(),
      },
    ]
    const hostIndex = makeHostIndex(hosts)

    const { result } = renderHook(() => useToolEvents(hostIndex))
    await waitFor(() => expect(fetch).toHaveBeenCalled())

    act(() => {
      result.current.handleEvent({
        type: 'tool-event',
        tool: 'claude',
        status: 'active',
        host: '', // pkg/toolevents.Event.Host is empty for local sessions
        session: 'my-local-session',
        session_id: stableSessionId,
        window: 0,
        timestamp: new Date().toISOString(),
      })
    })

    const v2SessionKey = `${ownerId}/${stableSessionId}`
    expect(result.current.isSessionInActiveTurn(v2SessionKey)).toBe(true)

    const view = makeView(ownerId, stableSessionId)
    const signal = sessionViewSignal(view, [], undefined, result.current.isSessionInActiveTurn(v2SessionKey))
    expect(signal.state).toBe('working')
  })

  it('renaming a session (display label change) does not break event-to-session correlation, since key derives from session_id not label', async () => {
    const ownerId = 'owner-rename-001'
    const stableSessionId = 'session-stable-rename-1'
    const hostIndex = makeHostIndex([])

    const { result } = renderHook(() => useToolEvents(hostIndex))
    await waitFor(() => expect(fetch).toHaveBeenCalled())

    act(() => {
      result.current.handleEvent({
        type: 'tool-event',
        tool: 'claude',
        status: 'waiting',
        host: '',
        session: 'original-label',
        session_id: stableSessionId,
        window: 0,
        pane: 'pane-1',
        timestamp: new Date().toISOString(),
      })
    })

    // Bare key (no hostIndex owner match for '' host without a local host
    // record) is just the stable session id.
    const key = stableSessionId
    expect(result.current.sessionNeedsAttention(key)).toBe(true)

    // A later event for the SAME session_id but a renamed display label
    // must replace (not duplicate) the tracked event under the same key.
    act(() => {
      result.current.handleEvent({
        type: 'tool-event',
        tool: 'claude',
        status: 'waiting',
        host: '',
        session: 'renamed-label',
        session_id: stableSessionId,
        window: 0,
        pane: 'pane-1',
        message: 'still needs input',
        timestamp: new Date().toISOString(),
      })
    })

    expect(result.current.getSessionEvents(key)).toHaveLength(1)
    expect(result.current.getSessionEvents(key)[0].message).toBe('still needs input')

    // A "completed" event for the renamed label, still carrying the same
    // session_id, must clear the same canonical session.
    act(() => {
      result.current.handleEvent({
        type: 'tool-event',
        tool: 'claude',
        status: 'completed',
        host: '',
        session: 'renamed-label',
        session_id: stableSessionId,
        window: 0,
        pane: 'pane-1',
        timestamp: new Date().toISOString(),
      })
    })

    expect(result.current.getSessionEvents(key)).toHaveLength(0)
    expect(result.current.sessionNeedsAttention(key)).toBe(false)
  })

  it('a host-table refresh (new hostIndex identity, same host data) does not duplicate event entries on the next /api/tool-events poll', async () => {
    const fingerprint = 'peer-fingerprint-refresh'
    const ownerId = 'owner-refresh-001'
    const stableSessionId = 'session-stable-refresh-1'
    const hosts: HostSnapshot[] = [
      { peer_id: fingerprint, owner_id: ownerId, name: 'remote-box', online: true, last_seen: new Date().toISOString() },
    ]

    const rawEvent = {
      tool: 'claude',
      status: 'active' as const,
      host: fingerprint,
      session: 'display-label',
      session_id: stableSessionId,
      window: 0,
      pane: 'pane-1',
      timestamp: new Date().toISOString(),
      auto_detected: false,
    }

    ;(fetch as unknown as ReturnType<typeof vi.fn>).mockImplementation((url: string) => {
      if (url === '/api/tool-events') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([rawEvent]) } as Response)
      }
      return emptyJsonResponse()
    })

    let hostIndex = makeHostIndex(hosts)
    const { result, rerender } = renderHook(({ hi }) => useToolEvents(hi), { initialProps: { hi: hostIndex } })
    await waitFor(() => expect(result.current.events).toHaveLength(1))

    const v2SessionKey = `${ownerId}/${stableSessionId}`
    expect(result.current.getSessionEvents(v2SessionKey)).toHaveLength(1)

    // Simulate a host-table refresh producing a new HostIndex object with
    // identical underlying host data, then force a re-poll of
    // /api/tool-events.
    hostIndex = makeHostIndex(hosts)
    rerender({ hi: hostIndex })
    await act(async () => { await result.current.refresh() })

    expect(result.current.getSessionEvents(v2SessionKey)).toHaveLength(1)
    expect(result.current.events).toHaveLength(1)
  })
})

describe('normalizeToolEvent', () => {
  it('produces the same key format as SessionView.key / sessionRefToKey ("owner/sessionId")', () => {
    const hostIndex = makeHostIndex([
      { peer_id: 'fp-1', owner_id: 'owner-1', name: 'box', online: true, last_seen: new Date().toISOString() },
    ])
    const evt = normalizeToolEvent({
      tool: 'claude',
      status: 'waiting',
      host: 'fp-1',
      session: 'display-label',
      session_id: 'stable-id-1',
      window: 0,
      timestamp: new Date().toISOString(),
    }, hostIndex)
    expect(evt.key).toBe('owner-1/stable-id-1')
  })
})
