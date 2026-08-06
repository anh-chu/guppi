import { useState, useEffect, useCallback } from 'react'
import type { HostIndex } from '../state/session/viewModel'

export interface ToolEvent {
  /** Canonical identity, set once at ingestion by normalizeToolEvent -- the same key space as SessionView.key / sessionRefToKey ("owner/sessionId", or bare sessionId for a local/unknown owner). */
  key: string
  tool: string
  status: 'active' | 'waiting' | 'completed' | 'error' | 'stuck'
  // Raw host/session fields below are retained ONLY for display (host_name,
  // session label) and the dismiss/DELETE request body (the server still
  // identifies events by host/session/window/pane, not by `key`). No
  // consumer should reconstruct identity from these -- use `key`.
  host?: string
  host_name?: string
  session: string
  /**
   * Durable session identity (state.SessionID on the wire); stable across
   * renames, unlike `session` (a mutable display label). normalizeToolEvent
   * prefers this over `session` when building `key`.
   */
  session_id?: string
  window: number
  pane?: string
  message?: string
  timestamp: string
  auto_detected?: boolean
}

/** Shape of a raw tool event as broadcast by the server (WS "tool-event" message, or an entry of the /api/tool-events snapshot) -- before normalization assigns `key`. */
export interface RawToolEvent {
  tool: string
  status: ToolEvent['status']
  host?: string
  host_name?: string
  session: string
  session_id?: string
  window: number
  pane?: string
  message?: string
  timestamp: string
  auto_detected?: boolean
}

/** Shape of a raw active-turn record from /api/active-turns -- same identity fields as RawToolEvent, without tool/status/window. */
interface RawActiveTurn {
  host?: string
  session: string
  session_id?: string
}

// fingerprint (''=local) -> canonical OwnerID, via hostIndex.byPeerId. Only
// meaningful when hostIndex was supplied; otherwise every fingerprint maps
// to itself (identity) -- hostIndex is optional only for unit tests that
// don't need OwnerID normalization.
function ownerIdFor(hostIndex: HostIndex | undefined, fingerprint: string): string {
  if (!hostIndex) return fingerprint
  if (fingerprint === '') return hostIndex.local?.owner_id || ''
  return hostIndex.byPeerId.get(fingerprint)?.owner_id || fingerprint
}

// buildKey produces the canonical composite key used to correlate a tool
// event with a session -- the same key space as SessionView.key /
// sessionRefToKey ("owner/session", or bare "session" for a null/local
// owner). Preferring `session_id` (stable) over `session` (a mutable
// display label that changes on rename) avoids losing correlation across a
// rename. When `hostIndex` is supplied, the host component is normalized
// to the OwnerID via byPeerId; without it, the plain
// "host ? host/session : session" shape is preserved exactly (unit tests).
function buildKey(hostIndex: HostIndex | undefined, host: string | undefined, sessionIdentity: string): string {
  const h = host || ''
  if (hostIndex) {
    const owner = ownerIdFor(hostIndex, h)
    return owner ? `${owner}/${sessionIdentity}` : sessionIdentity
  }
  return h ? `${h}/${sessionIdentity}` : sessionIdentity
}

/**
 * Normalizes a raw tool event (from the WS "tool-event" message or the
 * /api/tool-events snapshot) into a canonical ToolEvent with `key` set
 * once, here, at ingestion. This is the ONLY place identity is derived from
 * raw transport fields -- every other consumer (TopBar, QuickSwitcher,
 * Overview, SessionApp's handleJumpToSession) must use `.key` directly.
 */
export function normalizeToolEvent(raw: RawToolEvent, hostIndex?: HostIndex): ToolEvent {
  const sessionIdentity = raw.session_id || raw.session
  return {
    key: buildKey(hostIndex, raw.host, sessionIdentity),
    tool: raw.tool,
    status: raw.status,
    host: raw.host,
    host_name: raw.host_name,
    session: raw.session,
    session_id: raw.session_id,
    window: raw.window,
    pane: raw.pane,
    message: raw.message,
    timestamp: raw.timestamp,
    auto_detected: raw.auto_detected,
  }
}

