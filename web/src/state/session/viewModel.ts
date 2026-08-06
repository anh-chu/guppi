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

import type { LocalSessionRecord, SessionRef } from './types'
import { sessionRefToKey } from './paneTreeAdapter'
import type { Host } from '../../hooks/useHosts'
import type { ToolEvent } from '../../hooks/useToolEvents'
import type { ActivitySnapshot } from '../../hooks/useActivity'

export type SessionState = 'needs_you' | 'working' | 'idle' | 'offline'

export interface SessionSignal {
  state: SessionState
  loud: boolean
  reason?: string
  tool?: string
}

export const stateRank: Record<SessionState, number> = {
  needs_you: 0,
  working: 1,
  idle: 2,
  offline: 3,
}

/**
 * Host lookup table SessionApp builds once per hosts refresh (see
 * hooks/useHosts.ts) and threads into toSessionView so per-session host
 * resolution never re-scans the host list.
 */
export interface HostIndex {
  hosts: Host[]
  local?: Host
  byPeerId: Map<string, Host>
  byOwnerId: Map<string, Host>
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
  host: Host | undefined
  /**
   * Connectivity for this session's owner. Local host is online whenever a
   * local host record exists; a remote owner is online only when its own
   * host record says so. An unknown remote owner (no host record) is
   * offline -- never optimistically online.
   */
  hostOnline: boolean
}

/** Canonical hidden/background/schedule attribute sets SessionApp hands to Sidebar/Overview/SessionActionsMenu. */
export interface SessionPresentationAttrs {
  hidden: Set<string>
  background: Set<string>
  scheduleIDs: Map<string, string>
}

/** Builds a SessionView straight from a canonical catalog record and the current host table -- no legacy Session shim involved. */
export function toSessionView(
  record: LocalSessionRecord,
  hosts: HostIndex,
  localOwner: string | null,
): SessionView {
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
    hidden: record.hidden === true,
    background: record.background === true,
    scheduleId: record.schedule_id,
    cwd: record.cwd,
    shell: record.shell,
    agentType: record.agent_type,
    worktreeBranch: record.worktree_branch,
    isLocal,
    host,
    hostOnline,
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
 * Pure display-state classifier for a v2 session -- the canonical
 * replacement for lib/sessionState.ts's sessionSignal(), which requires a
 * legacy `Session` (with a faked `windows: []`) to run. There is no
 * isSessionActive()/isToolSession() equivalent here: canonical sessions are always
 * single-pane daemon sessions, so "is a real process running in some pane"
 * cannot be derived from a windows/panes tree the way legacy sessions can.
 * `inActiveTurn`/`activity` are the only working signals available beyond
 * connectivity.
 *
 * `view.hostOnline` (see toSessionView) is the sole source of connectivity here
 * -- callers can no longer pass an ad hoc/optional hostOnline flag and
 * accidentally skip it.
 */
export function sessionViewSignal(
  view: SessionView,
  events: ToolEvent[],
  activity: ActivitySnapshot | undefined,
  inActiveTurn: boolean,
): SessionSignal {
  const loudEvent = events.find(e => loudStatuses.has(e.status))
  const tool = (loudEvent || events[0])?.tool

  if (loudEvent) {
    return { state: 'needs_you', loud: true, reason: loudEvent.status, tool }
  }
  if (!view.hostOnline) {
    return { state: 'offline', loud: false, tool }
  }
  const working = inActiveTurn || (activity != null && activity.idle_seconds <= 5)
  if (working) {
    return { state: 'working', loud: false, tool }
  }
  return { state: 'idle', loud: false, tool }
}
