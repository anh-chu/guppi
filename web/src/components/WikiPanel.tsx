import { useEffect, useRef, useState } from 'react'

export type WikiPanelStatus = 'checking' | 'ready' | 'unconfigured' | 'offline'

interface WikiPanelProps {
  wikiUrl: string
  apiKey?: string
  filePath: string | null
  sessionCwd?: string
  onClose: () => void
}

function resolveFilePath(path: string, cwd?: string): string {
  if (path.startsWith('/')) return path
  if (path.startsWith('~/')) return path // wiki-viewer handles ~
  if ((path.startsWith('./') || path.startsWith('../')) && cwd) {
    // Simple resolution: join cwd + relative path, let wiki-viewer handle the rest
    return cwd.replace(/\/$/, '') + '/' + path.replace(/^\.?\//, '')
  }
  return path
}

export function WikiPanel({ wikiUrl, apiKey, filePath, sessionCwd, onClose }: WikiPanelProps) {
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const [status, setStatus] = useState<WikiPanelStatus>('checking')
  const [width, setWidth] = useState(480)
  const dragRef = useRef<{ startX: number; startWidth: number } | null>(null)
  // Health check on mount and when wikiUrl/apiKey changes
  useEffect(() => {
    setStatus('checking')
    const controller = new AbortController()
    const headers: Record<string, string> = {}
    if (apiKey) headers['Authorization'] = `Bearer ${apiKey}`
    fetch(`${wikiUrl}/api/system/root-status`, { signal: controller.signal, headers })
      .then(r => r.ok ? r.json() : Promise.reject(r.status))
      .then((data: { configured?: boolean }) => {
        setStatus(data.configured ? 'ready' : 'unconfigured')
      })
      .catch(() => setStatus('offline'))
    return () => controller.abort()
  }, [wikiUrl, apiKey])  // apiKey from props

  // Queue and send postMessage — handle the race where path is clicked before iframe loads
  const [iframeLoaded, setIframeLoaded] = useState(false)
  const pendingPathRef = useRef<string | null>(null)

  const sendOpenFile = (resolved: string) => {
    const win = iframeRef.current?.contentWindow
    if (!win) return
    win.postMessage({ type: 'open-file', path: resolved }, wikiUrl)
  }

  // When filePath changes: send immediately if loaded, otherwise queue
  useEffect(() => {
    if (!filePath) return
    const resolved = resolveFilePath(filePath, sessionCwd)
    if (iframeLoaded) {
      sendOpenFile(resolved)
    } else {
      pendingPathRef.current = resolved
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filePath, sessionCwd, wikiUrl])

  // On iframe load: flush any queued path
  const handleIframeLoad = () => {
    setIframeLoaded(true)
    if (pendingPathRef.current) {
      sendOpenFile(pendingPathRef.current)
      pendingPathRef.current = null
    }
  }

  // Drag-to-resize handle
  useEffect(() => {
    const onMove = (e: MouseEvent) => {
      const drag = dragRef.current
      if (!drag) return
      const delta = drag.startX - e.clientX
      setWidth(Math.max(320, Math.min(900, drag.startWidth + delta)))
    }
    const onUp = () => { dragRef.current = null }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
    return () => {
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }
  }, [])

  const resolvedPath = filePath ? resolveFilePath(filePath, sessionCwd) : null
  const apiKeyParam = apiKey ? `&api_key=${encodeURIComponent(apiKey)}` : ''
  const iframeSrc = resolvedPath
    ? `${wikiUrl}/?embed=1${apiKeyParam}&path=${encodeURIComponent(resolvedPath)}`
    : `${wikiUrl}/?embed=1${apiKeyParam}`

  return (
    <div
      className="flex flex-row h-full shrink-0 border-l border-hairline"
      style={{ width }}
    >
      {/* Drag handle */}
      <div
        className="w-1 cursor-col-resize bg-transparent hover:bg-primary/30 transition-colors shrink-0"
        onMouseDown={e => {
          e.preventDefault()
          dragRef.current = { startX: e.clientX, startWidth: width }
        }}
      />

      <div className="flex flex-col flex-1 min-w-0 bg-canvas">
        {/* Header */}
        <div className="flex items-center justify-between px-3 py-1.5 border-b border-hairline shrink-0">
          <span className="text-[11px] font-bold uppercase tracking-widest text-mute">
            {resolvedPath ? resolvedPath.split('/').pop() : 'Files'}
          </span>
          <div className="flex items-center gap-2">
            {resolvedPath && (
              <a
                href={`${wikiUrl}/?path=${encodeURIComponent(resolvedPath)}`}
                target="_blank"
                rel="noreferrer"
                title="Open in wiki-viewer"
                className="p-1 text-mute hover:text-primary transition-colors"
              >
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
                  <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><polyline points="15 3 21 3 21 9"/><line x1="10" y1="14" x2="21" y2="3"/>
                </svg>
              </a>
            )}
            <button
              onClick={onClose}
              title="Close panel"
              className="p-1 text-mute hover:text-primary transition-colors"
            >
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
                <line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>
              </svg>
            </button>
          </div>
        </div>

        {/* Body */}
        {status === 'offline' && (
          <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
              <circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/>
            </svg>
            <p className="text-[12px] text-mute">wiki-viewer is not running</p>
            <p className="text-[11px] text-mute/60">Start it with <code className="font-mono bg-surface px-1 py-0.5 rounded text-[10px]">npx wiki-viewer</code></p>
          </div>
        )}

        {status === 'unconfigured' && (
          <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
              <path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><polyline points="9 22 9 12 15 12 15 22"/>
            </svg>
            <p className="text-[12px] text-mute">wiki-viewer needs a root directory</p>
            <a href={wikiUrl} target="_blank" rel="noreferrer" className="text-[11px] text-primary hover:underline">
              Open wiki-viewer to configure →
            </a>
          </div>
        )}

        {status === 'checking' && (
          <div className="flex items-center justify-center flex-1 gap-2 text-mute/50">
            <span className="inline-block h-3 w-3 rounded-full border-2 border-current border-t-transparent animate-spin" />
            <span className="text-[11px]">Connecting…</span>
          </div>
        )}

        {status === 'ready' && (
          <iframe
            ref={iframeRef}
            src={iframeSrc}
            className="flex-1 w-full border-none min-h-0"
            title="wiki-viewer"
            onLoad={handleIframeLoad}
          />
        )}
      </div>
    </div>
  )
}
