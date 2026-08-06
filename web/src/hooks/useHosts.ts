import { useState, useEffect, useCallback, useMemo } from 'react'
import type { HostIndex } from '../state/session/viewModel'

export interface Host {
  id: string
  /**
   * canonical catalog OwnerID for this host (pkg/peer.HostInfo.OwnerID on the wire).
   * A DIFFERENT string encoding than `id` (the peer transport fingerprint) --
   * see state.OwnerIDFromFingerprint. Use this, never `id`, wherever a value
   * is sent to the server as a v2 identity (e.g. target_owner on a v2 create,
   * or a v2-routed terminal attach's `host` param, which server-side now
   * resolves via ResolveHostParam).
   *
   * Genuinely absent (not just optional-by-caution) when the host has no
   * canonical catalog wired: pkg/peer.Manager.GetHosts marshals this field
   * with `omitempty`, and ownerIDForPeerLocked returns "" with ok=false for
   * a peer with no catalog -- see pkg/peer/manager.go and the
   * TestGetHosts_IncludesOwnerID boundary test in host_identity_test.go.
   * Callers must treat an empty/missing owner_id as "no v2 identity for this
   * host", not as a bug.
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

  const hostIndex = useMemo<HostIndex>(() => {
    const byPeerId = new Map<string, Host>()
    const byOwnerId = new Map<string, Host>()
    let local: Host | undefined
    for (const h of hosts) {
      byPeerId.set(h.id, h)
      if (h.owner_id) byOwnerId.set(h.owner_id, h)
      if (h.local) local = h
    }
    return { hosts, local, byPeerId, byOwnerId }
  }, [hosts])

  return { hosts, refresh, hostIndex }
}
