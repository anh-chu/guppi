import { useState, useEffect, useCallback } from 'react'
import { Host } from './useHosts'

export interface ToolEvent {
  tool: string
  status: 'active' | 'waiting' | 'completed' | 'error' | 'stuck'
  host?: string
  host_name?: string
  session: string
  /**
   * Durable session identity (state.SessionID on the wire); stable across
   * renames, unlike `session` (a mutable display label). Prefer this for
   * correlation whenever present; `session` remains for legacy events/tools
   * that don't send it.
   */
  session_id?: string
  window: number
  pane?: string
  message?: string
  timestamp: string
  auto_detected?: boolean
}

// useToolEvents accepts the current host table (useHosts' Host[]) so
// callers whose session keys are built from the canonical OwnerID
// (SessionApp's sessionKey(), which is always `${ownerId}/${sessionId}`,
// never the raw peer transport fingerprint) can match against it. Tool
// events arrive keyed by `host` = peer fingerprint (empty = local) and
// `session`/`session_id` = display label/stable id (see
// pkg/toolevents.Event) -- a DIFFERENT identity encoding than SessionApp's
// session keys. `hosts` is optional (omitted only in unit tests that don't
// need OwnerID normalization); when omitted, every fingerprint maps to
// itself instead of an OwnerID.
export function useToolEvents(hosts?: Host[]) {
  const [events, setEvents] = useState<ToolEvent[]>([])
  // Tracks sessions with an in-progress hook-based agent turn.
  // Keyed the same as sessionKey() in useSessions: "session" or
  // "host/session" (bare key form), or "ownerId/sessionId" (SessionApp, via
  // buildKey below). Set on hook-based active events; cleared on completed.
  // Outlives individual pane events so the badge doesn't flicker "idle"
  // during the brief gaps between tool calls within a single turn.
  const [activeSessions, setActiveSessions] = useState<Set<string>>(new Set())

  // fingerprint (event.host; '' means local) -> canonical OwnerID, via the
  // current host table. Only meaningful when `hosts` was supplied; otherwise
  // every fingerprint maps to itself (identity).
  const ownerIdFor = useCallback((fingerprint: string): string => {
    if (!hosts) return fingerprint
    if (fingerprint === '') return hosts.find(h => h.local)?.owner_id || ''
    return hosts.find(h => h.id === fingerprint)?.owner_id || fingerprint
  }, [hosts])

  // buildKey produces the composite key used to correlate a tool event with
  // a session. Preferring `session_id` (stable) over `session` (a mutable
  // display label that changes on rename) avoids losing correlation across
  // a rename. When `hosts` is supplied, the host component is normalized to
  // the OwnerID and the key is ALWAYS "owner/session" (never bare), matching
  // SessionApp's sessionKey() -- which always includes the OwnerID, even for
  // local sessions. Without `hosts`, the plain
  // "host ? host/session : session" shape is preserved exactly.
  const buildKey = useCallback((host: string | undefined, sessionIdentity: string): string => {
    const h = host || ''
    if (hosts) {
      const owner = ownerIdFor(h)
      return owner ? `${owner}/${sessionIdentity}` : sessionIdentity
    }
    return h ? `${h}/${sessionIdentity}` : sessionIdentity
  }, [hosts, ownerIdFor])

  const eventKey = useCallback((e: ToolEvent): string => buildKey(e.host, e.session_id || e.session), [buildKey])

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
        const turns: { host?: string; session: string; session_id?: string }[] = await turnsRes.json() || []
        const keys = turns.map(t => buildKey(t.host, t.session_id || t.session))
        setActiveSessions(new Set(keys))
      }
      if (res.ok) {
        const serverData: ToolEvent[] = await res.json() || []
        setEvents(prev => {
          // Preserve hook-based active events — the server never persists them,
          // so a full replace would clear "working" state mid-turn.
          // Auto-detected active events are NOT preserved: the detector now sends
          // a completed event when the process exits, which clears them properly.
          const samePane = (a: ToolEvent, b: ToolEvent) =>
            a.session === b.session && a.window === b.window &&
            (a.pane || '') === (b.pane || '') && (a.host || '') === (b.host || '')
          const localActives = prev.filter(e =>
            e.status === 'active' && !e.auto_detected && !serverData.some(s => samePane(s, e))
          )
          return [...localActives, ...serverData]
        })
      }
    } catch (err) {
      console.error('Failed to fetch tool events:', err)
    }
  }, [buildKey])

  useEffect(() => {
    refresh()
    // Periodic re-sync to catch missed WebSocket messages
    const interval = setInterval(refresh, 5000)
    return () => clearInterval(interval)
  }, [refresh])

  // Handle incoming WebSocket tool events
  const handleEvent = useCallback((evt: any) => {
    if (evt.type !== 'tool-event') return

    const toolEvt: ToolEvent = {
      tool: evt.tool,
      status: evt.status,
      host: evt.host,
      host_name: evt.host_name,
      session: evt.session,
      session_id: evt.session_id,
      window: evt.window,
      pane: evt.pane,
      message: evt.message,
      timestamp: evt.timestamp,
      auto_detected: evt.auto_detected,
    }

    // Session-level turn tracking (hook-based only — not auto-detected).
    // Both setEvents and setActiveSessions are batched into one render by React 18.
    const sk = eventKey(toolEvt)
    if (toolEvt.status === 'active' && !toolEvt.auto_detected) {
      setActiveSessions(prev => new Set([...prev, sk]))
    } else if (toolEvt.status === 'completed') {
      setActiveSessions(prev => { const next = new Set(prev); next.delete(sk); return next })
    }

    setEvents(prev => {
      // Remove existing event for same host/session/window/pane
      // Normalize pane to handle undefined vs empty string
      const filtered = prev.filter(
        e => !(e.session === toolEvt.session && e.window === toolEvt.window && (e.pane || '') === (toolEvt.pane || '') && (e.host || '') === (toolEvt.host || ''))
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
  }, [eventKey])

  // Get events for a specific session. Accepts either the plain
  // composite key ("host/name" or bare "name") or, when `hosts` was
  // supplied, SessionApp's "ownerId/sessionId" key -- eventKey normalizes each
  // tracked event the same way, via `session_id` (stable) over `session`
  // (mutable display label) and, for SessionApp, the fingerprint->OwnerID host
  // mapping, so both sides of the comparison use an identical encoding.
  const getSessionEvents = useCallback((key: string) => {
    return events.filter(e => eventKey(e) === key)
  }, [events, eventKey])

  // Check if a session has any "waiting"/"stuck" events (same key contract
  // as getSessionEvents).
  const sessionNeedsAttention = useCallback((key: string) => {
    const needsAttn = (e: ToolEvent) => e.status === 'waiting' || e.status === 'stuck'
    return events.some(e => eventKey(e) === key && needsAttn(e))
  }, [events, eventKey])

  // Returns true if the session has an in-progress hook-based agent turn.
  // More stable than checking events directly — persists across the brief
  // gaps between tool calls where no active event is in-flight.
  const isSessionInActiveTurn = useCallback((key: string) => {
    return activeSessions.has(key)
  }, [activeSessions])

  // Dismiss a specific event (clear from server and local state)
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
      e => !(e.session === evt.session && e.window === evt.window && (e.pane || '') === (evt.pane || '') && (e.host || '') === (evt.host || ''))
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
