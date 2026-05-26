import { useState, useEffect, useCallback } from 'react'

export type ForwardMode = 'proxy' | 'socat'

export interface PortForward {
  port: number
  label: string
  mode: ForwardMode
  external_port?: number
}

export function usePortForwards() {
  const [forwards, setForwards] = useState<PortForward[]>([])
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(async () => {
    try {
      const res = await fetch('/api/portforwards')
      if (res.ok) {
        const data = await res.json()
        setForwards(Array.isArray(data) ? data : [])
      }
    } catch {
      // silent
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    refresh()
  }, [refresh])

  const add = useCallback(async (port: number, label: string, mode: ForwardMode, externalPort?: number): Promise<string | null> => {
    try {
      const res = await fetch('/api/portforwards', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ port, label, mode, external_port: externalPort || 0 }),
      })
      if (res.ok) {
        const data = await res.json()
        setForwards(Array.isArray(data) ? data : [])
        return null
      }
      return await res.text()
    } catch (e) {
      return String(e)
    }
  }, [])

  const remove = useCallback(async (port: number) => {
    const res = await fetch(`/api/portforward/${port}`, { method: 'DELETE' })
    if (res.ok) {
      setForwards(prev => prev.filter(f => f.port !== port))
    }
  }, [])

  return { forwards, loading, add, remove, refresh }
}
