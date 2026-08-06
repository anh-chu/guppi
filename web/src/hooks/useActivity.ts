import { useState, useCallback, useEffect } from 'react'
import type { HostIndex } from '../state/session/viewModel'

export interface ActivitySnapshot {
  /** Canonical identity, set once at ingestion by normalizeActivitySnapshot -- the same key space as SessionView.key / ToolEvent.key ("owner/sessionId", or bare sessionId for a local/unknown owner). */
  key: string
  // Raw host/session fields below are retained only for display; no
  // consumer should reconstruct identity from these -- use `key`.
  host?: string
  session: string
  idle_seconds: number
  total_bytes: number
}

/** Shape of a raw activity snapshot as broadcast by the server (WS "activity" message, or an entry of the /api/activity snapshot) -- before normalization assigns `key`. */
export interface RawActivitySnapshot {
  host?: string
  session: string
  idle_seconds: number
  total_bytes: number
}

// fingerprint (''=local) -> canonical OwnerID, via hostIndex.byPeerId. Only
// meaningful when hostIndex was supplied; otherwise every fingerprint maps
// to itself (identity). Mirrors the identical pattern in useToolEvents.ts.
function ownerIdFor(hostIndex: HostIndex | undefined, fingerprint: string): string {
  if (!hostIndex) return fingerprint
  if (fingerprint === '') return hostIndex.local?.owner_id || ''
  return hostIndex.byPeerId.get(fingerprint)?.owner_id || fingerprint
}

// Canonical key for an activity snapshot -- the same key space as
// SessionView.key / ToolEvent.key ("owner/session", or bare "session" for
// a null/local owner). `session` here is already the durable session ID
// (see pkg/... activity snapshot), unlike ToolEvent's mutable display
// label, so there is no session_id/session preference to make.
function buildKey(hostIndex: HostIndex | undefined, host: string | undefined, session: string): string {
  const h = host || ''
  if (hostIndex) {
    const owner = ownerIdFor(hostIndex, h)
    return owner ? `${owner}/${session}` : session
  }
  return h ? `${h}/${session}` : session
}

/**
 * Normalizes a raw activity snapshot (from the WS "activity" message or the
 * /api/activity snapshot) into a canonical ActivitySnapshot with `key` set
 * once, here, at ingestion -- the same key space as ToolEvent.key /
 * SessionView.key. This is the ONLY place identity is derived from raw
 * transport fields for activity.
 */
export function normalizeActivitySnapshot(raw: RawActivitySnapshot, hostIndex?: HostIndex): ActivitySnapshot {
  return {
    key: buildKey(hostIndex, raw.host, raw.session),
    host: raw.host,
    session: raw.session,
    idle_seconds: raw.idle_seconds,
    total_bytes: raw.total_bytes,
  }
}

// useActivity accepts the current HostIndex (useHosts' hostIndex) so
// normalizeActivitySnapshot can map a snapshot's peer fingerprint host to
// the canonical OwnerID that SessionView.key is built from. `hostIndex` is
// optional (omitted only in unit tests that don't need OwnerID
// normalization); when omitted, every fingerprint maps to itself instead
// of an OwnerID. Mirrors the identical pattern in useToolEvents.ts.
export function useActivity(hostIndex?: HostIndex) {
  const [activity, setActivity] = useState<Map<string, ActivitySnapshot>>(new Map())

  // Fetch initial state on mount
  useEffect(() => {
    async function fetchInitial() {
      try {
        const res = await fetch('/api/activity')
        if (res.ok) {
          const data: RawActivitySnapshot[] = await res.json()
          const map = new Map<string, ActivitySnapshot>()
          if (data) {
            for (const raw of data) {
              const snap = normalizeActivitySnapshot(raw, hostIndex)
              map.set(snap.key, snap)
            }
          }
          setActivity(map)
        }
      } catch {
        // ignore fetch errors on initial load
      }
    }
    fetchInitial()
  }, [hostIndex])

  // Called by the WS event handler when an activity event arrives
  const handleActivityEvent = useCallback((snapshots: RawActivitySnapshot[]) => {
    const map = new Map<string, ActivitySnapshot>()
    for (const raw of snapshots) {
      const snap = normalizeActivitySnapshot(raw, hostIndex)
      map.set(snap.key, snap)
    }
    setActivity(map)
  }, [hostIndex])

  const getSessionActivity = useCallback((key: string): ActivitySnapshot | undefined => {
    return activity.get(key)
  }, [activity])

  return { activity, getSessionActivity, handleActivityEvent }
}
