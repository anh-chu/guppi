import { useState, useCallback, useEffect } from 'react'

export interface ActivitySnapshot {
  host?: string
  session: string
  idle_seconds: number
  sparkline: number[]
  total_bytes: number
}

// Key for activity map: host/session or just session
function activityKey(snap: ActivitySnapshot): string {
  return snap.host ? `${snap.host}/${snap.session}` : snap.session
}

export function useActivity() {
  const [activity, setActivity] = useState<Map<string, ActivitySnapshot>>(new Map())

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
  }, [])

  // Called by the WS event handler when an activity event arrives
  const handleActivityEvent = useCallback((snapshots: ActivitySnapshot[]) => {
    const map = new Map<string, ActivitySnapshot>()
    for (const snap of snapshots) {
      map.set(activityKey(snap), snap)
    }
    setActivity(map)
  }, [])

  const getSessionActivity = useCallback((session: string): ActivitySnapshot | undefined => {
    return activity.get(session)
  }, [activity])

  return { activity, getSessionActivity, handleActivityEvent }
}
