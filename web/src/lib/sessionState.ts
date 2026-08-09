import type { Session } from '../hooks/useSessions'
import type { ToolEvent } from '../hooks/useToolEvents'
import type { ActivitySnapshot } from '../hooks/useActivity'

export type SessionState = 'needs_you' | 'working' | 'idle' | 'offline'
export type SessionStatus = 'working' | 'waiting' | 'stuck' | 'error' | 'idle' | 'shell' | 'process'

export interface SessionSignal {
  state: SessionState
  loud: boolean
  reason?: string
  tool?: string
  agentCount: number
}

export interface SessionProjection {
  status: SessionStatus
  signal: SessionSignal
  needsAttention: boolean
  activeCommand: string
  commandIsShell: boolean
  agentPresent: boolean
  runningCommands: string[]
  userPrompt?: string
  lastAgentMessage?: string
  promptPreview?: string
  activityLabel?: string
  loudEvent?: ToolEvent
}

const agentCommands = new Set(['claude', 'codex', 'copilot', 'opencode'])
const SHELL_COMMANDS = new Set(['bash', 'zsh', 'fish', 'sh', 'dash', 'ksh', 'csh', 'tcsh', 'tmux', 'login'])
const NATIVE_HOOK_TOOLS = new Set(['pi', 'claude', 'opencode', 'codex'])
export const LOUD_STATUSES = new Set(['waiting', 'stuck', 'error'])

export const stateRank: Record<SessionState, number> = {
  needs_you: 0,
  working: 1,
  idle: 2,
  offline: 3,
}

