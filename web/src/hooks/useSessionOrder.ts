import { useState, useCallback, useMemo, useEffect } from 'react'
import type { SessionView } from '../state/session/viewModel'
import { sessionViewSignal, stateRank } from '../state/session/viewModel'

export type SessionOrder = {
  orderedKeys: string[]
  moveToTop(key: string): void
  moveUp(key: string): void
  moveDown(key: string): void
  reset(): void
}

const STORAGE_KEY = 'termyard:session-order-v1'

function readStoredOrder(key: string): string[] {
  try {
    const stored = localStorage.getItem(key)
    if (!stored) return []
    const parsed = JSON.parse(stored)
    // Discard non-string values and duplicates
    if (!Array.isArray(parsed)) return []
    const result: string[] = []
    const seen = new Set<string>()
    for (const item of parsed) {
      if (typeof item === 'string' && !seen.has(item)) {
        result.push(item)
        seen.add(item)
      }
    }
    return result
  } catch {
    return []
  }
}

function writeStoredOrder(key: string, values: string[]) {
  try {
    localStorage.setItem(key, JSON.stringify(values))
  } catch {
    // Silent fail if localStorage is unavailable
  }
}

/**
 * Browser-local session ordering via localStorage.
 *
 * Maintains a persistent list of session keys in preferred order.
 * New sessions (not in the preference list) are appended using the
 * default sort (state rank → newest → key).
 *
 * Pruning (removing keys with no matching session) happens only
 * after catalog bootstrap completes, preventing reconnect gaps
 * from erasing preferences.
 *
 * Returns the ordered SessionView[] and mutation functions.
 */
export function useSessionOrder(
  sessions: SessionView[],
  bootstrapped: boolean,
  getSessionEvents: (key: string) => any[],
  isSessionInActiveTurn: (key: string) => boolean,
): { ordered: SessionView[]; order: SessionOrder } {
  const [storedOrder, setStoredOrder] = useState<string[]>(() => readStoredOrder(STORAGE_KEY))

  // Define updateOrder early so it can be used in useEffect
  const updateOrder = useCallback((nextOrder: string[]) => {
    setStoredOrder(nextOrder)
    writeStoredOrder(STORAGE_KEY, nextOrder)
  }, [])

  // Default sort: state rank → newest → key
  // (matches Sidebar's orderedSessions logic)
  const defaultSorted = useMemo(() => {
    const signalOf = (session: SessionView) =>
      sessionViewSignal(session, getSessionEvents(session.key), isSessionInActiveTurn(session.key))

    return [...sessions].sort((a, b) => {
      const aState = signalOf(a).state
      const bState = signalOf(b).state
      if (aState !== bState) return stateRank[aState] - stateRank[bState]
      const at = a.createdAt || ''
      const bt = b.createdAt || ''
      if (at !== bt) return bt.localeCompare(at)
      return a.key.localeCompare(b.key)
    })
  }, [sessions, getSessionEvents, isSessionInActiveTurn])

  // Prune unknown keys only after bootstrap, preventing reconnect gaps
  // from erasing preferences. Use useEffect to avoid state updates in render.
  useEffect(() => {
    if (!bootstrapped) return
    const sessionsByKey = new Map(sessions.map(s => [s.key, s]))
    const validKeys = new Set(sessionsByKey.keys())
    const nextOrder = storedOrder.filter(key => validKeys.has(key))
    if (nextOrder.length !== storedOrder.length) {
      updateOrder(nextOrder)
    }
  }, [bootstrapped, sessions, storedOrder, updateOrder])

  // Ordered sessions: use stored order for known keys, append unknowns
  // in default sort order.
  const ordered = useMemo(() => {
    const result: SessionView[] = []
    const seen = new Set<string>()
    const sessionsByKey = new Map(sessions.map(s => [s.key, s]))

    // Add known ordered keys first (if they still exist)
    for (const key of storedOrder) {
      const session = sessionsByKey.get(key)
      if (session) {
        result.push(session)
        seen.add(key)
      }
    }

    // Append unknown sessions in default sort order
    for (const session of defaultSorted) {
      if (!seen.has(session.key)) {
        result.push(session)
        seen.add(session.key)
      }
    }

    return result
  }, [sessions, storedOrder, defaultSorted])

  const moveToTop = useCallback(
    (key: string) => {
      const idx = storedOrder.indexOf(key)
      if (idx === 0) return // Already at top
      // If not in list, add to top; if in list, move to top
      const next = idx < 0
        ? [key, ...storedOrder]
        : [key, ...storedOrder.slice(0, idx), ...storedOrder.slice(idx + 1)]
      updateOrder(next)
    },
    [storedOrder, updateOrder],
  )

  const moveUp = useCallback(
    (key: string) => {
      const idx = storedOrder.indexOf(key)
      if (idx <= 0) return // Already at top or not in list
      const next = [...storedOrder]
      ;[next[idx - 1], next[idx]] = [next[idx], next[idx - 1]]
      updateOrder(next)
    },
    [storedOrder, updateOrder],
  )

  const moveDown = useCallback(
    (key: string) => {
      const idx = storedOrder.indexOf(key)
      if (idx < 0 || idx >= storedOrder.length - 1) return // Not in list or at bottom
      const next = [...storedOrder]
      ;[next[idx], next[idx + 1]] = [next[idx + 1], next[idx]]
      updateOrder(next)
    },
    [storedOrder, updateOrder],
  )

  const reset = useCallback(() => {
    updateOrder([])
  }, [updateOrder])

  const order: SessionOrder = { orderedKeys: storedOrder, moveToTop, moveUp, moveDown, reset }

  return { ordered, order }
}
