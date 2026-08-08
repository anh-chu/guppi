import { describe, it, expect } from 'vitest'
import { sessionSignal, isSessionActive, stateRank } from './sessionState'
import type { Session } from '../hooks/useSessions'
import type { ToolEvent } from '../hooks/useToolEvents'
import type { ActivitySnapshot } from '../hooks/useActivity'

const mkSession = (over: Partial<Session> = {}): Session => ({
  id: 's1',
  name: 'demo',
  windows: [],
  created: '',
  ...over,
} as Session)

const win = (panes: { current_command?: string; id?: string }[]) => ({
  id: 'w1',
  session_id: 's1',
  name: 'w',
  index: 0,
  active: true,
  layout: '',
  panes: panes.map((p, i) => ({
    id: p.id ?? `%${i}`,
    window_id: 'w1',
    session_id: 's1',
    index: i,
    active: true,
    current_command: p.current_command ?? '',
    pid: 1,
  })),
})

const evt = (over: Partial<ToolEvent> = {}): ToolEvent => ({
  tool: 'claude',
  status: 'waiting',
  session: 'demo',
  window: 0,
  timestamp: '',
  ...over,
})

const act = (idle: number): ActivitySnapshot => ({ session: 'demo', idle_seconds: idle, total_bytes: 0 })

describe('sessionSignal', () => {
  it('detects offline', () => {
    const sig = sessionSignal(mkSession({ host: 'h1', host_online: false }), [], undefined, false)
    expect(sig.state).toBe('offline')
    expect(sig.loud).toBe(false)
  })

  it('detects waiting needs_you', () => {
    const sig = sessionSignal(mkSession(), [evt()], undefined, false)
    expect(sig.state).toBe('needs_you')
    expect(sig.loud).toBe(true)
    expect(sig.reason).toBe('waiting')
    expect(sig.tool).toBe('claude')
  })

  it('detects stuck needs_you', () => {
    const sig = sessionSignal(mkSession(), [evt({ status: 'stuck' })], undefined, false)
    expect(sig.state).toBe('needs_you')
    expect(sig.loud).toBe(true)
    expect(sig.reason).toBe('stuck')
  })

  it('detects error needs_you', () => {
    const sig = sessionSignal(mkSession(), [evt({ status: 'error' })], undefined, false)
    expect(sig.state).toBe('needs_you')
    expect(sig.loud).toBe(true)
    expect(sig.reason).toBe('error')
  })

  it('lets needs_you beat offline', () => {
    const sig = sessionSignal(mkSession({ host: 'h1', host_online: false }), [evt()], undefined, false)
    expect(sig.state).toBe('needs_you')
  })

  it('detects working via active turn', () => {
    const sig = sessionSignal(mkSession(), [], undefined, true)
    expect(sig.state).toBe('working')
  })

  it('detects working via command', () => {
    const sig = sessionSignal(mkSession({ windows: [win([{ current_command: 'claude' }])] }), [], undefined, false)
    expect(sig.state).toBe('working')
  })

  it('detects working via fresh activity', () => {
    const sig = sessionSignal(mkSession(), [], act(2), false)
    expect(sig.state).toBe('working')
  })

  it('treats five seconds as working and six as idle', () => {
    expect(sessionSignal(mkSession(), [], act(5), false).state).toBe('working')
    expect(sessionSignal(mkSession(), [], act(6), false).state).toBe('idle')
  })

  it('detects idle', () => {
    const sig = sessionSignal(mkSession(), [], act(120), false)
    expect(sig.state).toBe('idle')
  })

  it('detects idle with undefined activity', () => {
    const sig = sessionSignal(mkSession(), [], undefined, false)
    expect(sig.state).toBe('idle')
  })

  it('counts agent panes', () => {
    const sig = sessionSignal(mkSession({ windows: [win([{ current_command: 'claude' }, { current_command: 'bash' }])] }), [], undefined, false)
    expect(sig.agentCount).toBe(1)
  })

  it('counts agent panes via event pane ids', () => {
    const sig = sessionSignal(
      mkSession({ windows: [win([{ current_command: 'bash', id: '%9' }])] }),
      [evt({ status: 'active', pane: '%9' })],
      undefined,
      false,
    )
    expect(sig.agentCount).toBe(1)
    expect(sig.state).toBe('idle')
  })

  it('keeps active event from forcing needs_you', () => {
    const sig = sessionSignal(mkSession(), [evt({ status: 'active' })], undefined, false)
    expect(sig.state).toBe('idle')
  })

  it('checks isSessionActive', () => {
    expect(isSessionActive(mkSession({ windows: [win([{ current_command: 'bash' }])] }))).toBe(false)
    expect(isSessionActive(mkSession({ windows: [win([{ current_command: 'claude' }])] }))).toBe(true)
  })

  it('orders state rank', () => {
    expect(stateRank.needs_you).toBeLessThan(stateRank.working)
    expect(stateRank.working).toBeLessThan(stateRank.idle)
    expect(stateRank.idle).toBeLessThan(stateRank.offline)
  })
})
