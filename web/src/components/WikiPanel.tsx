import { useEffect, useRef, useState } from 'react'
import { pickEmbedRoot, buildWikiSrc } from '../lib/wikiRoot'

interface WikiStatus {
  installed: boolean
  installing: boolean
  running: boolean
  version: string
  error: string
  default_root: string
}

interface WikiPanelProps {
  filePath: string | null
  /**
   * Bumped on every open request, even when the path is unchanged. Without it a
   * second open of the same file is a no-op, because the send effect keys on the
   * resolved path. That is easy to hit now that one panel serves every pane:
   * open A from one pane, then A from another, and nothing would happen.
   */
  openNonce?: number
  /** Session cwd. Sent as wiki-viewer's ephemeral root so the file resolves
   *  regardless of which workspace wiki-viewer has active. */
  sessionCwd?: string
  /** Peer host ID. Threaded through to the grant call so remote files are
   *  relayed through the mesh control link. */
  hostId?: string
  /** Session name, needed by /file/grant. */
  session?: string
  onClose: () => void
}

export function WikiPanel({ filePath, openNonce, sessionCwd, hostId, session, onClose }: WikiPanelProps) {
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const [width, setWidth] = useState(480)
  const dragRef = useRef<{ startX: number; startWidth: number } | null>(null)
  const [isMobile, setIsMobile] = useState(() => window.matchMedia('(max-width: 900px), (pointer: coarse)').matches)

  // Detect mobile via matchMedia with change listener
  useEffect(() => {
    const media = window.matchMedia('(max-width: 900px), (pointer: coarse)')
    const sync = () => setIsMobile(media.matches)
    media.addEventListener('change', sync)
    return () => media.removeEventListener('change', sync)
  }, [])

  // wiki-viewer lifecycle status, polled for the overlay.
  const [wikiStatus, setWikiStatus] = useState<WikiStatus | null>(null)
  const pollCountRef = useRef(0)
  const pollTimerRef = useRef<number | null>(null)

  useEffect(() => {
    let cancelled = false
    pollCountRef.current = 0

    const poll = async () => {
      if (cancelled) return
      try {
        const res = await fetch('/api/wiki/status')
        if (!res.ok) throw new Error('status')
        const status: WikiStatus = await res.json()
        if (cancelled) return
        setWikiStatus(status)
        if (!status.running && pollCountRef.current < 30) {
          pollCountRef.current++
          pollTimerRef.current = window.setTimeout(poll, 1000)
        }
      } catch {
        if (!cancelled && pollCountRef.current < 30) {
          pollCountRef.current++
          pollTimerRef.current = window.setTimeout(poll, 1000)
        }
      }
    }

    poll()
    return () => {
      cancelled = true
      if (pollTimerRef.current !== null) clearTimeout(pollTimerRef.current)
    }
  }, [])

  // iframe state
  const [iframeSrc, setIframeSrc] = useState<string | null>(null)
  const [currentPath, setCurrentPath] = useState<string | null>(null)
  const [iframeLoaded, setIframeLoaded] = useState(false)
  const iframeLoadedRef = useRef(false)
  const pendingPathRef = useRef<string | null>(null)
  const bakedPathRef = useRef<string | null>(null)
  const currentRootRef = useRef<string | null>(null)
  // Set when the most recent open request failed to resolve to a real file
  // (bad path, no active pane cwd, peer unreachable, ...). Surfaced by the
  // panel itself -- wiki-viewer never sees a request it can't satisfy, so it
  // has no error of its own to show, and the panel used to just stay on its
  // blank "select a file" screen with no indication anything went wrong.
  const [openError, setOpenError] = useState<string | null>(null)

  // Resolve root and file target from inputs. Keyed on the three values that
  // represent a distinct open request. Root changes rebuild the iframe src;
  // file-only changes send a postMessage to the already-loaded iframe.
  useEffect(() => {
    let cancelled = false

    ;(async () => {
      let root: string | undefined
      let file: string | null = null
      let error: string | null = null

      if (filePath) {
        // Resolve the file path server-side through the grant endpoint.
        // Handles relative paths against session cwd and ~ expansion.
        // For remote peers (hostId set), materialises the file locally.
        // For local files, returns {token, path} with no root.
        const qs = `path=${encodeURIComponent(filePath)}&session=${encodeURIComponent(session || '')}&host=${encodeURIComponent(hostId || '')}`
        try {
          const gr = await fetch(`/file/grant?${qs}`, { method: 'POST' })
          if (gr.ok) {
            const grant: { token?: string; path?: string; root?: string; is_dir?: boolean } = await gr.json()
            if (grant.is_dir && grant.path) {
              // Directory: open browse mode rooted at it, no file target.
              root = grant.path
            } else {
              if (grant.root) root = grant.root
              if (grant.path) file = grant.path
            }
          } else {
            error = (await gr.text().catch(() => '')).trim() || `could not open file (${gr.status})`
          }
        } catch {
          error = 'could not reach server to open file'
        }

        // A grant only carries a root when it materialised the file into a
        // private temp dir, which happens for genuinely remote peers. A local
        // host returns {token, path}: the path is already a real local file,
        // so no root comes back. When no root is returned by the grant,
        // fall back to the usual cwd/file-dir logic: cwd when it contains
        // the file, file dir otherwise.
        if (!root && file) root = pickEmbedRoot(file, sessionCwd)
      } else {
        // No file: root the panel at the configured default so the user can
        // browse. Fetch status inline; the overlay fetches it independently.
        try {
          const sr = await fetch('/api/wiki/status')
          if (sr.ok) {
            const st: WikiStatus = await sr.json()
            root = st.default_root || '/'
          }
        } catch {
          root = '/'
        }
      }

      if (cancelled) return

      setOpenError(error)
      if (error) {
        // Surface the failure where the user is looking (the panel itself
        // asked for this file) rather than only inside the panel body, so a
        // failed open on top of an already-open file isn't silently
        // swallowed once the overlay below is hidden by an existing iframe.
        window.dispatchEvent(new CustomEvent('termyard:toast', {
          detail: { severity: 'warn', source: 'wiki-panel', message: `Could not open ${filePath}: ${error}` },
        }))
      }
      if (!root) return

      const rootChanged = root !== currentRootRef.current

      if (rootChanged) {
        currentRootRef.current = root
        bakedPathRef.current = file
        setIframeSrc(buildWikiSrc({ root, file }))
      }

      setCurrentPath(file)

      // Same root, different file: tell the running iframe to navigate.
      if (!rootChanged && file && file !== bakedPathRef.current) {
        if (iframeLoadedRef.current) {
          const win = iframeRef.current?.contentWindow
          if (win) {
            win.postMessage({ type: 'open-file', path: file }, window.location.origin)
          }
        } else {
          pendingPathRef.current = file
        }
      }
    })()

    return () => { cancelled = true }
    // sessionCwd and session are deliberately excluded: the task contract keys
    // this effect on the three values below, the same way the original code
    // keyed the embed-url effect on effectiveRoot alone so a focus change would
    // not tear down the iframe.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filePath, openNonce, hostId])

  // A new embed URL means a fresh document; the load gate must reset with it.
  useEffect(() => {
    setIframeLoaded(false)
    iframeLoadedRef.current = false
  }, [iframeSrc])

  const handleIframeLoad = () => {
    setIframeLoaded(true)
    iframeLoadedRef.current = true
    const baked = bakedPathRef.current
    bakedPathRef.current = null
    const pending = pendingPathRef.current
    pendingPathRef.current = null
    if (pending && pending !== baked) {
      const win = iframeRef.current?.contentWindow
      if (win) {
        win.postMessage({ type: 'open-file', path: pending }, window.location.origin)
      }
    }
  }

  // "Open in wiki-viewer" (new tab). Same-origin, carries no API key. Re-points
  // file= at the currently shown path so the tab sees the same file as the
  // panel even after postMessage navigation.
  const externalHref = (() => {
    if (!iframeSrc || !currentPath) return null
    try {
      const u = new URL(iframeSrc, window.location.origin)
      u.searchParams.set('chrome', '1')
      u.searchParams.set('file', currentPath)
      return u.toString()
    } catch {
      return null
    }
  })()

  // Drag resize
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

  const basename = currentPath
    ? currentPath.split('/').pop()
    : filePath
      ? filePath.split('/').pop()
      : 'Files'

  // Exactly one overlay, driven by the polled status.
  const overlay = (() => {
    if (!wikiStatus) {
      return (
        <div className="flex items-center justify-center flex-1 gap-2 text-mute/50">
          <span className="inline-block h-3 w-3 rounded-full border-2 border-current border-t-transparent animate-spin" />
          <span className="text-[11px]">Connecting...</span>
        </div>
      )
    }
    if (!wikiStatus.installed) {
      return (
        <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
          <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
            <path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><polyline points="9 22 9 12 15 12 15 22"/>
          </svg>
          <p className="text-[12px] text-mute">wiki-viewer is not installed</p>
          <p className="text-[11px] text-mute/60">
            Open Settings &rarr; Integrations to install the file viewer.
          </p>
        </div>
      )
    }
    if (!wikiStatus.running) {
      if (wikiStatus.error) {
        return (
          <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
            <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
              <circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/>
            </svg>
            <p className="text-[12px] text-mute">wiki-viewer failed to start</p>
            <p className="text-[11px] text-mute/60 font-mono break-all">{wikiStatus.error}</p>
          </div>
        )
      }
      return (
        <div className="flex items-center justify-center flex-1 gap-2 text-mute/50">
          <span className="inline-block h-3 w-3 rounded-full border-2 border-current border-t-transparent animate-spin" />
          <span className="text-[11px]">Starting wiki-viewer...</span>
        </div>
      )
    }
    // wiki-viewer is up but the panel has nothing to show it: the requested
    // file failed to resolve (bad path, no active pane cwd, ...) and there is
    // no previously-open file to fall back to. Without this, the panel body
    // was just blank -- correct per the underlying state, but indistinguishable
    // from "still loading" or a bug.
    if (openError && !iframeSrc) {
      return (
        <div className="flex flex-col items-center justify-center flex-1 gap-3 px-6 text-center">
          <svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="text-mute/50">
            <path d="M9.5 2h5l7 12.5-3.5 6h-12l-3.5-6z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="16" x2="12" y2="16"/>
          </svg>
          <p className="text-[12px] text-mute">Could not open file</p>
          {filePath && (
            <p className="text-[11px] text-mute/60 font-mono break-all">{filePath}</p>
          )}
          <p className="text-[11px] text-mute/60">{openError}</p>
        </div>
      )
    }
    return null
  })()

  return (
    <div className={isMobile ? 'fixed inset-0 z-40 bg-canvas flex flex-row' : 'flex flex-row h-full shrink-0 border-l border-hairline'} style={!isMobile ? { width } : {}}>
      {!isMobile && (
        <div
          className="w-1 cursor-col-resize bg-transparent hover:bg-primary/30 transition-colors shrink-0"
          onMouseDown={e => {
            e.preventDefault()
            dragRef.current = { startX: e.clientX, startWidth: width }
          }}
        />
      )}

      <div className="flex flex-col flex-1 min-w-0 bg-canvas">
        <div className="flex items-center justify-between px-3 py-1.5 border-b border-hairline shrink-0">
          <span className="text-[11px] font-bold uppercase tracking-widest text-mute truncate">
            {basename}
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

        {overlay}

        {wikiStatus?.running && iframeSrc && (
          <iframe
            ref={iframeRef}
            src={iframeSrc}
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