// useToolEvents accepts the current HostIndex (useHosts' hostIndex) so
// normalizeToolEvent can map an event's peer fingerprint host to the
// canonical OwnerID that SessionView.key is built from. `hostIndex` is
// optional (omitted only in unit tests that don't need OwnerID
// normalization); when omitted, every fingerprint maps to itself instead
// of an OwnerID.
export function useToolEvents(hostIndex?: HostIndex) {
  const [events, setEvents] = useState<ToolEvent[]>([])
  // Tracks sessions with an in-progress hook-based agent turn, keyed the
  // same as ToolEvent.key. Set on hook-based active events; cleared on
  // completed. Outlives individual pane events so the badge doesn't
  // flicker "idle" during the brief gaps between tool calls within a
  // single turn.
  const [activeSessions, setActiveSessions] = useState<Set<string>>(new Set())

  // Fetch initial state
  const refresh = useCallback(async () => {
    try {
      const [res, turnsRes] = await Promise.all([
        fetch('/api/tool-events'),
        fetch('/api/active-turns'),
      ])
      // Reconcile turn tracking against the server's authoritative set so a
      // dropped "completed" WebSocket frame can't leave the badge stuck on
      // "working". The server is reliable here: notify posts over HTTP.
      if (turnsRes.ok) {
        const turns: RawActiveTurn[] = await turnsRes.json() || []
        const keys = turns.map(t => buildKey(hostIndex, t.host, t.session_id || t.session))
        setActiveSessions(new Set(keys))
      }
      if (res.ok) {
        const raw: RawToolEvent[] = await res.json() || []
        const serverData: ToolEvent[] = raw.map(r => normalizeToolEvent(r, hostIndex))
        setEvents(prev => {
          // Preserve hook-based active events — the server never persists them,
          // so a full replace would clear "working" state mid-turn.
          // Auto-detected active events are NOT preserved: the detector now sends
          // a completed event when the process exits, which clears them properly.
          // Dedup/clear by canonical key plus pane coordinates -- not raw
          // display label -- so a rename between polls can't split one pane's
          // event into two.
          const samePane = (a: ToolEvent, b: ToolEvent) =>
            a.key === b.key && a.window === b.window && (a.pane || '') === (b.pane || '')
          const localActives = prev.filter(e =>
            e.status === 'active' && !e.auto_detected && !serverData.some(s => samePane(s, e))
          )
          return [...localActives, ...serverData]
        })
      }
    } catch (err) {
      console.error('Failed to fetch tool events:', err)
    }
  }, [hostIndex])

  useEffect(() => {
    refresh()
    // Periodic re-sync to catch missed WebSocket messages
    const interval = setInterval(refresh, 5000)
    return () => clearInterval(interval)
  }, [refresh])

  // Handle incoming WebSocket tool events
  const handleEvent = useCallback((evt: any) => {
    if (evt.type !== 'tool-event') return

    const toolEvt = normalizeToolEvent(evt, hostIndex)

    // Session-level turn tracking (hook-based only — not auto-detected).
    // Both setEvents and setActiveSessions are batched into one render by React 18.
    if (toolEvt.status === 'active' && !toolEvt.auto_detected) {
      setActiveSessions(prev => new Set([...prev, toolEvt.key]))
    } else if (toolEvt.status === 'completed') {
      setActiveSessions(prev => { const next = new Set(prev); next.delete(toolEvt.key); return next })
    }

    setEvents(prev => {
      // Remove existing event for the same canonical session + pane
      // coordinates (not raw display label -- a rename must not split one
      // pane's event into two).
      const filtered = prev.filter(
        e => !(e.key === toolEvt.key && e.window === toolEvt.window && (e.pane || '') === (toolEvt.pane || ''))
      )
      // Don't persist completed events — they clear the pane's existing event
      if (toolEvt.status === 'completed') {
        return filtered
      }
      // Keep all active events (hook-based and auto-detected).
      // The deduplication filter above ensures only the latest event per pane is kept.
      // Subsequent waiting/completed events will naturally replace the active one.
      return [...filtered, toolEvt]
    })
  }, [hostIndex])

  // Get events for a specific session, by its canonical key (SessionView.key).
  const getSessionEvents = useCallback((key: string) => {
    return events.filter(e => e.key === key)
  }, [events])

  // Check if a session has any "waiting"/"stuck" events (same key contract
  // as getSessionEvents).
  const sessionNeedsAttention = useCallback((key: string) => {
    const needsAttn = (e: ToolEvent) => e.status === 'waiting' || e.status === 'stuck'
    return events.some(e => e.key === key && needsAttn(e))
  }, [events])

  // Returns true if the session has an in-progress hook-based agent turn.
  // More stable than checking events directly — persists across the brief
  // gaps between tool calls where no active event is in-flight.
  const isSessionInActiveTurn = useCallback((key: string) => {
    return activeSessions.has(key)
  }, [activeSessions])

  // Dismiss a specific event (clear from server and local state). The
  // DELETE body still carries the raw host/session/window/pane fields --
  // the server identifies events by those, not by the canonical `key`.
  const dismissEvent = useCallback(async (evt: ToolEvent) => {
    try {
      await fetch('/api/tool-event', {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ host: evt.host || '', session: evt.session, window: evt.window, pane: evt.pane || '' }),
      })
    } catch (err) {
      console.error('Failed to dismiss event:', err)
    }
    setEvents(prev => prev.filter(
      e => !(e.key === evt.key && e.window === evt.window && (e.pane || '') === (evt.pane || ''))
    ))
  }, [])

  // Clear all events
  const dismissAll = useCallback(async () => {
    try {
      await fetch('/api/tool-events', { method: 'DELETE' })
    } catch (err) {
      console.error('Failed to clear events:', err)
    }
    setEvents([])
    setActiveSessions(new Set())
  }, [])

  return { events, handleEvent, getSessionEvents, sessionNeedsAttention, isSessionInActiveTurn, dismissEvent, dismissAll, refresh }
}
