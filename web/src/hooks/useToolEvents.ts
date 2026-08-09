import { useState, useEffect, useCallback } from 'react'

export interface ToolEvent {
  tool: string
  status: 'active' | 'waiting' | 'completed' | 'error' | 'stuck'
  host?: string
  host_name?: string
  session: string
  window: number
  pane?: string
  message?: string
  timestamp: string
  auto_detected?: boolean
}

export function useToolEvents() {
  const [events, setEvents] = useState<ToolEvent[]>([])
  // Tracks sessions with an in-progress hook-based agent turn.
  // Keyed the same as sessionKey() in useSessions: "session" or "host/session".
  // Set on hook-based active events; cleared on completed.
  // Outlives individual pane events so the badge doesn't flicker "idle"
  // during the brief gaps between tool calls within a single turn.
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
        const turns: { host?: string; session: string }[] = await turnsRes.json() || []
        const keys = turns.map(t => (t.host ? `${t.host}/${t.session}` : t.session))
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
  }, [])

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
      window: evt.window,
      pane: evt.pane,
      message: evt.message,
      timestamp: evt.timestamp,
      auto_detected: evt.auto_detected,
    }

    // Session-level turn tracking (hook-based only — not auto-detected).
    // Both setEvents and setActiveSessions are batched into one render by React 18.
    const sk = toolEvt.host ? `${toolEvt.host}/${toolEvt.session}` : toolEvt.session
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
  }, [])

  // Get events for a specific session (accepts composite key: "host/name" or "name")
  const getSessionEvents = useCallback((key: string) => {
    const idx = key.indexOf('/')
    if (idx === -1) {
      // Local session — match events with no host
      return events.filter(e => e.session === key && !e.host)
    }
    const host = key.substring(0, idx)
    const name = key.substring(idx + 1)
    return events.filter(e => e.session === name && e.host === host)
  }, [events])

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

  return { events, handleEvent, getSessionEvents, isSessionInActiveTurn, dismissEvent, dismissAll, refresh }
}
