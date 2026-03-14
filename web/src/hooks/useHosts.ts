import { useState, useEffect, useCallback } from 'react'

export interface Host {
  id: string
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
