import { useEffect, useState } from 'react'

export type WikiHealth = {
  reachable?: boolean
  has_key?: boolean
  auth_ok?: boolean
  configured?: boolean
}

/**
 * Whether wiki-viewer can serve a file we point it at.
 *
 * configured is deliberately ignored: it reports whether wiki-viewer has its
 * own workspace root set up, which only matters when we do NOT supply a root.
 * Every file open supplies one, so a wiki-viewer with no workspace of its own
 * still serves our files fine.
 */
export function wikiCanServeFiles(health: WikiHealth): boolean {
  return !!health.reachable && !!health.has_key && !!health.auth_ok
}

/**
 * Cached wiki-viewer health, so opening a file can decide synchronously between
 * the panel and the legacy token tab.
 *
 * null means not known yet, and callers should stay optimistic: the panel opens
 * and shows its own diagnostic, which is more useful than silently routing
 * elsewhere for a reason the user never sees.
 *
 * Polled because wiki-viewer is a separate process the user starts and stops
 * independently. Without this the routing decision would be frozen at page load
 * and would only recover on a reload.
 */
export function useWikiHealth(enabled: boolean): boolean | null {
  const [usable, setUsable] = useState<boolean | null>(null)

  useEffect(() => {
    if (!enabled) return
    let cancelled = false
    const controller = new AbortController()

    const check = async () => {
      try {
        const res = await fetch('/api/wiki/health', { signal: controller.signal })
        if (!res.ok) throw new Error('health')
        const health: WikiHealth = await res.json()
        if (!cancelled) setUsable(wikiCanServeFiles(health))
      } catch {
        // Our own API failed, which says nothing about wiki-viewer. Back to
        // unknown rather than false, so we do not reroute on our own hiccup.
        if (!cancelled) setUsable(null)
      }
    }

    void check()
    const id = window.setInterval(check, 60_000)
    return () => {
      cancelled = true
      controller.abort()
      window.clearInterval(id)
    }
  }, [enabled])

  return usable
}
