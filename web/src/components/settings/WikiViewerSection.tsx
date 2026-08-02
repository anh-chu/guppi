import { useState, useEffect } from 'react'
import { usePreferences } from '../../hooks/usePreferences'
import { Row, Toggle } from './controls'

export interface WikiStatus {
  installed: boolean
  installing: boolean
  running: boolean
  version: string
  error: string
  default_root: string
}

export function WikiViewerSection() {
  const { prefs, updatePrefs } = usePreferences()
  const [status, setStatus] = useState<WikiStatus | null>(null)
  const [installing, setInstalling] = useState(false)

  useEffect(() => {
    let cancelled = false
    let count = 0
    let timer: number | undefined

    const poll = async () => {
      if (cancelled) return
      try {
        const res = await fetch('/api/wiki/status')
        if (res.ok) {
          const s: WikiStatus = await res.json()
          if (!cancelled) setStatus(s)
        }
      } catch { /* keep last known status */ }
      if (!cancelled && count < 30) {
        count++
        timer = window.setTimeout(poll, 1000)
      }
    }

    poll()
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [])

  // Reset local installing flag once the server reports it is no longer installing.
  useEffect(() => {
    if (status && !status.installing && installing) {
      setInstalling(false)
    }
  }, [status, installing])

  const doInstall = async () => {
    setInstalling(true)
    try {
      await fetch('/api/wiki/install', { method: 'POST' })
    } catch { /* status poll will pick up the result */ }
  }

  const active = installing || (status?.installing ?? false)
  const error = status?.error && !status?.installing ? status.error : null
  const version = status?.installed ? status.version : null

  return (
    <div className="flex flex-col gap-4">
      <Row
        label="File panel"
        description="Open file paths in a side panel. When off, they open in a new tab instead."
      >
        <Toggle
          checked={!prefs.wiki_disabled}
          onChange={v => updatePrefs({ wiki_disabled: !v })}
        />
      </Row>
      <Row
        label="File viewer"
        description={error || (active ? 'Installing wiki-viewer...' : version ? `wiki-viewer ${version}` : 'wiki-viewer is not installed')}
      >
        {active ? (
          <span className="inline-flex items-center gap-2 rounded-sm border border-warning/40 bg-warning/10 px-3 py-1.5 text-[13px] font-bold text-warning">
            <span className="h-2 w-2 animate-pulse rounded-full bg-warning" />
            Installing...
          </span>
        ) : version ? (
          <div className="flex items-center gap-2">
            <span className="font-mono text-mute text-xs">{version}</span>
            <button
              onClick={doInstall}
              className="rounded-sm border border-hairline px-2 py-1 text-[11px] font-bold text-mute hover:text-ink transition-colors"
            >
              Reinstall
            </button>
          </div>
        ) : (
          <button
            onClick={doInstall}
            className="rounded-sm border border-hairline bg-surface px-3 py-1.5 text-[13px] font-bold text-ink hover:bg-surface-elevated transition-all"
          >
            Install file viewer
          </button>
        )}
      </Row>
    </div>
  )
}
