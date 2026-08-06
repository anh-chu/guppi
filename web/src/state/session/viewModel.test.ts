import { describe, it, expect } from 'vitest'
import { toSessionView, toPresentationAttrs, sessionViewSignal, stateRank } from './viewModel'
import type { LocalSessionRecord } from './types'
import type { ToolEvent } from '../../hooks/useToolEvents'
import type { ActivitySnapshot } from '../../hooks/useActivity'

function mkRecord(over: Partial<LocalSessionRecord> = {}): LocalSessionRecord {
  return {
    id: 'sess-1',
    owner: '',
    ref: { owner: null, session: 'sess-1', window: 0, pane: 0 },
    phase: 'active',
    desired: 'run',
    revision: 1,
    created_at: '2025-01-01T00:00:00Z',
    ...over,
  }
}

function mkEvent(over: Partial<ToolEvent> = {}): ToolEvent {
  return {
    session: 'sess-1',
    tool: 'claude',
    status: 'active',
    ...over,
  } as ToolEvent
}

const activity = (idleSeconds: number): ActivitySnapshot => ({ idle_seconds: idleSeconds, total_bytes: 0 } as ActivitySnapshot)

describe('toSessionView', () => {
  it('keys by sessionRefToKey(record.ref), not by name', () => {
    const view = toSessionView(mkRecord({ ref: { owner: 'owner1', session: 'sess-1', window: 0, pane: 0 }, name: 'Renamed' }))
    expect(view.key).toBe('owner1/sess-1')
    expect(view.id).toBe('sess-1')
  })

  it('keys with bare id when ref.owner is null (local session)', () => {
    const view = toSessionView(mkRecord({ ref: { owner: null, session: 'sess-1', window: 0, pane: 0 } }))
    expect(view.key).toBe('sess-1')
  })

  it('label falls back to the immutable id when displayName is unset or blank', () => {
    expect(toSessionView(mkRecord()).label).toBe('sess-1')
    expect(toSessionView(mkRecord({ name: '   ' })).label).toBe('sess-1')
    expect(toSessionView(mkRecord({ name: 'Friendly Name' })).label).toBe('Friendly Name')
  })

  it('defaults hidden/background to false when absent from the record', () => {
    const view = toSessionView(mkRecord())
    expect(view.hidden).toBe(false)
    expect(view.background).toBe(false)
  })

  it('reads hidden/background straight off the record when present', () => {
    const view = toSessionView(mkRecord({ hidden: true, background: true }))
    expect(view.hidden).toBe(true)
    expect(view.background).toBe(true)
  })
})

describe('toPresentationAttrs', () => {
  it('buckets views into hidden/background sets keyed by SessionView.key', () => {
    const hidden = toSessionView(mkRecord({ ref: { owner: null, session: 'a', window: 0, pane: 0 }, id: 'a', hidden: true }))
    const bg = toSessionView(mkRecord({ ref: { owner: null, session: 'b', window: 0, pane: 0 }, id: 'b', background: true }))
    const plain = toSessionView(mkRecord({ ref: { owner: null, session: 'c', window: 0, pane: 0 }, id: 'c' }))
    const attrs = toPresentationAttrs([hidden, bg, plain])
    expect(attrs.hidden.has('a')).toBe(true)
    expect(attrs.hidden.has('b')).toBe(false)
    expect(attrs.background.has('b')).toBe(true)
    expect(attrs.background.has('c')).toBe(false)
    expect(attrs.scheduleIDs.size).toBe(0)
  })

  it('returns empty sets for an empty session list', () => {
    const attrs = toPresentationAttrs([])
    expect(attrs.hidden.size).toBe(0)
    expect(attrs.background.size).toBe(0)
    expect(attrs.scheduleIDs.size).toBe(0)
  })
})

describe('sessionViewSignal', () => {
  it('reports needs_you for a loud (waiting/stuck/error) event, regardless of activity', () => {
    const signal = sessionViewSignal([mkEvent({ status: 'waiting' })], activity(0), true)
    expect(signal.state).toBe('needs_you')
    expect(signal.loud).toBe(true)
    expect(signal.reason).toBe('waiting')
  })

  it('reports offline only when hostOnline is explicitly false', () => {
    expect(sessionViewSignal([], undefined, false, false).state).toBe('offline')
    expect(sessionViewSignal([], undefined, false, undefined).state).not.toBe('offline')
    expect(sessionViewSignal([], undefined, false, true).state).not.toBe('offline')
  })

  it('reports working when inActiveTurn is true even with no recent activity snapshot', () => {
    expect(sessionViewSignal([], undefined, true).state).toBe('working')
  })

  it('reports working when the activity snapshot is recent (<= 5 idle seconds)', () => {
    expect(sessionViewSignal([], activity(5), false).state).toBe('working')
    expect(sessionViewSignal([], activity(6), false).state).toBe('idle')
  })

  it('reports idle when nothing else applies', () => {
    expect(sessionViewSignal([], undefined, false).state).toBe('idle')
  })

  it('stateRank orders needs_you < working < idle < offline', () => {
    expect(stateRank.needs_you).toBeLessThan(stateRank.working)
    expect(stateRank.working).toBeLessThan(stateRank.idle)
    expect(stateRank.idle).toBeLessThan(stateRank.offline)
  })
})
