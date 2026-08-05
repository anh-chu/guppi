// @vitest-environment jsdom
//
// Regression proof for round-8 Finding C: AppV2's session keys are always
// `${ownerId}/${stableSessionId}` (Session.host is the v2 OwnerID, never the
// peer transport fingerprint -- see App.tsx's `sessions` memo and
// sessionKey() in useSessions.ts), but tool/activity events arrive keyed by
// `{host: peer-fingerprint, session: mutable-display-label}` with a SEPARATE
// stable `session_id` field the frontend previously never read (see
// pkg/toolevents.Event / pkg/ws/hub.go's WS wrapping). Before the fix, these
// two encodings never matched, so working/waiting/stuck indicators silently
// never updated for v2 sessions.
//
// This test drives useToolEvents' REAL implementation (not mocked, unlike
// App.test.tsx's App-level mock of this hook) through its actual public
// surface (handleEvent, the same function App.tsx wires to the WebSocket;
// sessionNeedsAttention/getSessionEvents/isSessionInActiveTurn, the same
// functions Sidebar/Overview call), and asserts the actual UI-facing
// derived status via sessionSignal -- the exact function App.tsx's `glance`
// summary and session badges are computed from -- not just an internal map
// having an entry.
import { renderHook, act, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useToolEvents } from './useToolEvents'
import type { Host } from './useHosts'
import type { Session } from './useSessions'
import { sessionSignal } from '../lib/sessionState'

const emptyJsonResponse = () =>
  Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)

describe('useToolEvents AppV2 identity normalization (Finding C)', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(() => emptyJsonResponse()))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('marks an AppV2 session (keyed by OwnerID/stableSessionId) as needing attention from a fingerprint/display-label "waiting" event carrying session_id', async () => {
    const fingerprint = 'peer-fingerprint-abc123'
    const ownerId = 'owner-xyz789' // v2 OwnerID -- a DIFFERENT string encoding than the fingerprint
    const stableSessionId = 'session-stable-001'

    const hosts: Host[] = [
      {
        id: fingerprint,
        owner_id: ownerId,
        name: 'remote-box',
        online: true,
        sessions: [],
        last_seen: new Date().toISOString(),
      },
    ]

    const { result } = renderHook(() => useToolEvents(hosts))
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

    // The exact key App.tsx's `sessions` memo / sessionKey() produce for a
    // v2 session: Session.host is always the OwnerID, Session.name the
    // stable SessionID.
    const v2SessionKey = `${ownerId}/${stableSessionId}`

    // The hook must resolve the event under the OwnerID/stable-id key, not
    // the raw fingerprint/display-label key it physically arrived with.
    expect(result.current.sessionNeedsAttention(v2SessionKey)).toBe(true)
    expect(result.current.getSessionEvents(v2SessionKey)).toHaveLength(1)

    // Actual UI-facing derived status -- the same sessionSignal() App.tsx's
    // glance summary and Sidebar/Overview badges are computed from.
    const session: Session = {
      id: stableSessionId,
      name: stableSessionId,
      host: ownerId,
      windows: [],
      created: new Date().toISOString(),
      attached: true,
      last_activity: new Date().toISOString(),
    }
    const signal = sessionSignal(
      session,
      result.current.getSessionEvents(v2SessionKey),
      undefined,
      result.current.isSessionInActiveTurn(v2SessionKey),
    )
    expect(signal.state).toBe('needs_you')

    // And the mismatched raw fingerprint/display-label key must NOT match
    // (proves this isn't accidentally passing via some other fallback).
    expect(result.current.sessionNeedsAttention(`${fingerprint}/my-renamed-display-label`)).toBe(false)
  })

  it('marks a local AppV2 session as working from a hook "active" event whose host is empty (local)', async () => {
    const ownerId = 'owner-local-001'
    const stableSessionId = 'session-stable-local-1'

    const hosts: Host[] = [
      {
        id: 'local-fingerprint',
        owner_id: ownerId,
        name: 'this machine',
        local: true,
        online: true,
        sessions: [],
        last_seen: new Date().toISOString(),
      },
    ]

    const { result } = renderHook(() => useToolEvents(hosts))
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

    const session: Session = {
      id: stableSessionId,
      name: stableSessionId,
      host: ownerId,
      windows: [],
      created: new Date().toISOString(),
      attached: true,
      last_activity: new Date().toISOString(),
    }
    const signal = sessionSignal(session, [], undefined, result.current.isSessionInActiveTurn(v2SessionKey))
    expect(signal.state).toBe('working')
  })
})
