import { useEffect, useRef, useState } from 'react'

export type WikiPanelStatus =
  | 'checking'
  | 'ready'
  | 'offline'
  | 'no-key'
  | 'bad-key'
  | 'bad-root'
  | 'unconfigured'

interface WikiPanelProps {
  wikiUrl: string
  filePath: string | null
  /** Session cwd. Sent as wiki-viewer's ephemeral root so the file resolves
   *  regardless of which workspace wiki-viewer has active. */
  sessionCwd?: string
  onClose: () => void
}

function resolveFilePath(path: string, cwd?: string): string {
  if (path.startsWith('/')) return path
  if (path.startsWith('~/')) return path
  if ((path.startsWith('./') || path.startsWith('../')) && cwd) {
    return cwd.replace(/\/$/, '') + '/' + path.replace(/^\.?\//, '')
  }
  return path
}

export function WikiPanel({ wikiUrl, filePath, sessionCwd, onClose }: WikiPanelProps) {
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const [status, setStatus] = useState<WikiPanelStatus>('checking')
  const [embedSrc, setEmbedSrc] = useState<string | null>(null)
  const [width, setWidth] = useState(480)
  const dragRef = useRef<{ startX: number; startWidth: number } | null>(null)

  const resolvedPath = filePath ? resolveFilePath(filePath, sessionCwd) : null

  // The root we hand wiki-viewer. Normally the requesting session's cwd; every
  // call site supplies one now, so the fallback only fires for a session with no
  // path data at all. Falling back to the file's own directory matters for more
  // than tree scope: wiki-viewer grants a non-loopback
  // parent postMessage trust only when it supplied ?root= (key-derived trust).
  // With no root, open-file is rejected from any non-loopback origin — and
  // termyard's documented deployment is behind Tailscale or a reverse proxy.
  // A narrow root is a small tree; no root is a panel where only the first
  // file of each session works.
  const effectiveRoot =
    sessionCwd ??
    (resolvedPath && resolvedPath.startsWith('/') && resolvedPath.lastIndexOf('/') > 0
      ? resolvedPath.slice(0, resolvedPath.lastIndexOf('/'))
      : undefined)

  // Latest resolved path, read by the embed-url effect without making it a
  // dependency. Changing the file must NOT rebuild the iframe URL — that would
  // reload the iframe on every click. Subsequent files go over postMessage.
  const resolvedPathRef = useRef<string | null>(resolvedPath)
  resolvedPathRef.current = resolvedPath

  // The file baked into the current embed URL, consumed once on first load.
  const bakedPathRef = useRef<string | null>(null)

  // Resolve status and build the embed URL. Keyed on the root only: the root is
  // what determines which wiki-viewer instance scope we need.
  useEffect(() => {
    let cancelled = false
    const controller = new AbortController()
    setStatus('checking')
    setEmbedSrc(null)

    ;(async () => {
      try {
        const hres = await fetch('/api/wiki/health', { signal: controller.signal })
        if (!hres.ok) throw new Error('health')
        const health: {
          reachable?: boolean
          has_key?: boolean
          auth_ok?: boolean
          configured?: boolean
        } = await hres.json()
        if (cancelled) return

        if (!health.reachable) { setStatus('offline'); return }
        if (!health.has_key) { setStatus('no-key'); return }
        if (!health.auth_ok) { setStatus('bad-key'); return }
        // wiki-viewer's own root config only matters when we are NOT supplying a
        // root. With a root, its workspace state is irrelevant by design.
        if (!effectiveRoot && !health.configured) { setStatus('unconfigured'); return }

        const params = new URLSearchParams()
        if (effectiveRoot) params.set('root', effectiveRoot)
        if (resolvedPathRef.current) params.set('file', resolvedPathRef.current)

        const eres = await fetch('/api/wiki/embed-url?' + params.toString(), {
          signal: controller.signal,
        })
        if (cancelled) return

        if (!eres.ok) {
          const body: { error?: string } = await eres.json().catch(() => ({}))
          switch (body.error) {
            case 'root_requires_api_key': setStatus('no-key'); break
            case 'key_rejected': setStatus('bad-key'); break
            case 'root_not_found':
            case 'root_not_a_directory':
            // Any other rejection of the root from wiki-viewer, passed through
            // verbatim so a new code on their side degrades to the right shape
            // rather than the wrong one.
            case 'root_rejected': setStatus('bad-root'); break
            case 'wiki_unreachable': setStatus('offline'); break
            default: setStatus('unconfigured')
          }
          return
        }

        const { url } = (await eres.json()) as { url: string }
        if (cancelled) return
        // wiki-viewer resolves file= during the initial load, so the first paint
        // already shows this file. Remember it so we don't immediately postMessage
        // a navigation to the file that is already open.
        bakedPathRef.current = resolvedPathRef.current
        // Deliberately not logged: this URL carries the API key.
        setEmbedSrc(url)
        setStatus('ready')
      } catch {
        if (!cancelled) setStatus('offline')
      }
    })()

    return () => { cancelled = true; controller.abort() }
  }, [effectiveRoot])

  // "Open in wiki-viewer" (new tab) must reuse the server-built URL, because that
  // is the only thing carrying ?root= and the API key — the frontend cannot
  // rebuild either, since the key is masked in the preferences GET.
  //
  // This link used to hand-build `${wikiUrl}/?file=<abs path>`, which dropped
  // both. wiki-viewer then fell back to the most-recent registered workspace,
  // could not relativize an absolute path outside it, and rendered its empty
  // "select a file to view or edit" screen. Unauthenticated it 307s to /signin.
  //
  // embed=1 is deliberately KEPT. wiki-viewer's middleware validates ?api_key=
  // only inside `if (isEmbed)`, so dropping embed=1 makes the key (and the
  // embed cookie) ignored and the navigation 307s to /signin. Verified: with
  // embed=1 it is 200, without it is 307 even with a valid key and cookie.
  // That coupling is intentional on their side — the api-key identity is
  // synthetic and honoring it outside embed mode would expose Settings, which
  // displays the API key itself.
  //
  // chrome=1 opts the sidebar back in, which embed mode otherwise hides. It is
  // what makes this a genuine "open the full app" link rather than a bare file
  // view in a full tab. Auth stays gated on embed=1; only the chrome moved.
  //
  // Only file= is re-pointed, at the currently shown path: embedSrc carries the
  // file baked in at load time, which goes stale after postMessage navigation.
  const externalHref = (() => {
    if (!embedSrc) return null
    try {
      const u = new URL(embedSrc, window.location.origin)
      u.searchParams.set('chrome', '1')
      if (resolvedPath) u.searchParams.set('file', resolvedPath)
      return u.toString()
    } catch {
      return null
    }
  })()

  // Send the open-file message, queueing if the iframe has not loaded yet.
  const [iframeLoaded, setIframeLoaded] = useState(false)
  const pendingPathRef = useRef<string | null>(null)

  const sendOpenFile = (resolved: string) => {
    const win = iframeRef.current?.contentWindow
    if (!win) return
    win.postMessage({ type: 'open-file', path: resolved }, wikiUrl)
  }

  useEffect(() => {
    if (!resolvedPath || status !== 'ready') return
    if (iframeLoaded) sendOpenFile(resolvedPath)
    else pendingPathRef.current = resolvedPath
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [resolvedPath, status, iframeLoaded, wikiUrl])

  const handleIframeLoad = () => {
    setIframeLoaded(true)
    const pending = pendingPathRef.current
    pendingPathRef.current = null
    // Consume the baked path exactly once: a later click on the same file must
    // still send, since by then the user has navigated away from it.
    const baked = bakedPathRef.current
    bakedPathRef.current = null
    if (pending && pending !== baked) sendOpenFile(pending)
  }

  // A new embed URL means a fresh document; the load gate must reset with it.
  useEffect(() => { setIframeLoaded(false) }, [embedSrc])

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

  return (
    <div className="flex flex-row h-full shrink-0 border-l border-hairline" style={{ width }}>
      <div
        className="w-1 cursor-col-resize bg-transparent hover:bg-primary/30 transition-colors shrink-0"
        onMouseDown={e => {
          e.preventDefault()
          dragRef.current = { startX: e.clientX, startWidth: width }
        }}
      />

      <div className="flex flex-col flex-1 min-w-0 bg-canvas">
        <div className="flex items-center justify-between px-3 py-1.5 border-b border-hairline shrink-0">
          <span className="text-[11px] font-bold uppercase tracking-widest text-mute truncate">
            {resolvedPath ? resolvedPath.split('/').pop() : 'Files'}
          </span>
          <div className="flex items-center gap-2 shrink-0">
            {externalHref && (
              <a
                href={externalHref}
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

        {status === 'offline' && (
          <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
              <circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/>
            </svg>
            <p className="text-[12px] text-mute">wiki-viewer is not running</p>
            <p className="text-[11px] text-mute/60">
              Start it with <code className="font-mono bg-surface px-1 py-0.5 rounded text-[10px]">npx wiki-viewer</code>
            </p>
          </div>
        )}

        {status === 'no-key' && (
          <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
              <path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4"/>
            </svg>
            <p className="text-[12px] text-mute">API key required</p>
            <p className="text-[11px] text-mute/60">
              Viewing files from this session needs wiki-viewer's API key. Copy it from
              wiki-viewer's Settings → API Access, then paste it into Settings → Integrations.
            </p>
            <a href={`${wikiUrl}/settings`} target="_blank" rel="noreferrer" className="text-[11px] text-primary hover:underline">
              Open wiki-viewer Settings →
            </a>
          </div>
        )}

        {status === 'bad-key' && (
          <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
              <circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/>
            </svg>
            <p className="text-[12px] text-mute">API key rejected</p>
            <p className="text-[11px] text-mute/60">
              wiki-viewer did not accept the stored key. It may have been rotated — copy the
              current one and update Settings → Integrations.
            </p>
          </div>
        )}

        {status === 'bad-root' && (
          <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
              <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><line x1="12" y1="11" x2="12" y2="14"/>
            </svg>
            <p className="text-[12px] text-mute">Session directory unavailable</p>
            <p className="text-[11px] text-mute/60 font-mono break-all">{effectiveRoot}</p>
            <p className="text-[11px] text-mute/60">It no longer exists, or is not a directory.</p>
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

        {status === 'ready' && embedSrc && (
          <iframe
            ref={iframeRef}
            src={embedSrc}
            className="flex-1 w-full border-none min-h-0"
            title="wiki-viewer"
            referrerPolicy="no-referrer"
            onLoad={handleIframeLoad}
          />
        )}
      </div>
    </div>
  )
}
