/**
 * Canonical UI-facing session view model for the SessionApp frontend.
 *
 * SessionView is the display-oriented shape SessionApp derives directly from the
 * canonical catalog (state/session/types.ts's LocalSessionRecord) and hands to
 * presentation logic. It intentionally has no `windows`/`panes`/`attached`/fake-timestamp
 * fields: v2 sessions are always single-pane daemon sessions with real
 * created_at metadata, unlike the legacy `Session` type (src/lib/session.ts).
 *
 * This module must not import anything from src/lib/session.ts -- it is
 * the extraction point the canonical frontend depends on instead.
 */

import type { LocalSessionRecord, SessionRef, SessionPhase } from './types'
import { sessionRefToKey } from './paneTreeAdapter'
import type { HostSnapshot, SessionRuntime } from './wireTypes'
import type { ToolEvent } from '../../hooks/useToolEvents'

export type SessionState = 'crashed' | 'needs_you' | 'offline' | 'starting' | 'working' | 'idle'

export interface SessionSignal {
  state: SessionState
  loud: boolean
  reason?: string
  tool?: string
}

export const stateRank: Record<SessionState, number> = {
  crashed: 0,
  needs_you: 1,
  working: 2,
  starting: 3,
  idle: 4,
  offline: 5,
}

/**
 * Host lookup table SessionApp builds from canonical host snapshots
 * and threads into toSessionView so per-session host resolution
 * never re-scans the host list.
 */
export interface HostIndex {
  hosts: HostSnapshot[]
  local?: HostSnapshot
  byPeerId: Map<string, HostSnapshot>
  byOwnerId: Map<string, HostSnapshot>
}

/** Canonical UI-facing session shape for SessionApp. No tmux-shaped fields. */
export interface SessionView {
  /** sessionRefToKey(ref): "owner/id", or bare "id" for a null/local owner. */
  key: string
  /** Canonical identity this view was derived from. */
  ref: SessionRef
  id: string
  ownerId: string
  /** Raw mutable display name, if any set via the `label` command. */
  displayName: string | undefined
  /** displayName when set and non-blank, else falls back to the immutable id -- mirrors legacy sessionLabel(). */
  label: string
  createdAt: string
  generation: string | undefined
  /** Session lifecycle phase: pending, starting, active, crashed, cleanly_ended, dismissed. */
  phase: SessionPhase
  /** true once ActionSetPresentation ("set_presentation") has hidden this session. */
  hidden: boolean
  /** true once ActionSetPresentation has backgrounded this session. */
  background: boolean
  scheduleId: string | undefined
  cwd: string | undefined
  shell: string | undefined
  agentType: string | undefined
  worktreeBranch: string | undefined
  /** true when this session's owner is the local node's own catalog owner. */
  isLocal: boolean
  /** Resolved host record for this session's owner, if known. */
  host: HostSnapshot | undefined
  /**
   * Connectivity for this session's owner. Local host is online whenever a
   * local host record exists; a remote owner is online only when its own
   * host record says so. An unknown remote owner (no host record) is
   * offline -- never optimistically online.
   */
  hostOnline: boolean
  /** Runtime fields from latest session snapshot. */
  currentPath?: string
  currentCommand?: string
  promptPreview?: string
  lastActivity?: string
  /** Idle time in seconds; populated from runtime snapshot. */
  idleSeconds?: number
  /** Total bytes transferred; populated from runtime snapshot. */
  totalBytes?: number
  /** Latest tool event for this session, if any. */
  latestToolEvent?: ToolEvent
}

/** Canonical hidden/background/schedule attribute sets SessionApp hands to Sidebar/Overview/SessionActionsMenu. */
export interface SessionPresentationAttrs {
  hidden: Set<string>
  background: Set<string>
  scheduleIDs: Map<string, string>
}

/**
 * Builds a SessionView straight from a canonical catalog record, host table,
 * runtime snapshot, and latest tool events. Components never join raw stores
 * themselves; SessionApp builds these once and distributes to all presenters.
 */
