import { useCallback, useEffect, useRef, useState } from 'react'

// useSessionAttrs is the frontend binding to the SERVER-AUTHORITATIVE shared
// session-attribute store (background / hidden bits, mesh-wide). It replaced
// the old localStorage + layout-sync path that caused parked/hidden sessions
// to reset on reload or phone revisit.
//
// Design contract:
//   - The SERVER owns the truth. There is no localStorage source of truth and
//     no global<->local key translation: keys here are exactly sessionKey()
//     ("<owner-fp>/<name>"), the same namespace the server stores.
//   - Mutations are single-key POSTs; the server stamps the timestamp, applies
//     per-key last-write-wins, persists, fans out to peers, and broadcasts a
//     `session-attrs-updated` event. We optimistically update local state and
//     reconcile from the server response.
//   - On any `session-attrs-updated` WS event (from our own write, another
//     tab, or a peer) we refetch the authoritative sets.

export type SessionAttrSets = {
  background: Set<string>
  hidden: Set<string>
  scheduleIDs: Map<string, string>
}

type WireSets = { background: string[]; hidden: string[]; schedule_ids?: Record<string, string> }

function toSets(w: WireSets | null | undefined): SessionAttrSets {
  return {
    background: new Set(w?.background ?? []),
    hidden: new Set(w?.hidden ?? []),
    scheduleIDs: new Map(Object.entries(w?.schedule_ids ?? {})),
  }
}

export function useSessionAttrs(authenticated: boolean) {
  const [sets, setSets] = useState<SessionAttrSets>(() => ({
    background: new Set(),
    hidden: new Set(),
    scheduleIDs: new Map(),
  }))
  const inFlightKeys = useRef<Set<string>>(new Set())

  const refresh = useCallback(async () => {
    try {
      const res = await fetch('/api/session-attrs')
      if (!res.ok) return
      const body: WireSets = await res.json()
      setSets(toSets(body))
    } catch {}
  }, [])

  useEffect(() => {
    if (!authenticated) return
    refresh()
  }, [authenticated, refresh])

  // setAttr POSTs the desired (background, hidden) state for one key. The
  // server is the writer; we optimistically reflect the change and reconcile
  // from the response. background/hidden default to the current value when
  // omitted, so callers can toggle one bit without disturbing the other.
  // Returns true iff the POST succeeded (res.ok), false on error.
  const setAttr = useCallback(
    async (key: string, next: { background?: boolean; hidden?: boolean }): Promise<boolean> => {
      const background = next.background ?? sets.background.has(key)
      const hidden = next.hidden ?? sets.hidden.has(key)
      // Optimistic local update.
      setSets(prev => {
        const bg = new Set(prev.background)
        const hd = new Set(prev.hidden)
        if (background) bg.add(key); else bg.delete(key)
        if (hidden) hd.add(key); else hd.delete(key)
        return { background: bg, hidden: hd, scheduleIDs: prev.scheduleIDs }
      })
      inFlightKeys.current.add(key)
      try {
        const res = await fetch('/api/session-attrs', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ key, background, hidden }),
        })
        if (res.ok) {
          const body: WireSets = await res.json()
          setSets(toSets(body))
          return true
        } else {
          // Non-2xx response: re-sync from server truth.
          refresh()
          return false
        }
      } catch {
        // Network error: re-sync from server truth.
        refresh()
        return false
      } finally {
        inFlightKeys.current.delete(key)
      }
    },
    [sets, refresh],
  )

  return { sets, setAttr, refresh }
}
