import type { Session } from '../lib/session'
import type { ToolEvent } from '../hooks/useToolEvents'
import type { ActivitySnapshot } from '../hooks/useActivity'

export type SessionState = 'needs_you' | 'working' | 'idle' | 'offline'

export interface SessionSignal {
  state: SessionState
  loud: boolean
  reason?: string
  tool?: string
  agentCount: number
}

const agentCommands = new Set(['claude', 'codex', 'copilot', 'opencode'])
const shellCommands = new Set(['bash', 'zsh', 'fish', 'sh', 'dash', 'ksh', 'csh', 'tcsh', 'tmux', 'login'])
const loudStatuses = new Set(['waiting', 'stuck', 'error'])

export const stateRank: Record<SessionState, number> = {
  needs_you: 0,
  working: 1,
  idle: 2,
  offline: 3,
}

export function isSessionActive(session: Session): boolean {
  if (!session.windows) return false
  return session.windows.some(w =>
    w.panes?.some(p => p.current_command && !shellCommands.has(p.current_command)),
  )
}

// Classify by nature (companion pane with no agent), not the live command or
// stale event history: running a process in the pane, or a pane id that once
// carried a tool event, must not unfold it. A true tool pane never gains
// agent_type, an auto-detected agent process, or hook history.
export function isToolSession(session: Session, events: ToolEvent[]): boolean {
  return !session.agent_type
    && !events.some(e => e.status === 'active' && e.auto_detected)
    && !(session.user_prompt?.trim() || session.last_agent_message?.trim())
}

export function sessionSignal(
  session: Session,
  events: ToolEvent[],
  activity: ActivitySnapshot | undefined,
  inActiveTurn: boolean,
): SessionSignal {
  const eventPanes = new Set(events.map(e => e.pane).filter((pane): pane is string => !!pane))
  const agentCount = (session.windows || []).reduce(
    (n, w) => n + (w.panes || []).filter(p => agentCommands.has(p.current_command) || eventPanes.has(p.id)).length,
    0,
  )
  const loudEvent = events.find(e => loudStatuses.has(e.status))
  const tool = (loudEvent || events[0])?.tool

  if (loudEvent) {
    return { state: 'needs_you', loud: true, reason: loudEvent.status, tool, agentCount }
  }

  if (session.host && session.host_online === false) {
    return { state: 'offline', loud: false, tool, agentCount }
  }

  const working = inActiveTurn || isSessionActive(session) || (activity != null && activity.idle_seconds <= 5)
  if (working) {
    return { state: 'working', loud: false, tool, agentCount }
  }

  return { state: 'idle', loud: false, tool, agentCount }
}
