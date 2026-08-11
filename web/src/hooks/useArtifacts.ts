import { useCallback, useEffect, useState } from 'react'

export interface FileArtifact {
  path: string
  display_path?: string
  name: string
  size?: number
  mod_time?: string
  tool?: string
  source: string
  first_seen: string
}

type ArtifactEvent = {
  type: 'tool-event' | 'artifacts'
  host?: string
  session?: string
  artifacts?: FileArtifact[]
}

const MAX_ARTIFACTS = 100
const ARTIFACT_EVENT = 'termyard:artifacts'

function mergeArtifacts(base: FileArtifact[], incoming: FileArtifact[]): FileArtifact[] {
  const merged = base.map(a => ({ ...a }))
  for (const art of incoming) {
    if (!art || !art.path) continue
    const idx = merged.findIndex(a => a.path === art.path)
    if (idx >= 0) {
      merged[idx] = { ...merged[idx], ...art }
      continue
    }
    merged.push({ ...art })
    if (merged.length > MAX_ARTIFACTS) {
      merged.splice(0, merged.length - MAX_ARTIFACTS)
    }
  }
  return merged
}

function sameSession(aHost: string | undefined, aSession: string | undefined, bHost: string | undefined, bSession: string | undefined): boolean {
  const hostMatches = !(aHost || bHost) || (aHost || '') === (bHost || '')
  return hostMatches && (aSession || '') === (bSession || '')
}

export function useArtifacts(session: string, host?: string) {
  const [artifacts, setArtifacts] = useState<FileArtifact[]>([])

  const refresh = useCallback(async (sessionArg = session, hostArg = host) => {
    if (!sessionArg) {
      setArtifacts([])
      return
    }
    try {
      const qs = new URLSearchParams({ session: sessionArg })
      if (hostArg) qs.set('host', hostArg)
      const res = await fetch(`/api/artifacts?${qs.toString()}`)
      if (!res.ok) return
      const data: { artifacts?: FileArtifact[] } = await res.json()
      setArtifacts(data.artifacts || [])
    } catch (err) {
      console.error('Failed to fetch artifacts:', err)
    }
  }, [session, host])

  const handleEvent = useCallback((evt: ArtifactEvent | null | undefined) => {
    if (!evt || (evt.type !== 'tool-event' && evt.type !== 'artifacts')) return
    const incoming = evt.artifacts
    if (!Array.isArray(incoming) || incoming.length === 0) return
    if (!sameSession(evt.host, evt.session, host, session)) return
    setArtifacts(prev => mergeArtifacts(prev, incoming))
  }, [session, host])

  useEffect(() => {
    setArtifacts([])
    void refresh()
  }, [refresh])

  useEffect(() => {
    const onArtifacts = (e: Event) => {
      handleEvent((e as CustomEvent).detail)
    }
    window.addEventListener(ARTIFACT_EVENT, onArtifacts)
    return () => window.removeEventListener(ARTIFACT_EVENT, onArtifacts)
  }, [handleEvent])

  return { artifacts, refresh, handleEvent }
}
