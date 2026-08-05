import { useState, useCallback, useEffect } from 'react'
import { Host } from './useHosts'

export interface ActivitySnapshot {
  host?: string
  session: string
  idle_seconds: number
  total_bytes: number
}

// useActivity optionally accepts the current host table (useHosts' Host[])
// so callers whose session keys are built from the v2 OwnerID (AppV2's
// sessionKey(), which is always `${ownerId}/${sessionId}`, never the raw
// peer transport fingerprint) can match against it. Activity snapshots
// arrive keyed by `host` = peer fingerprint (empty = local) and `session`
// = the durable session ID -- a DIFFERENT identity encoding than AppV2's
// session keys. When `hosts` is omitted (AppLegacy, whose Session.host is
// itself the raw fingerprint), behavior is unchanged from before this
// normalization existed. Mirrors the identical pattern in useToolEvents.ts.
export function useActivity(hosts?: Host[]) {
  const [activity, setActivity] = useState<Map<string, ActivitySnapshot>>(new Map())

  // fingerprint (snap.host; '' means local) -> v2 OwnerID, via the current
  // host table. Only meaningful when `hosts` was supplied; otherwise every
  // fingerprint maps to itself (identity), preserving pre-existing
  // fingerprint-keyed behavior for AppLegacy.
  const ownerIdFor = useCallback((fingerprint: string): string => {
    if (!hosts) return fingerprint
    if (fingerprint === '') return hosts.find(h => h.local)?.owner_id || ''
    return hosts.find(h => h.id === fingerprint)?.owner_id || fingerprint
  }, [hosts])

  // Key for activity map. When `hosts` is supplied, the host component is
  // normalized to the OwnerID and the key is ALWAYS "owner/session" (never
  // bare), matching AppV2's sessionKey() -- which always includes the
  // OwnerID, even for local sessions. Without `hosts`, the original
  // AppLegacy-compatible "host ? host/session : session" shape is preserved
  // exactly.
  const activityKey = useCallback((snap: ActivitySnapshot): string => {
    const h = snap.host || ''
    if (hosts) {
      const owner = ownerIdFor(h)
      return owner ? `${owner}/${snap.session}` : snap.session
    }
    return h ? `${h}/${snap.session}` : snap.session
  }, [hosts, ownerIdFor])

  // Fetch initial state on mount
  useEffect(() => {
    async function fetchInitial() {
      try {
        const res = await fetch('/api/activity')
        if (res.ok) {
          const data: ActivitySnapshot[] = await res.json()
          const map = new Map<string, ActivitySnapshot>()
          if (data) {
            for (const snap of data) {
              map.set(activityKey(snap), snap)
            }
          }
          setActivity(map)
        }
      } catch {
        // ignore fetch errors on initial load
      }
    }
    fetchInitial()
  }, [activityKey])

  // Called by the WS event handler when an activity event arrives
  const handleActivityEvent = useCallback((snapshots: ActivitySnapshot[]) => {
    const map = new Map<string, ActivitySnapshot>()
    for (const snap of snapshots) {
      map.set(activityKey(snap), snap)
    }
    setActivity(map)
  }, [activityKey])

  const getSessionActivity = useCallback((session: string): ActivitySnapshot | undefined => {
    return activity.get(session)
  }, [activity])

  return { activity, getSessionActivity, handleActivityEvent }
}
