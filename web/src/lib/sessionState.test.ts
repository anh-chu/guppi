import { describe, it, expect } from 'vitest'
import { sessionSignal, isSessionActive, stateRank, sessionStatus, sessionProjection } from './sessionState'
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

describe('sessionStatus (detail)', () => {

  it('mixed stuck+waiting+error events yield loudEvent stuck', () => {
    const proj = sessionProjection(mkSession(), [
      evt({ status: 'error', message: 'err' }),
      evt({ status: 'waiting', message: 'wait' }),
      evt({ status: 'stuck', message: 'stuck msg' }),
    ], undefined, false)
    expect(proj.status).toBe('stuck')
    expect(proj.loudEvent?.status).toBe('stuck')
  })

  it('native-hook agent at prompt is idle', () => {
    const st = sessionStatus(
      mkSession({ windows: [win([{ current_command: 'claude' }])] }),
      [evt({ status: 'active', tool: 'claude', auto_detected: true })],
      false,
    )
    expect(st).toBe('idle')
  })

  it('treats tmux and login as shell', () => {
    const tmuxSt = sessionStatus(mkSession({ windows: [win([{ current_command: 'tmux' }])] }), [], false)
    const loginSt = sessionStatus(mkSession({ windows: [win([{ current_command: 'login' }])] }), [], false)
    expect(tmuxSt).toBe('shell')
    expect(loginSt).toBe('shell')
  })

  it('picks active pane over first', () => {
    const firstPaneCmd = 'bash'
    const activePaneCmd = 'python'
    const session = mkSession({
      windows: [{
        id: 'w1',
        session_id: 's1',
        name: 'w',
        index: 0,
        active: true,
        layout: '',
        panes: [
          { id: '%0', window_id: 'w1', session_id: 's1', index: 0, active: false, current_command: firstPaneCmd, pid: 1 },
          { id: '%1', window_id: 'w1', session_id: 's1', index: 1, active: true, current_command: activePaneCmd, pid: 2 },
        ],
      }],
    })
    const st = sessionStatus(session, [], false)
    expect(st).toBe('process') // python is not shell
  })

  it('selects active pane even with empty command', () => {
    const session = mkSession({
      windows: [{
        id: 'w1',
        session_id: 's1',
        name: 'w',
        index: 0,
        active: true,
        layout: '',
        panes: [
          { id: '%0', window_id: 'w1', session_id: 's1', index: 0, active: false, current_command: 'python', pid: 1 },
          { id: '%1', window_id: 'w1', session_id: 's1', index: 1, active: true, current_command: '', pid: 2 },
        ],
      }],
    })
    const st = sessionStatus(session, [], false)
    expect(st).toBe('idle') // active pane is empty, so idle (not process from first pane)
  })

  it('falls back to first pane if no active', () => {
    const session = mkSession({
      windows: [{
        id: 'w1',
        session_id: 's1',
        name: 'w',
        index: 0,
        active: true,
        layout: '',
        panes: [
          { id: '%0', window_id: 'w1', session_id: 's1', index: 0, active: false, current_command: 'python', pid: 1 },
          { id: '%1', window_id: 'w1', session_id: 's1', index: 1, active: false, current_command: 'bash', pid: 2 },
        ],
      }],
    })
    const st = sessionStatus(session, [], false)
    expect(st).toBe('process') // first pane is python
  })
})

describe('sessionProjection', () => {
  it('includes error in loud event', () => {
    const proj = sessionProjection(mkSession(), [evt({ status: 'error' })], undefined, false)
    expect(proj.status).toBe('error')
    expect(proj.needsAttention).toBe(true)
    expect(proj.loudEvent?.status).toBe('error')
  })

  it('trims user prompt, last agent message, prompt preview', () => {
    const proj = sessionProjection(
      mkSession({
        user_prompt: '  hello world  ',
        last_agent_message: '  response  ',
        prompt_preview: '  preview  ',
      }),
      [],
      undefined,
      false,
    )
    expect(proj.userPrompt).toBe('hello world')
    expect(proj.lastAgentMessage).toBe('response')
    expect(proj.promptPreview).toBe('preview')
  })

  it('includes running commands (non-shell, no duplicates)', () => {
    const proj = sessionProjection(
      mkSession({
        windows: [
          win([{ current_command: 'bash' }, { current_command: 'python' }]),
          win([{ current_command: 'python' }, { current_command: 'node' }]),
        ],
      }),
      [],
      undefined,
      false,
    )
    expect(proj.runningCommands).toEqual(['python', 'node'])
  })

  it('includes activity label from active event', () => {
    const proj = sessionProjection(
      mkSession(),
      [evt({ status: 'active', message: 'reading files' })],
      undefined,
      false,
    )
    expect(proj.activityLabel).toBe('reading files')
  })

  it('includes activity label from waiting event if no active event', () => {
    const proj = sessionProjection(
      mkSession(),
      [evt({ status: 'waiting', message: 'what should I do?' })],
      undefined,
      false,
    )
    expect(proj.activityLabel).toBe('what should I do?')
  })
})

describe('sessionSignal (4-state)', () => {
  it('offline takes precedence over idle/shell/process', () => {
    const sig = sessionSignal(
      mkSession({ host: 'h1', host_online: false, windows: [win([{ current_command: 'python' }])] }),
      [],
      undefined,
      false,
    )
    expect(sig.state).toBe('offline')
  })

  it('process detail maps to working signal', () => {
    const sig = sessionSignal(
      mkSession({ windows: [win([{ current_command: 'python' }])] }),
      [],
      undefined,
      false,
    )
    expect(sig.state).toBe('working')
  })

  it('native-hook agent via hook-history not upgraded by isSessionActive', () => {
    const sig = sessionSignal(
      mkSession({
        user_prompt: 'hello',
        windows: [win([{ current_command: 'claude' }])],
      }),
      [],
      undefined,
      false,
    )
    expect(sig.state).toBe('idle')
  })
})
