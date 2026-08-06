/**
 * Canonical UI-facing session view model for the v2 (AppV2) frontend.
 *
 * SessionView is the display-oriented shape AppV2 derives directly from the
 * v2 catalog (state/v2/types.ts's LocalSessionRecord) and hands to
 * presentation logic. It intentionally has no `windows`/`panes` fields: v2
 * sessions are always single-pane daemon sessions, unlike the legacy
 * `Session` type (src/hooks/useSessions.ts) which AppV2 previously had to
 * fake a `windows: []` shape for just to satisfy shared components' prop
 * types.
 *
 * This module must not import anything from src/hooks/useSessions.ts or
 * src/state/workspaceReducer.ts (the legacy hook/reducer modules) -- it is
 * the extraction point the v2 frontend depends on instead. Structural
 * (not nominal) typing means callers can still pass values built from this
 * module into components typed against the legacy `Session`/`SessionAttrSets`
 * shapes, as long as the fields line up; see App.tsx's AppV2 for the adapter
 * that does that today.
 */

import type { LocalSessionRecord } from '../v2/types'
import { sessionRefToKey } from '../v2/paneTreeAdapter'
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

/** Canonical UI-facing session shape for AppV2. */
export interface SessionView {
  /** sessionRefToKey(record.ref): "owner/id", or bare "id" for a null/local owner. */
  key: string
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
}

/** Canonical hidden/background/schedule attribute sets, structurally identical to hooks/useSessionAttrs.ts's SessionAttrSets. */
export interface SessionPresentationAttrs {
  hidden: Set<string>
  background: Set<string>
  scheduleIDs: Map<string, string>
}

/** Builds a SessionView straight from a v2 catalog record -- no legacy Session shim involved. */
export function toSessionView(record: LocalSessionRecord): SessionView {
  const label = record.name && record.name.trim() !== '' ? record.name : record.id
  return {
    key: sessionRefToKey(record.ref),
    id: record.id,
    ownerId: record.owner,
    displayName: record.name,
    label,
    createdAt: record.created_at,
    generation: record.generation,
    hidden: record.hidden === true,
    background: record.background === true,
    scheduleId: undefined,
  }
}

/** Derives the SessionPresentationAttrs sets AppV2 hands to Sidebar/Overview/SessionActionsMenu from a list of SessionViews. */
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
 * Pure display-state classifier for a v2 session -- the v2-native
 * replacement for lib/sessionState.ts's sessionSignal(), which requires a
 * legacy `Session` (with a faked `windows: []`) to run. There is no
 * isSessionActive()/isToolSession() equivalent here: v2 sessions are always
 * single-pane daemon sessions, so "is a real process running in some pane"
 * cannot be derived from a windows/panes tree the way legacy sessions can.
 * `inActiveTurn`/`activity` are the only working signals available today --
 * this matches AppV2's actual prior behavior, since its faked `windows: []`
 * made isSessionActive() always return false anyway.
 *
 * `hostOnline` is optional and defaults to unknown (never reports offline)
 * until v2 exposes per-owner connectivity to this layer; passing `false`
 * reports 'offline' the same way legacy's `session.host_online === false`
 * check did.
 */
export function sessionViewSignal(
  events: ToolEvent[],
  activity: ActivitySnapshot | undefined,
  inActiveTurn: boolean,
  hostOnline?: boolean,
): SessionSignal {
  const loudEvent = events.find(e => loudStatuses.has(e.status))
  const tool = (loudEvent || events[0])?.tool

  if (loudEvent) {
    return { state: 'needs_you', loud: true, reason: loudEvent.status, tool }
  }
  if (hostOnline === false) {
    return { state: 'offline', loud: false, tool }
  }
  const working = inActiveTurn || (activity != null && activity.idle_seconds <= 5)
  if (working) {
    return { state: 'working', loud: false, tool }
  }
  return { state: 'idle', loud: false, tool }
}