export function toSessionView(
  opts: {
    record: LocalSessionRecord
    hosts: HostIndex
    localOwner: string | null
    runtime?: SessionRuntime
    latestToolEvent?: ToolEvent
  },
): SessionView {
  const { record, hosts, localOwner, runtime, latestToolEvent } = opts
  const label = record.name && record.name.trim() !== '' ? record.name : record.id
  const isLocal = record.owner === localOwner
  const host = isLocal ? hosts.local : hosts.byOwnerId.get(record.owner)
  const hostOnline = isLocal ? hosts.local != null : host?.online === true
  return {
    key: sessionRefToKey(record.ref),
    ref: record.ref,
    id: record.id,
    ownerId: record.owner,
    displayName: record.name,
    label,
    createdAt: record.created_at,
    generation: record.generation,
    phase: record.phase,
    hidden: record.hidden === true,
    background: record.background === true,
    scheduleId: record.schedule_id,
    cwd: record.cwd || runtime?.current_path,
    shell: record.shell,
    agentType: record.agent_type,
    worktreeBranch: record.worktree_branch,
    isLocal,
    host,
    hostOnline,
    currentPath: runtime?.current_path,
    currentCommand: runtime?.current_command,
    promptPreview: runtime?.prompt_preview,
    lastActivity: runtime?.last_active,
    idleSeconds: (runtime as any)?.idle_seconds,
    totalBytes: (runtime as any)?.total_bytes,
    latestToolEvent,
  }
}

/** Derives the SessionPresentationAttrs sets SessionApp hands to Sidebar/Overview/SessionActionsMenu from a list of SessionViews. */
export function toPresentationAttrs(views: SessionView[]): SessionPresentationAttrs {
  const hidden = new Set<string>()
  const background = new Set<string>()
  const scheduleIDs = new Map<string, string>()
  for (const v of views) {
    if (v.hidden) hidden.add(v.key)
    if (v.background) background.add(v.key)
    if (v.scheduleId) scheduleIDs.set(v.key, v.scheduleId)
  }
  return { hidden, background, scheduleIDs }
}

const loudStatuses = new Set(['waiting', 'stuck', 'error'])

/**
 * Shell command patterns: commands that represent an idle shell prompt,
 * not active work. Used by isNonShellCommand to decide if currentCommand
 * should be considered active work.
 */
const shellCommands = new Set(['bash', 'sh', 'zsh', 'fish', 'ksh', 'tcsh', 'csh'])

function isNonShellCommand(cmd?: string): boolean {
  if (!cmd || !cmd.trim()) return false
  // Extract the base command name (first word, no args)
  const base = cmd.split(/\s+/)[0]
  const baseName = base.split('/').pop() || ''
  return !shellCommands.has(baseName)
}

function getIdleSeconds(lastActivity?: string, createdAt?: string): number | undefined {
  if (!lastActivity) return undefined
  const lastTime = new Date(lastActivity).getTime()
  if (Number.isNaN(lastTime)) return undefined
  // Compute idle time relative to lastActivity, not wall clock
  const now = Date.now()
  const idleMs = Math.max(0, now - lastTime)
  return Math.floor(idleMs / 1000)
}

/**
 * Pure display-state classifier implementing the canonical status priority:
 * 1. crashed (phase === 'crashed')
 * 2. needs_you (loud tool event)
 * 3. offline (remote host offline)
 * 4. starting (phase === 'pending' || phase === 'starting')
 * 5. working (active tool turn, activity idle <= 5 sec, or non-shell currentCommand)
 * 6. idle (fallback)
 */
export function sessionViewSignal(
  view: SessionView,
  events: ToolEvent[],
  inActiveTurn: boolean,
): SessionSignal {
  const loudEvent = events.find(e => loudStatuses.has(e.status))
  const tool = (loudEvent || events[0])?.tool

  // Priority 1: crashed
  if (view.phase === 'crashed') {
    return { state: 'crashed', loud: true, reason: 'crashed', tool }
  }

  // Priority 2: needs_you (loud tool event)
  if (loudEvent) {
    return { state: 'needs_you', loud: true, reason: loudEvent.status, tool }
  }

  // Priority 3: offline (remote host offline)
  if (!view.hostOnline) {
    return { state: 'offline', loud: false, tool }
  }

  // Priority 4: starting
  if (view.phase === 'pending' || view.phase === 'starting') {
    return { state: 'starting', loud: false, tool }
  }

  // Priority 5: working
  // Active in an agent tool turn OR activity idle <= 5 sec OR non-shell currentCommand
  if (inActiveTurn) {
    return { state: 'working', loud: false, tool }
  }
  const idleSeconds = getIdleSeconds(view.lastActivity, view.createdAt)
  if (idleSeconds !== undefined && idleSeconds <= 5) {
    return { state: 'working', loud: false, tool }
  }
  if (isNonShellCommand(view.currentCommand)) {
    return { state: 'working', loud: false, tool }
  }

  // Priority 6: idle
  return { state: 'idle', loud: false, tool }
}