export function isSessionActive(session: Session): boolean {
  if (!session.windows) return false
  return session.windows.some(w =>
    w.panes?.some(p => p.current_command && !SHELL_COMMANDS.has(p.current_command)),
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

// Get command from active pane (or first pane if no active)
function getActivePaneCommand(session: Session): string {
  for (const w of session.windows ?? []) {
    for (const p of w.panes ?? []) {
      if (p.active) return p.current_command ?? ''
    }
  }
  return session.windows?.[0]?.panes?.[0]?.current_command ?? ''
}

// Get loud event (precedence: stuck > waiting > error)
function getLoudEvent(events: ToolEvent[]): ToolEvent | undefined {
  return events.find(e => e.status === 'stuck')
    || events.find(e => e.status === 'waiting')
    || events.find(e => e.status === 'error')
}

// Compute detail status: 7-state canonical detail for UI display
export function sessionStatus(
  session: Session,
  events: ToolEvent[],
  inActiveTurn: boolean,
): SessionStatus {
  const hasHookHistory = !!(session.user_prompt?.trim() || session.last_agent_message?.trim())
  const activeCmd = getActivePaneCommand(session)
  const cmdIsShell = SHELL_COMMANDS.has(activeCmd) // only actual shell commands, not empty
  const detectedAgent = events.find(e => e.status === 'active' && e.auto_detected)
  const isNativeHookTool = NATIVE_HOOK_TOOLS.has(activeCmd)
  const loudEvent = getLoudEvent(events)

  // Precedence: stuck > waiting > error > inActiveTurn working > auto-detected non-native-hook working > hook-history idle > native-hook-agent idle > empty/shell/process
  if (loudEvent) return loudEvent.status as SessionStatus
  if (inActiveTurn) return 'working'
  if (detectedAgent && !NATIVE_HOOK_TOOLS.has(detectedAgent.tool)) return 'working'
  if (hasHookHistory || !activeCmd) return 'idle' // hook history OR empty command = idle
  if (detectedAgent && NATIVE_HOOK_TOOLS.has(detectedAgent.tool)) return 'idle'
  if (isNativeHookTool) return 'idle'
  return cmdIsShell ? 'shell' : 'process' // non-shell non-agent pane
}

// Derive 4-state signal from detail status + offline + activity (internal helper)
function signalFromStatus(
  detail: SessionStatus,
  session: Session,
  events: ToolEvent[],
  activity: ActivitySnapshot | undefined,
): SessionSignal {
  const eventPanes = new Set(events.map(e => e.pane).filter((pane): pane is string => !!pane))
  const agentCount = (session.windows || []).reduce(
    (n, w) => n + (w.panes || []).filter(p => agentCommands.has(p.current_command) || eventPanes.has(p.id)).length,
    0,
  )
  const loudEvent = getLoudEvent(events)
  const tool = (loudEvent || events[0])?.tool

  const detailToState: Record<SessionStatus, SessionState | null> = {
    'working': 'working',
    'waiting': 'needs_you',
    'stuck': 'needs_you',
    'error': 'needs_you',
    'idle': null,    // May upgrade to working
    'shell': null,   // May upgrade to working
    'process': null, // May upgrade to working
  }

  const baseState = detailToState[detail]
  if (baseState) {
    return { state: baseState, loud: !!loudEvent, reason: loudEvent?.status, tool, agentCount }
  }

  // Check offline before activity upgrade
  if (session.host && session.host_online === false) {
    return { state: 'offline', loud: false, tool, agentCount }
  }

  // Activity upgrade: idle_seconds <= 5 and isSessionActive only upgrade idle-family to working
  // Exception: exempt native-hook agents from isSessionActive-based upgrade
  // Native-hook agent: detected via auto-detected event, hook history + native-hook command, or explicit native-hook command
  const hasHookHistory = !!(session.user_prompt?.trim() || session.last_agent_message?.trim())
  const activeCmd = getActivePaneCommand(session)
  const isNativeHookTool = NATIVE_HOOK_TOOLS.has(activeCmd)
  const hasNativeHookAgent = events.some(e => e.status === 'active' && e.auto_detected && NATIVE_HOOK_TOOLS.has(e.tool))
    || (hasHookHistory && isNativeHookTool)
  const isActive = isSessionActive(session)
  const freshActivity = activity !== undefined && activity.idle_seconds <= 5

  if (freshActivity || (isActive && !hasNativeHookAgent)) {
    return { state: 'working', loud: false, tool, agentCount }
  }

  return { state: 'idle', loud: false, tool, agentCount }
}

// Derive 4-state signal from detail status + offline + activity
export function sessionSignal(
  session: Session,
  events: ToolEvent[],
  activity: ActivitySnapshot | undefined,
  inActiveTurn: boolean,
): SessionSignal {
  const detail = sessionStatus(session, events, inActiveTurn)
  return signalFromStatus(detail, session, events, activity)
}

// Full projection: detail status + signal + all useful fields for UI
export function sessionProjection(
  session: Session,
  events: ToolEvent[],
  activity: ActivitySnapshot | undefined,
  inActiveTurn: boolean,
): SessionProjection {
  const activeCmd = getActivePaneCommand(session)
  const cmdIsShell = SHELL_COMMANDS.has(activeCmd)
  const detectedAgent = events.find(e => e.status === 'active' && e.auto_detected)
  const agentPresent = !cmdIsShell || !!detectedAgent

  // Running commands: non-shell across all panes (single occurrence per command)
  const runningCommands: string[] = []
  const seen = new Set<string>()
  for (const w of session.windows ?? []) {
    for (const p of w.panes ?? []) {
      const cmd = p.current_command
      if (cmd && !SHELL_COMMANDS.has(cmd) && !seen.has(cmd)) {
        seen.add(cmd)
        runningCommands.push(cmd)
      }
    }
  }

  const userPrompt = session.user_prompt?.trim()
  const lastAgentMessage = session.last_agent_message?.trim()
  const promptPreview = session.prompt_preview?.trim()

  const activeEvent = events.find(e => e.status === 'active' && !e.auto_detected)
  const waitingEvent = events.find(e => e.status === 'waiting')
  const activityLabel = activeEvent?.message || (waitingEvent?.message ?? undefined)

  const loudEvent = getLoudEvent(events)
  const needsAttention = !!loudEvent

  const status = sessionStatus(session, events, inActiveTurn)
  const signal = signalFromStatus(status, session, events, activity)

  return {
    status,
    signal,
    needsAttention,
    activeCommand: activeCmd,
    commandIsShell: cmdIsShell,
    agentPresent,
    runningCommands,
    userPrompt,
    lastAgentMessage,
    promptPreview,
    activityLabel: activityLabel || undefined,
    loudEvent,
  }
}
