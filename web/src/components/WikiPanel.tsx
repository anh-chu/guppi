import { useEffect, useRef, useState } from 'react'

import { basename } from '../lib/fileSource'
import { isInside, pickEmbedRoot, resolveFilePath } from '../lib/wikiRoot'
import { FilePane } from './FilePane'

// Re-exported because these were the panel's public surface before the logic
// moved to lib/wikiRoot.ts, and callers should not have to care that it did.
export { pickEmbedRoot, resolveFilePath }

/** Status of the wiki-viewer BROWSE surface only. Never a file's status. */
export type WikiPanelStatus =
  | 'checking'
  | 'ready'
  | 'offline'
  | 'no-key'
  | 'bad-key'
  | 'bad-root'
  | 'unconfigured'

/** Which of the panel's two independent surfaces is in front. */
type PanelView = 'file' | 'browse'

interface WikiPanelProps {
  wikiUrl: string
  filePath: string | null
  /**
   * Bumped on every open request, even when the path is unchanged. Without it a
   * second open of the same file is a no-op, because the load effect keys on the
   * resolved path. That is easy to hit now that one panel serves every pane:
   * open A from one pane, then A from another, and nothing would happen.
   */
  openNonce?: number
  /** Session cwd. Used to resolve relative paths and to root the browse tree. */
  sessionCwd?: string
  /** Peer host ID. Threaded to the grant call so remote files relay over the mesh. */
  hostId?: string
  onClose: () => void
}

/** What the browse iframe was mounted with. null means it was never asked for. */
interface BrowseMount {
  root?: string
  /** Baked into the initial URL, so the first paint already shows this file. */
  file: string | null
}

/**
 * The side panel. Two surfaces that share only a width and a close button:
 *
 *  - FILE: rendered natively, in termyard's own origin, by @wiki-viewer/viewer.
 *    Because it is same-origin, <img src="/file?token=..."> carries termyard's
 *    session cookie and binary files just work. Nothing about it depends on
 *    wiki-viewer running, or on the file being on this machine.
 *
 *  - BROWSE: the wiki-viewer iframe with ?root=, for walking and EDITING the
 *    real wiki. This is the one thing the iframe is still good for, and it needs
 *    wiki-viewer's API key, so it keeps its own health checks and status cards.
 *
 * They are kept strictly apart. Opening a file no longer touches the iframe, and
 * a file that fails to load reports inside FilePane. The previous shape put
 * per-file errors into the panel's status, the status decided whether the iframe
 * rendered, and so one bad file tore down the surface every later file needed.
 */
