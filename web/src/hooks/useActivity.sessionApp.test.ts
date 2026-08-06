// @vitest-environment jsdom
//
// Round-9 Finding, Gap 2: SessionApp's session keys are always
// `${ownerId}/${stableSessionId}` (SessionView.ownerId is the canonical OwnerID,
// never the peer transport fingerprint), but the server's PTY activity snapshot
// is keyed `${peerFingerprint}/${SessionID}`. Before the fix, useActivity had no
// host-normalization logic (unlike its sibling useToolEvents), so a v2
// session with live PTY output but no current tool-hook event was
// misclassified as inactive because the lookup key never matched.
//
// Task 5 (identity canonicalization at ingestion): the hook now takes the
// HostIndex directly (useHosts' hostIndex, byPeerId keyed) rather than a raw
// Host[] the hook itself had to scan, and every snapshot carries the
// canonical `key` set once by normalizeActivitySnapshot -- these tests build
// a HostIndex the same way useHosts.ts does.
//
// This test drives useActivity's REAL implementation (not mocked) through
// its actual public surface (handleActivityEvent, the same function App.tsx
// wires to the WebSocket's "activity" message; getSessionActivity, the same
// function Sidebar/Overview call), and asserts the actual UI-facing derived
// status via sessionViewSignal -- the exact function App.tsx's badges are
// computed from -- not just an internal map having an entry.
import { renderHook, act, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useActivity, normalizeActivitySnapshot } from './useActivity'
import type { Host } from './useHosts'
import type { HostIndex, SessionView } from '../state/session/viewModel'
import { sessionViewSignal } from '../state/session/viewModel'

const emptyJsonResponse = () =>
  Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)

function makeHostIndex(hosts: Host[]): HostIndex {
  const byPeerId = new Map<string, Host>()
  const byOwnerId = new Map<string, Host>()
  let local: Host | undefined
  for (const h of hosts) {
    byPeerId.set(h.id, h)
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

describe('useActivity SessionApp identity normalization (Finding, Gap 2)', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(() => emptyJsonResponse()))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('reports an SessionApp session (keyed by OwnerID/stableSessionId) as recently-active from a fingerprint-keyed activity snapshot', async () => {
    const fingerprint = 'peer-fingerprint-abc123'
    const ownerId = 'owner-xyz789' // canonical OwnerID -- a DIFFERENT string encoding than the fingerprint
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
    const hostIndex = makeHostIndex(hosts)

    const { result } = renderHook(() => useActivity(hostIndex))
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
    expect(looked?.key).toBe(v2SessionKey)

    // And the mismatched raw fingerprint key must NOT match (proves this
    // isn't accidentally passing via some other fallback).
    expect(result.current.getSessionActivity(`${fingerprint}/${stableSessionId}`)).toBeUndefined()

    // Actual UI-facing derived status -- the same sessionViewSignal() App.tsx's
    // badges are computed from.
    const view = makeView(ownerId, stableSessionId)
    const signal = sessionViewSignal(view, [], looked, false)
    expect(signal.state).toBe('working')
  })

  it('reports a local SessionApp session as recently-active from an activity snapshot whose host is empty (local)', async () => {
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
    const hostIndex = makeHostIndex(hosts)

    const { result } = renderHook(() => useActivity(hostIndex))
    await waitFor(() => expect(fetch).toHaveBeenCalled())

    act(() => {
      result.current.handleActivityEvent([
        { host: '', session: stableSessionId, idle_seconds: 2, total_bytes: 128 },
      ])
    })

    const v2SessionKey = `${ownerId}/${stableSessionId}`
    const looked = result.current.getSessionActivity(v2SessionKey)
    expect(looked).toBeDefined()

    const view = makeView(ownerId, stableSessionId)
    const signal = sessionViewSignal(view, [], looked, false)
    expect(signal.state).toBe('working')
  })
})

describe('normalizeActivitySnapshot', () => {
  it('produces the same key format as SessionView.key / ToolEvent.key ("owner/sessionId")', () => {
    const hostIndex = makeHostIndex([
      { id: 'fp-1', owner_id: 'owner-1', name: 'box', online: true, sessions: [], last_seen: new Date().toISOString() },
    ])
    const snap = normalizeActivitySnapshot({ host: 'fp-1', session: 'stable-id-1', idle_seconds: 0, total_bytes: 0 }, hostIndex)
    expect(snap.key).toBe('owner-1/stable-id-1')
  })
})
