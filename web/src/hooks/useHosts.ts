import { useState, useEffect, useCallback } from 'react'

export interface Host {
  id: string
  /**
   * canonical catalog OwnerID for this host (pkg/peer.HostInfo.OwnerID on the wire).
   * A DIFFERENT string encoding than `id` (the peer transport fingerprint) --
   * see state.OwnerIDFromFingerprint. Use this, never `id`, wherever a value
   * is sent to the server as a v2 identity (e.g. target_owner on a v2 create,
   * or a v2-routed terminal attach's `host` param, which server-side now
   * resolves via ResolveHostParam). Empty when the host has no canonical catalog
   * (legacy-only mode).
   */
  owner_id?: string
  name: string
  version?: string
  local?: boolean
  online: boolean
  sessions: any[]
  stats?: Record<string, any>
  last_seen: string
}

export function useHosts() {
  const [hosts, setHosts] = useState<Host[]>([])

  const refresh = useCallback(async () => {
    try {
      const res = await fetch('/api/hosts')
      if (res.ok) {
        const data = await res.json()
        setHosts(data || [])
      }
    } catch (err) {
      console.error('Failed to fetch hosts:', err)
    }
  }, [])

  useEffect(() => {
    refresh()
    const interval = setInterval(refresh, 30000)
    return () => clearInterval(interval)
  }, [refresh])

  return { hosts, refresh }
}