export function WikiPanel({ wikiUrl, filePath, openNonce, sessionCwd, hostId, onClose }: WikiPanelProps) {
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const [status, setStatus] = useState<WikiPanelStatus>('checking')
  const [embedSrc, setEmbedSrc] = useState<string | null>(null)
  const [width, setWidth] = useState(480)
  const dragRef = useRef<{ startX: number; startWidth: number } | null>(null)

  const resolvedPath = filePath ? resolveFilePath(filePath, sessionCwd) : null

  const [view, setView] = useState<PanelView>(filePath ? 'file' : 'browse')

  // Bumped by Reload. The only way to replace a grant that died of its 5 minute
  // TTL while the panel sat open.
  const [reloadSeq, setReloadSeq] = useState(0)

  // Any open request brings the file surface forward, including a repeat of the
  // same path, which is why the nonce is a dependency.
  useEffect(() => {
    if (filePath) setView('file')
  }, [filePath, openNonce])

  // The iframe is created the first time browse is asked for and then kept
  // mounted while a file is showing, so coming back does not reload wiki-viewer
  // and lose unsaved edits. Mounting it lazily also means a session that only
  // ever opens files never loads wiki-viewer at all.
  const [browse, setBrowse] = useState<BrowseMount | null>(() =>
    filePath ? null : { root: pickEmbedRoot(null, sessionCwd), file: null },
  )

  // What the iframe currently shows, so a Browse click for the same file does
  // not re-navigate it.
  const shownBrowseFileRef = useRef<string | null>(null)

  // Latest resolved path, read by the embed-url effect without becoming one of
  // its dependencies: changing the file must never rebuild the iframe URL.
  const resolvedPathRef = useRef<string | null>(resolvedPath)
  resolvedPathRef.current = resolvedPath

  // The file baked into the current embed URL, consumed once on first load.
  const bakedPathRef = useRef<string | null>(null)

  // The origin the embed URL resolves to, for postMessage's targetOrigin.
  const wikiOriginRef = useRef<string>(wikiUrl)

  const pendingPathRef = useRef<string | null>(null)
  const [iframeLoaded, setIframeLoaded] = useState(false)

  const sendOpenFile = (resolved: string) => {
    const win = iframeRef.current?.contentWindow
    if (!win) return
    win.postMessage({ type: 'open-file', path: resolved }, wikiOriginRef.current)
    shownBrowseFileRef.current = resolved
  }

  /**
   * Show the browse surface, pointed at the current file when there is one.
   *
   * This is the only path that navigates the iframe, and it only runs from an
   * explicit click. That is deliberate: a reload here is something the user
   * asked for, whereas a reload triggered by opening a file in the other surface
   * would silently discard whatever they were editing.
   */
  const showBrowse = () => {
    const target = resolvedPathRef.current
    setView('browse')
    if (!browse) {
      setBrowse({ root: pickEmbedRoot(target, sessionCwd), file: target })
      return
    }
    if (!target || target === shownBrowseFileRef.current) return
    if (browse.root && !isInside(browse.root, target)) {
      // The mounted tree cannot contain this file, so wiki-viewer would answer
      // "Invalid path". Remount at a root that can.
      setBrowse({ root: pickEmbedRoot(target, sessionCwd), file: target })
      return
    }
    if (iframeLoaded) sendOpenFile(target)
    else pendingPathRef.current = target
  }

  const browseRoot = browse?.root
  const browseFile = browse?.file ?? null

  // Resolve status and build the embed URL. Keyed on the mount, so it runs once
  // per browse mount and never in response to a file open.
  useEffect(() => {
    if (!browse) return
    let cancelled = false
    const controller = new AbortController()
    setStatus('checking')
    setEmbedSrc(null)

    void (async () => {
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
        if (!browseRoot && !health.configured) { setStatus('unconfigured'); return }

        const params = new URLSearchParams()
        if (browseRoot) params.set('root', browseRoot)
        if (browseFile) params.set('file', browseFile)

        const eres = await fetch('/api/wiki/embed-url?' + params.toString(), {
          signal: controller.signal,
        })
        if (cancelled) return

        if (!eres.ok) {
          const body: { error?: string } = await eres.json().catch(() => ({}))
          switch (body.error) {
            case 'api_key_required': setStatus('no-key'); break
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
        // already shows it. Remember it so we do not immediately postMessage a
        // navigation to the file that is already open.
        bakedPathRef.current = browseFile
        shownBrowseFileRef.current = browseFile
        // Capture the origin from the returned URL for postMessage verification.
        try { wikiOriginRef.current = new URL(url).origin } catch { /* keep the raw wikiUrl fallback */ }
        // Deliberately not logged: this URL carries the API key.
        setEmbedSrc(url)
        setStatus('ready')
      } catch {
        if (!cancelled) setStatus('offline')
      }
    })()

    return () => { cancelled = true; controller.abort() }
  }, [browse, browseRoot, browseFile])

  /**
   * "Open in wiki-viewer" (new tab) reuses the server-built URL, because that is
   * the only thing carrying ?root= and the API key: the frontend cannot rebuild
   * either, since the key is masked in the preferences GET.
   *
   * embed=1 is deliberately KEPT. wiki-viewer validates ?api_key= only inside
   * `if (isEmbed)`, so dropping embed=1 makes the key (and the embed cookie)
   * ignored and the navigation 307s to /signin. Verified: with embed=1 it is
   * 200, without it is 307 even with a valid key and cookie. That coupling is
   * intentional on their side, since the api-key identity is synthetic and
   * honoring it outside embed mode would expose Settings, which displays the key.
   *
   * chrome=1 is set again here rather than inherited: a full tab must show the
   * sidebar regardless of what the panel currently asks for.
   */
  const externalHref = (() => {
    if (!embedSrc) return null
    try {
      const u = new URL(embedSrc, window.location.origin)
      u.searchParams.set('chrome', '1')
      // Reactive state, not shownBrowseFileRef: a ref read during render does
      // not refresh the href when a postMessage navigation changes it.
      const shown = resolvedPath ?? browseFile
      if (shown) u.searchParams.set('file', shown)
      return u.toString()
    } catch {
      return null
    }
  })()

  const handleIframeLoad = () => {
    setIframeLoaded(true)
    const pending = pendingPathRef.current
    pendingPathRef.current = null
    // Consume the baked path exactly once: a later click on the same file must
    // still send, since by then the user may have navigated away from it.
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

  const title = view === 'file'
    ? (resolvedPath ? basename(resolvedPath) : filePath ? basename(filePath) : 'Files')
    : 'Wiki'

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
        <div className="flex items-center justify-between gap-2 px-3 py-1.5 border-b border-hairline shrink-0">
          <span className="text-[11px] font-bold uppercase tracking-widest text-mute truncate">
            {title}
          </span>
          <div className="flex items-center gap-2 shrink-0">
            {view === 'file' && filePath && (
              <button
                onClick={() => setReloadSeq(n => n + 1)}
                title="Reload file"
                className="p-1 text-mute hover:text-primary transition-colors"
              >
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
                  <polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/>
                </svg>
              </button>
            )}
            {view === 'file' ? (
              <button
                onClick={showBrowse}
                title="Browse and edit in wiki-viewer"
                className="px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-mute hover:text-primary transition-colors"
              >
                Wiki
              </button>
            ) : (
              <button
                onClick={() => setView('file')}
                title="Back to the file view"
                className="px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-mute hover:text-primary transition-colors"
              >
                File
              </button>
            )}
            {view === 'browse' && externalHref && (
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

        {/* FILE surface. Kept mounted rather than unmounted so switching to the
            wiki and back does not refetch, and so the iframe below keeps its
            state. display is set inline on purpose: Tailwind's `hidden` and
            `flex` are the same utility group with equal specificity, so which
            one wins depends on stylesheet order, and losing that race renders a
            blank pane with a clean console. */}
        <div
          className="flex-col flex-1 min-h-0"
          style={{ display: view === 'file' ? 'flex' : 'none' }}
        >
          <FilePane
            path={resolvedPath}
            hostId={hostId}
            openNonce={openNonce}
            reloadSeq={reloadSeq}
          />
        </div>

        {/* BROWSE surface. Mounted on first request and kept mounted after, so
            an in-progress edit survives a detour through the file view. */}
        {browse && (
          <div
            className="flex-col flex-1 min-h-0"
            style={{ display: view === 'browse' ? 'flex' : 'none' }}
          >
            {status === 'offline' && (
              <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
                <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
                  <circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/>
                </svg>
                <p className="text-[12px] text-mute">wiki-viewer is not running</p>
                <p className="text-[11px] text-mute/60">
                  Start it with <code className="font-mono bg-surface px-1 py-0.5 rounded text-[10px]">npx wiki-viewer</code>
                </p>
                <p className="text-[11px] text-mute/60">Viewing files does not need it.</p>
              </div>
            )}

            {status === 'no-key' && (
              <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
                <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
                  <path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4"/>
                </svg>
                <p className="text-[12px] text-mute">API key required</p>
                <p className="text-[11px] text-mute/60">
                  Browsing the wiki needs wiki-viewer's API key. Copy it from wiki-viewer's
                  Settings → API Access, then paste it into Settings → Integrations.
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
                <p className="text-[11px] text-mute/60 font-mono break-all">{browseRoot}</p>
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
        )}
      </div>
    </div>
  )
}
