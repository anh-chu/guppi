// @vitest-environment jsdom
//
// Round-9 Finding, Gap 2: AppV2's session keys are always
// `${ownerId}/${stableSessionId}` (Session.host is the v2 OwnerID, never the
// peer transport fingerprint), but the server's PTY activity snapshot is
// keyed `${peerFingerprint}/${SessionID}`. Before the fix, useActivity had no
// host-normalization logic (unlike its sibling useToolEvents), so a v2
// session with live PTY output but no current tool-hook event was
// misclassified as inactive because the lookup key never matched.
//
// This test drives useActivity's REAL implementation (not mocked) through
// its actual public surface (handleActivityEvent, the same function App.tsx
// wires to the WebSocket's "activity" message; getSessionActivity, the same
// function Sidebar/Overview call), and asserts the actual UI-facing derived
// status via sessionSignal -- the exact function App.tsx's badges are
// computed from -- not just an internal map having an entry. Mirrors
// useToolEvents.appv2.test.ts's exact style.
import { renderHook, act, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useActivity } from './useActivity'
import type { Host } from './useHosts'
import type { Session } from '../lib/session'
import { sessionSignal } from '../lib/sessionState'

const emptyJsonResponse = () =>
  Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)

describe('useActivity AppV2 identity normalization (Finding, Gap 2)', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(() => emptyJsonResponse()))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('reports an AppV2 session (keyed by OwnerID/stableSessionId) as recently-active from a fingerprint-keyed activity snapshot', async () => {
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

    const { result } = renderHook(() => useActivity(hosts))
    await waitFor(() => expect(fetch).toHaveBeenCalled())

    // Real snapshot shape as broadcast by the server: host is the peer
    // fingerprint, session is the durable session ID.
    act(() => {
      result.current.handleActivityEvent([
        { host: fingerprint, session: stableSessionId, idle_seconds: 1, total_bytes: 4096 },
      ])
    })

    const v2SessionKey = `${ownerId}/${stableSessionId}`

    // The hook must resolve the snapshot under the OwnerID/stable-id key,
    // not the raw fingerprint key it physically arrived with.
    const looked = result.current.getSessionActivity(v2SessionKey)
    expect(looked).toBeDefined()
    expect(looked?.idle_seconds).toBe(1)

    // And the mismatched raw fingerprint key must NOT match (proves this
    // isn't accidentally passing via some other fallback).
    expect(result.current.getSessionActivity(`${fingerprint}/${stableSessionId}`)).toBeUndefined()

    // Actual UI-facing derived status -- the same sessionSignal() App.tsx's
    // badges are computed from.
    const session: Session = {
      id: stableSessionId,
      name: stableSessionId,
      host: ownerId,
      windows: [],
      created: new Date().toISOString(),
      attached: true,
      last_activity: new Date().toISOString(),
    }
    const signal = sessionSignal(session, [], looked, false)
    expect(signal.state).toBe('working')
  })

  it('reports a local AppV2 session as recently-active from an activity snapshot whose host is empty (local)', async () => {
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

    const { result } = renderHook(() => useActivity(hosts))
    await waitFor(() => expect(fetch).toHaveBeenCalled())

    act(() => {
      result.current.handleActivityEvent([
        { host: '', session: stableSessionId, idle_seconds: 2, total_bytes: 128 },
      ])
    })

    const v2SessionKey = `${ownerId}/${stableSessionId}`
    const looked = result.current.getSessionActivity(v2SessionKey)
    expect(looked).toBeDefined()

    const session: Session = {
      id: stableSessionId,
      name: stableSessionId,
      host: ownerId,
      windows: [],
      created: new Date().toISOString(),
      attached: true,
      last_activity: new Date().toISOString(),
    }
    const signal = sessionSignal(session, [], looked, false)
    expect(signal.state).toBe('working')
  })
})
